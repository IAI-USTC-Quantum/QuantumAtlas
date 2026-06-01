#!/usr/bin/env bash
# rustfs_bootstrap.sh - 在 RustFS 上幂等创建 bucket + IAM user + 限定 policy，
# 并打印新生成的 access_key/secret_key 供写入 server .env。
#
# 复用场景：
#   - 首次部署时创建 qatlas-{pdf,md,images} 三桶 + qatlasd svcacct
#   - 灾难恢复后重建对象存储侧权限
#   - 之后再开新桶（如 qatlas-openalex）只需把它加进 BUCKETS 变量
#
# 设计要点：
#   - 用 MinIO Client (mc) 调 RustFS 的 admin API（RustFS 兼容 MinIO admin）
#   - root 凭据走 env vars MC_HOST_<alias>，**永不落盘**，脚本退出时连同 mc binary 一起销毁
#   - 所有步骤幂等：bucket 存在则跳过；user/policy 存在则只确保 attach 关系
#   - 生成的 service account 凭据通过 stdout 单次返回；脚本不写入任何文件
#   - 单个 svcacct + 单个 policy 覆盖全部三桶（一份凭据写所有 asset 桶）
#
# 不做的事：
#   - 不自动写 .env（避免 secret 落盘到错位置；调用方按自己 deploy 流程粘贴）
#   - 不删除已有 access key（rotate 是显式操作，见末尾备注）
#
# 用法：
#   export RUSTFS_ENDPOINT=https://raw.quantum-atlas.ai
#   export RUSTFS_ROOT_ACCESS_KEY=<root_ak>
#   export RUSTFS_ROOT_SECRET_KEY=<root_sk>
#   # 可选覆盖（默认对应当前 QuantumAtlas v0.7.0 三桶部署）：
#   # export BUCKETS="qatlas-pdf qatlas-md qatlas-images"
#   # export IAM_USER=qatlasd          # 注意：用 IAM_USER 而非 USER，避免与 shell 内置 $USER 冲突
#   # export POLICY=qatlas-assets-rw
#   bash scripts/rustfs_bootstrap.sh
#
# 输出（最后几行）：
#   Access Key: <新生成>
#   Secret Key: <新生成>
#
# rotate 用法（建一对新 key，旧的另起命令删）：
#   bash scripts/rustfs_bootstrap.sh   # 只会新增 svcacct，不动旧 key
#   ./mc admin user svcacct rm <alias> <old_access_key>   # 手动删旧

set -uo pipefail

: "${RUSTFS_ENDPOINT:?need RUSTFS_ENDPOINT (e.g. https://raw.quantum-atlas.ai)}"
: "${RUSTFS_ROOT_ACCESS_KEY:?need RUSTFS_ROOT_ACCESS_KEY}"
: "${RUSTFS_ROOT_SECRET_KEY:?need RUSTFS_ROOT_SECRET_KEY}"

BUCKETS="${BUCKETS:-qatlas-pdf qatlas-md qatlas-images}"
# IAM_USER (not USER): bash auto-sets $USER to the login name in every
# interactive shell, so "${USER:-default}" never falls through to the
# default. Use IAM_USER to dodge the collision.
IAM_USER="${IAM_USER:-qatlasd}"
POLICY="${POLICY:-qatlas-assets-rw}"
ALIAS="rustfs_bootstrap_$$"

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"; unset "MC_HOST_${ALIAS}"' EXIT

echo "[1/6] downloading mc to $WORKDIR ..."
curl -sSL -o "$WORKDIR/mc" https://dl.min.io/client/mc/release/linux-amd64/mc
chmod +x "$WORKDIR/mc"
MC="$WORKDIR/mc"

# 用 env var 传 alias 凭据，避免写 ~/.mc/config.json
export MC_HOST_${ALIAS}="${RUSTFS_ENDPOINT/https:\/\//https://${RUSTFS_ROOT_ACCESS_KEY}:${RUSTFS_ROOT_SECRET_KEY}@}"

# sanity check
"$MC" --quiet alias list "$ALIAS" >/dev/null 2>&1 || {
  echo "ERROR: mc alias setup failed; check RUSTFS_ENDPOINT / credentials" >&2
  exit 1
}

echo "[2/6] ensure buckets: $BUCKETS"
for B in $BUCKETS; do
  if "$MC" --quiet ls "$ALIAS/$B" >/dev/null 2>&1; then
    echo "      bucket $B already exists, skip"
  else
    "$MC" --quiet mb "$ALIAS/$B"
    echo "      bucket $B created"
  fi
done

