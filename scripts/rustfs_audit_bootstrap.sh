#!/usr/bin/env bash
# rustfs_audit_bootstrap.sh - 一次性、幂等地在 RustFS 上为 T10「写入留痕 / audit
# sink」铺设对象存储侧资源：建 audit 桶 + 建 sink 专属 svcacct（只对该桶 RW，无
# Delete）+ 给现有 edge 父用户挂一条该桶只读 policy。最后只打印 *sink 的*
# access/secret，填进 NAS 侧 Fluent Bit sidecar 的 S3 output 配置（DSM GUI 管理）。
#
# 为什么单独一支脚本（不复用 rustfs_bootstrap.sh）：
#   - 资源边界完全不同：这里要的是「sink 独立身份」+「edge 只读」，跟 asset 三桶
#     的「单 svcacct 全桶 RW」语义正交。
#   - **权限隔离是 T10 的安全前提**：sink 用 *独立* svcacct（最小权限 + 审计不可变），
#     只拿 audit 桶的 Get/Put/List、**没有 Delete**，也碰不到三个资产桶；绝不复用
#     edge 那把 CNEDAZ2HQDU9TX8A2BUO。自循环由 Fluent Bit 的 `grep` filter（drop
#     api.bucket==qatlas-audit）断，不依赖 accessKey；但独立身份仍是正确的卫生
#     （复用 edge key 既越权、又会在改用 accessKey 过滤断环时把正规 edge 写 drop 光）。
#
# 信任模型（跟 provision edge svcacct 当初一模一样）：
#   - RustFS **root** 凭据一直在运维手里（NAS compose env 里本来就有），只在本脚本
#     临时用于 mc admin，**永不落盘 / 永不进 .env / 永不进 qatlas / 永不进任何
#     long-running 配置**。脚本退出即随 mktemp 工作目录一起销毁。
#   - 脚本只把新生成的 `qatlas-audit-sink` access/secret 经 stdout 单次返回。实施
#     agent 全程只见两把 *scoped* key：sink 的 RW（本脚本输出）+ edge 的只读
#     （edge 现有 svcacct 继承本脚本给父用户挂的只读 policy），**从不见 root**。
#
# 用法（运维在能直连 RustFS 的机器上跑；endpoint 走 mesh 即可）：
#   export RUSTFS_ENDPOINT=http://10.144.18.10:9000
#   export RUSTFS_ROOT_ACCESS_KEY=<root_ak>          # = compose 里的 RUSTFS_ACCESS_KEY
#   export RUSTFS_ROOT_SECRET_KEY=<root_sk>          # = compose 里的 RUSTFS_SECRET_KEY
#   bash scripts/rustfs_audit_bootstrap.sh
#
# 可选覆盖（默认对应当前 T10 部署）：
#   export AUDIT_BUCKET=qatlas-audit
#   export SINK_USER=qatlas-audit-sink              # sink 专属身份
#   export SINK_POLICY=qatlas-audit-rw              # 只对 audit 桶 RW
#   export EDGE_USER=qatlas-server                  # 现有 edge 父用户（挂只读）
#   export RO_POLICY=qatlas-audit-ro                # 只对 audit 桶 只读
#   export SKIP_EDGE_RO=1                            # 不给 edge 挂只读（仅建 sink 侧）
#
# 输出（最后几行）：
#   Access Key: <sink 新生成>
#   Secret Key: <sink 新生成>
#
# rotate（建新对，旧的另起命令删）：
#   bash scripts/rustfs_audit_bootstrap.sh                    # 只新增 svcacct
#   ./mc admin user svcacct rm <alias> <old_sink_access_key>  # 手动删旧

set -uo pipefail

: "${RUSTFS_ENDPOINT:?need RUSTFS_ENDPOINT (e.g. http://10.144.18.10:9000)}"
: "${RUSTFS_ROOT_ACCESS_KEY:?need RUSTFS_ROOT_ACCESS_KEY (= compose RUSTFS_ACCESS_KEY)}"
: "${RUSTFS_ROOT_SECRET_KEY:?need RUSTFS_ROOT_SECRET_KEY (= compose RUSTFS_SECRET_KEY)}"

AUDIT_BUCKET="${AUDIT_BUCKET:-qatlas-audit}"
SINK_USER="${SINK_USER:-qatlas-audit-sink}"
SINK_POLICY="${SINK_POLICY:-qatlas-audit-rw}"
EDGE_USER="${EDGE_USER:-qatlas-server}"
RO_POLICY="${RO_POLICY:-qatlas-audit-ro}"
SKIP_EDGE_RO="${SKIP_EDGE_RO:-0}"
ALIAS="rustfs_audit_bootstrap_$$"

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"; unset "MC_HOST_${ALIAS}"' EXIT

echo "[1/6] downloading mc to $WORKDIR ..."
curl -sSL -o "$WORKDIR/mc" https://dl.min.io/client/mc/release/linux-amd64/mc
chmod +x "$WORKDIR/mc"
MC="$WORKDIR/mc"

# Build the alias URL scheme-agnostically (mesh endpoint is http://, the LE
# edge endpoint is https://). rustfs_bootstrap.sh only substituted https://;
# here we split scheme/rest so both work.
_scheme="${RUSTFS_ENDPOINT%%://*}"
_rest="${RUSTFS_ENDPOINT#*://}"
export MC_HOST_${ALIAS}="${_scheme}://${RUSTFS_ROOT_ACCESS_KEY}:${RUSTFS_ROOT_SECRET_KEY}@${_rest}"
unset _scheme _rest

# sanity check
"$MC" --quiet alias list "$ALIAS" >/dev/null 2>&1 || {
  echo "ERROR: mc alias setup failed; check RUSTFS_ENDPOINT / credentials" >&2
  exit 1
}

echo "[2/6] ensure audit bucket: $AUDIT_BUCKET"
if "$MC" --quiet ls "$ALIAS/$AUDIT_BUCKET" >/dev/null 2>&1; then
  echo "      bucket $AUDIT_BUCKET already exists, skip"
else
  "$MC" --quiet mb "$ALIAS/$AUDIT_BUCKET"
  echo "      bucket $AUDIT_BUCKET created"
fi
# NOTE: deliberately NO bucket versioning on the audit bucket. Audit objects are
# write-once-immutable (one-event-one-object, key = <date>/<request_id>.json),
# never overwritten, so there are no noncurrent versions to keep. The asset
# buckets need versioning for rollback; the audit trail does not.

echo "[3/6] ensure sink RW policy: $SINK_POLICY (scoped to $AUDIT_BUCKET only)"
# Sink RW policy — exactly the ops the audit-sink performs and nothing more:
#   - s3:PutObject          write one object per audit event
#   - s3:GetObject          read-back / startup self-check
#   - s3:ListBucket
#     + s3:GetBucketLocation HEAD-bucket / BucketExists probe (objstore.Ping)
#
# Deliberately NOT granted (least privilege for an *audit writer*):
#   - s3:DeleteObject / s3:DeleteObjectVersion: the sink must never be able to
#     erase the audit trail. Pruning old audit objects, if ever needed, is a
#     deliberate root-only op — keeping it out of the sink identity preserves
#     audit immutability (the whole point of T10's forensics posture). Flip
#     this only if you consciously accept a self-erasable audit log.
#   - s3:*BucketVersioning: no versioning on this bucket (see [2/6]); the
#     audit-sink subcommand must skip EnsureVersioning for qatlas-audit.
SINK_POLICY_FILE="$WORKDIR/${SINK_POLICY}.json"
cat > "$SINK_POLICY_FILE" <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject"
      ],
      "Resource": [
        "arn:aws:s3:::${AUDIT_BUCKET}/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:ListBucket",
        "s3:GetBucketLocation"
      ],
      "Resource": [
        "arn:aws:s3:::${AUDIT_BUCKET}"
      ]
    }
  ]
}
EOF
"$MC" --quiet admin policy create "$ALIAS" "$SINK_POLICY" "$SINK_POLICY_FILE" 2>&1 \
  | grep -vE "(already exists|^$)" || true

echo "[4/6] ensure sink user $SINK_USER + attach $SINK_POLICY"
if "$MC" --quiet admin user info "$ALIAS" "$SINK_USER" >/dev/null 2>&1; then
  echo "      user $SINK_USER already exists, skip"