echo "[3/6] ensure policy: $POLICY (scoped to buckets: $BUCKETS)"
# Policy grants the IAM user four things on this bucket only:
#   1. Object I/O (Get/Put/Delete) — hot path for paper assets.
#   2. Versioned object I/O (GetObjectVersion / DeleteObjectVersion):
#      separate AWS perms from #1. Stat/Get on the current version
#      works under s3:GetObject, but reading a specific version-id
#      (?versionId=) needs s3:GetObjectVersion. Same split for delete.
#      `qatlasd storage prune` calls DeleteObject with a version-id
#      to drop noncurrent versions, so this perm is required.
#   3. Bucket read (ListBucket/ListBucketVersions/GetBucketLocation):
#      ListBucketVersions backs `storage prune`'s enumeration call.
#   4. Bucket versioning Get/Put — qatlas self-manages versioning at
#      boot via objstore.S3Store.EnsureVersioning, so ops never need
#      to run `mc version enable`. We grant Get + Put so qatlas can
#      read state and skip the Put when already correct (avoids
#      noisy audit-log "config change" events on every boot).
#
# Not granted (deliberately):
#   - s3:GetLifecycleConfiguration / s3:PutLifecycleConfiguration:
#     RustFS 1.0.0-beta.5 rejects these action names ("invalid action").
#     We don't currently use lifecycle anyway — noncurrent versions are
#     retained forever (Synology-Snapshot model), cleanup is via
#     `qatlasd storage prune` not automatic expiration.
#   - s3:DeleteBucket / s3:PutBucketPolicy / s3:PutBucketAcl: bucket
#     destruction and ACL changes are root-only ops, never qatlas's job.
POLICY_FILE="$WORKDIR/${POLICY}.json"
# Build per-bucket resource ARN arrays covering all asset buckets so a
# single svcacct + policy reaches pdf / md / images alike.
obj_arns=""   # arn:.../<bucket>/*   (object-level statement)
buk_arns=""   # arn:.../<bucket>     (bucket-level statement)
for B in $BUCKETS; do
  obj_arns="${obj_arns}        \"arn:aws:s3:::${B}/*\",\n"
  buk_arns="${buk_arns}        \"arn:aws:s3:::${B}\",\n"
done
# strip trailing ",\n"
obj_arns="${obj_arns%,\\n}"
buk_arns="${buk_arns%,\\n}"
printf '%b' "{
  \"Version\": \"2012-10-17\",
  \"Statement\": [
    {
      \"Effect\": \"Allow\",
      \"Action\": [
        \"s3:GetObject\",
        \"s3:PutObject\",
        \"s3:DeleteObject\",
        \"s3:GetObjectVersion\",
        \"s3:DeleteObjectVersion\"
      ],
      \"Resource\": [
${obj_arns}
      ]
    },
    {
      \"Effect\": \"Allow\",
      \"Action\": [
        \"s3:ListBucket\",
        \"s3:ListBucketVersions\",
        \"s3:GetBucketLocation\",
        \"s3:GetBucketVersioning\",
        \"s3:PutBucketVersioning\"
      ],
      \"Resource\": [
${buk_arns}
      ]
    }
  ]
}
" > "$POLICY_FILE"
"$MC" --quiet admin policy create "$ALIAS" "$POLICY" "$POLICY_FILE" 2>&1 \
  | grep -vE "(already exists|^$)" || true

echo "[4/6] ensure user: $IAM_USER"
if "$MC" --quiet admin user info "$ALIAS" "$IAM_USER" >/dev/null 2>&1; then
  echo "      user already exists, skip"
else
  # 生成一个随机 console 登录密码（不打印；user 永不需要 console 登录）
  TMP_PWD=$(openssl rand -base64 24)
  "$MC" --quiet admin user add "$ALIAS" "$IAM_USER" "$TMP_PWD"
  unset TMP_PWD
  echo "      user created"
fi

echo "[5/6] attach policy $POLICY to user $IAM_USER"
"$MC" --quiet admin policy attach "$ALIAS" "$POLICY" --user "$IAM_USER" 2>&1 \
  | grep -vE "(already attached|specified policy.*not attached|^$)" || true

echo "[6/6] create new service account (access key pair) for $IAM_USER"
echo "---"
"$MC" admin user svcacct add "$ALIAS" "$IAM_USER"
echo "---"
echo
echo "DONE. Copy the Access Key + Secret Key above into server .env:"
echo "  QATLAS_S3_ENDPOINT=${RUSTFS_ENDPOINT}"
for B in $BUCKETS; do
  case "$B" in
    *-pdf|qatlas-pdf)    echo "  QATLAS_S3_BUCKET_PDF=${B}" ;;
    *-md|qatlas-md)      echo "  QATLAS_S3_BUCKET_MD=${B}" ;;
    *-images|qatlas-images) echo "  QATLAS_S3_BUCKET_IMAGES=${B}" ;;
    *)                   echo "  # (extra bucket) ${B}" ;;
  esac
done
echo "  QATLAS_S3_ACCESS_KEY_ID=<Access Key from above>"
echo "  QATLAS_S3_SECRET_ACCESS_KEY=<Secret Key from above>"
echo
echo "To list all keys for this user later:"
echo "  mc admin accesskey ls <alias> --user $IAM_USER"