else
  # random console password — never printed, sink never logs into a console.
  TMP_PWD=$(openssl rand -base64 24)
  "$MC" --quiet admin user add "$ALIAS" "$SINK_USER" "$TMP_PWD"
  unset TMP_PWD
  echo "      user $SINK_USER created"
fi
"$MC" --quiet admin policy attach "$ALIAS" "$SINK_POLICY" --user "$SINK_USER" 2>&1 \
  | grep -vE "(already attached|specified policy.*not attached|^$)" || true

echo "[5/6] ensure edge read-only policy $RO_POLICY + attach to $EDGE_USER"
if [ "$SKIP_EDGE_RO" = "1" ]; then
  echo "      SKIP_EDGE_RO=1 set, skipping edge read grant"
else
  # Edge Go reads qatlas-audit/<date>/ objects on a timer to scan for anomalies
  # (accessKey != expected svcacct / accessKey == root / unexpected remotehost).
  # Read-only is all it needs. We attach this to the *parent user* qatlas-server;
  # the existing edge svcacct (CNEDAZ2HQDU9TX8A2BUO) inherits the union of its
  # parent user's policies (it was minted without an inline --policy), so the
  # read grant propagates automatically — no new edge key, no .env change.
  RO_POLICY_FILE="$WORKDIR/${RO_POLICY}.json"
  cat > "$RO_POLICY_FILE" <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "s3:GetObject"
      ],
      "Resource": [
        "arn:aws:s3:::${AUDIT_BUCKET}/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:ListBucket",
        "s3:GetBucketLocation"
      ],
      "Resource": [
        "arn:aws:s3:::${AUDIT_BUCKET}"
      ]
    }
  ]
}
EOF
  "$MC" --quiet admin policy create "$ALIAS" "$RO_POLICY" "$RO_POLICY_FILE" 2>&1 \
    | grep -vE "(already exists|^$)" || true
  if "$MC" --quiet admin user info "$ALIAS" "$EDGE_USER" >/dev/null 2>&1; then
    "$MC" --quiet admin policy attach "$ALIAS" "$RO_POLICY" --user "$EDGE_USER" 2>&1 \
      | grep -vE "(already attached|specified policy.*not attached|^$)" || true
    echo "      attached $RO_POLICY to $EDGE_USER (edge svcacct inherits)"
  else
    echo "      WARN: edge user $EDGE_USER not found; created $RO_POLICY but did" >&2
    echo "            NOT attach it. Attach manually once edge user exists:" >&2
    echo "            mc admin policy attach <alias> $RO_POLICY --user $EDGE_USER" >&2
  fi
fi

echo "[6/6] create new service account (access key pair) for $SINK_USER"
echo "---"
"$MC" admin user svcacct add "$ALIAS" "$SINK_USER"
echo "---"
echo
echo "DONE. Hand ONLY the Access Key + Secret Key above to the audit-sink agent."
echo "Do NOT share the RustFS root key. The sink uses these in NAS compose:"
echo "  # qatlas-audit-sink service env (NAS DSM compose, sidecar):"
echo "  QATLAS_S3_ENDPOINT=${RUSTFS_ENDPOINT}"
echo "  QATLAS_S3_BUCKET_AUDIT=${AUDIT_BUCKET}"
echo "  QATLAS_AUDIT_SINK_ACCESS_KEY_ID=<Access Key from above>"
echo "  QATLAS_AUDIT_SINK_SECRET_ACCESS_KEY=<Secret Key from above>"
echo
echo "The sink dedup key is THIS access key: the sink drops events whose"
echo "accessKey == its own (and bucket == ${AUDIT_BUCKET}) to break the"
echo "write -> audit -> write self-loop. Never dedup by User-Agent (forgeable)."
echo
echo "Edge Go reads ${AUDIT_BUCKET} via its EXISTING svcacct (now inheriting"
echo "${RO_POLICY} read-only) — no new edge key needed."
echo
echo "To list all keys for the sink user later:"
echo "  mc admin accesskey ls <alias> --user $SINK_USER"
