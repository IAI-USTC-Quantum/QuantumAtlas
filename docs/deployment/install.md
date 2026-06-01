# 安装与 service 注册

两步：

1. **`install-qatlasd.sh`** 下载 binary（不动 systemd）
2. **`qatlasd service install`** 注册成 systemd / launchd / SCM 服务

两步分开是有意的——install 脚本要 POSIX sh 极简，service 注册需要交互 / 多 flag，分开后各自负责自己的事。

## 一行装 binary

```bash
# 装最新 release 到 ~/.local/bin/qatlasd
curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh

# 锁定版本
curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh -s -- --version v0.2.8

# 装到指定目录
curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh -s -- --dir /opt/qatlas/bin

# 环境变量同义
QATLAS_VERSION=v0.2.8 QATLAS_INSTALL_DIR=/opt/qatlas/bin \
    curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh
```

支持 `linux/{amd64,arm64}` + `darwin/arm64` 三个平台。Intel Mac 故意不发预编 binary（GitHub Actions `macos-13` runner 排队 10–40 分钟），改走 `go install github.com/IAI-USTC-Quantum/QuantumAtlas/cmd/qatlasd@latest` 自编。脚本本身**不做二次哈希校验**——下载完整性靠 HTTPS（curl/wget 校验 GitHub CA 链）保证，从同一个 release 拉 `SHA256SUMS` 来比对是自签名（能改 binary 的攻击者同时改了 manifest）。需要更强保证的场景请走下面 [Release 资产的校验方式](#release-资产的校验方式)。

### 脚本内部行为

1. 检测 OS/arch
2. 解析 GitHub Release 的 `latest` redirect 拿 tag（不调 API，避免限流）
3. HTTPS 下载 `qatlasd-<os>-<arch>` 到目标目录
4. `chmod +x`
5. 打印 next-step 提示

**全 POSIX sh**（不依赖 bash），所以 Alpine / BusyBox / macOS sh 都能跑。

### Release 资产的校验方式

每次 release 都通过 [`.github/workflows/release.yml`](https://github.com/IAI-USTC-Quantum/QuantumAtlas/blob/main/.github/workflows/release.yml) 自动生成两类校验产物，全部走业界标准格式，可被通用工具直接消费。两条路径都是**可选**的手动操作，给愿意做额外验证的用户用。

#### 1. `SHA256SUMS`（POSIX `sha256sum` 标准格式）

Release 资产里有一个 `SHA256SUMS` 文件，每行 `<sha256>  <basename>`，覆盖**全部** release 产物（3 个 binary + wheel + sdist）。**有意义的用法是跨网络/跨时段比对**——在 GitHub Release 网页上肉眼记下 hash，或在一台机器上拉 manifest、在另一台机器/镜像源拉到 binary 后本地校验：

```bash
# Linux / WSL
sha256sum -c --ignore-missing SHA256SUMS

# macOS / BSD
shasum -a 256 -c --ignore-missing SHA256SUMS
```

`--ignore-missing` 让校验只针对当前目录里实际存在的文件，省去预先 grep 出自己关心那一行的麻烦。能挡：跨源传输不一致、镜像投毒、本地落盘损坏。**挡不了**有 release 写权限的攻击者同时改 binary + SHA256SUMS 的"完整链替换"——那种攻击的防线是下面的 attestation。

#### 2. SLSA Build Provenance Attestation（Sigstore Bundle 标准格式）

每个 release artifact 在打包时同步用 [`actions/attest-build-provenance`](https://github.com/actions/attest-build-provenance) 走 GitHub OIDC + Sigstore 公开实例**密钥学签名**，把 `(artifact digest, repo, commit, workflow file, runner, timestamp)` 绑定起来。attestation 落 GitHub 自家的 Attestations API（不是 release 资产），有两种通用工具可验，都不依赖任何专属凭据：

```bash
# 方式 A: gh CLI（最简单，自动拉 bundle + 验签 + 验 cert identity 一气呵成）
gh attestation verify ./qatlasd-linux-amd64 --repo IAI-USTC-Quantum/QuantumAtlas

# 方式 B: 纯 curl + cosign（任何机器，零安装 gh CLI，可断网验证）
ARTIFACT=qatlasd-linux-amd64
DIGEST=$(sha256sum "$ARTIFACT" | awk '{print $1}')
curl -fsSL -H "Accept: application/vnd.github+json" \
  "https://api.github.com/repos/IAI-USTC-Quantum/QuantumAtlas/attestations/sha256:${DIGEST}" \
  | jq -r '.attestations[0].bundle' > qatlasd.sigstore.json
cosign verify-blob "$ARTIFACT" \
  --bundle qatlasd.sigstore.json \
  --certificate-identity-regexp '^https://github\.com/IAI-USTC-Quantum/QuantumAtlas/\.github/workflows/release\.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

attestation 能挡的额外攻击面：源仓库写权限被劫持后的恶意 release（重新签名需要在 `IAI-USTC-Quantum/QuantumAtlas` 仓库的 release.yml workflow 里跑出 OIDC token，PAT / Personal Token 拿不到）、typosquatting fork 钓鱼（cert identity 直接绑 source repo path）。日常装机不需要这一步——SLSA attestation 是给安全敏感场景（CI/CD pipeline、企业 SRE 审计、需要 SLSA L3 合规凭证的部署）的可选强校验路径。

### 常见错误

!!! failure "404 release artifact"
    指定的 tag 没有该平台的 binary。检查 [releases](https://github.com/IAI-USTC-Quantum/QuantumAtlas/releases)；通常 `v0.2.3+` 才有 binary。

!!! failure "Permission denied: ~/.local/bin"
    `~/.local/bin` 不存在或不可写。脚本会尝试 `mkdir -p`，但如果父目录权限不对，会失败。手动 `mkdir -p ~/.local/bin` 或换 `--dir /tmp/qatlas` 测试。

!!! failure "TAG 解析出错（罕见，私库 / 网络问题）"
    脚本 fail-loud，给出 GitHub 实际 redirect 内容。检查网络 + repo 可访问性。

## 注册为系统服务

binary 装好后，启动一次确认它能跑：

```bash
qatlasd --version
# qatlasd version 0.2.8
```

然后用 `service install` 注册成 systemd：

```bash
qatlasd service install
```

交互模式会：

1. 让你选 mode：**user**（systemd user unit，单用户后台）vs **system**（systemd system unit，root 跑 / 监听低端口）
2. 让你确认 `.env` 路径（从 `$QATLAS_DOTENV` → `~/QuantumAtlas/.env` → `./.env` 自动检测）
3. 渲染 unit 文件到 stdout 让你 review
4. `[Y/n]` 确认 → 写盘 + `systemctl daemon-reload` + `systemctl start`

### 全自动模式（CI / agent / one-liner）

非 TTY 环境必须显式 `--mode` 和 `--force`：

```bash
# user mode（不需要 sudo）
qatlasd service install \
    --mode user \
    --dotenv-path ~/QuantumAtlas/.env \
    --force

# system mode（需要 sudo）
sudo qatlasd service install \
    --mode system \
    --dotenv-path /etc/quantum-atlas/.env \
    --bind 127.0.0.1:4200 \
    --force
```

!!! danger "system mode 必须用 `sudo qatlasd ...`，**不能**用 `sudo -u <user> qatlasd ...`"

    System unit 写到 `/etc/systemd/system/`，需要 root 写权限——这意味着
    `qatlasd service install --mode system` 的进程**必须**以 EUID=0 运行。
    同时，渲染出的 unit 里的 `User=` 字段、`ReadWritePaths=` 的家目录锚点
    都由 binary 内部从 `$SUDO_USER` 反推（见 `cmd/qatlasd/service_cmd.go::resolveSystemUser`
    + `effectiveHomeDir`）。

    | 调用方式 | EUID | `$SUDO_USER` | 结果 |
    |---|---|---|---|
    | `sudo qatlasd service install ...` | 0 ✓ | 你的 login user ✓ | **正确**：写得了 unit，`User=<你>` |
    | `sudo -u alice qatlasd service install ...` | alice 的 uid ❌ | `root`（sudo 调用者） ❌ | 双重错：1）EUID≠0 写不了 `/etc/systemd/system/` 直接 `permission denied`；2）即便能写，`User=root` 进 unit，daemon 用 root 跑 |
    | `sudo bash script.sh` 里调 `qatlasd service install ...` | 0 ✓ | 你的 login user ✓（外层 `sudo` 设的） | **正确**：跟方式 1 等价；适合写到部署脚本里 |

    自动化脚本里**绝对不要**在 `sudo bash ...` 之内再套 `sudo -u`——
    那是把"我要换运行身份"和"我要写系统文件"两个目标硬拗到一个命令上，
    永远是冲突的。

| Flag | 默认 | 含义 |
|---|---|---|
| `--mode user\|system` | TTY 询问；非 TTY 必填 | unit 安装位置 |
| `--dotenv-path <path>` | auto-detect | 写入 unit 的 `Environment=QATLAS_DOTENV=` |
| `--bind <addr>` | `127.0.0.1:4200` | `serve --http=` 的值 |
| `--name <name>` | `qatlasd` | unit 名（影响 `<name>.service`）|
| `--dry-run` | false | 只渲染 unit，不写盘 |
| `--force` | false | 已有同名 unit 直接覆盖；非 TTY 必填 |

### 渲染出来的 unit 长什么样

system mode 下 unit 在 `/etc/systemd/system/qatlasd.service`，大致如下：

```ini title="/etc/systemd/system/qatlasd.service"
[Unit]
Description=QuantumAtlas server
After=network-online.target

[Service]
Type=simple
User=timidly
Group=timidly
WorkingDirectory=/home/timidly
Environment=QATLAS_DOTENV=/etc/quantum-atlas/.env
ExecStart=/home/timidly/.local/bin/qatlasd serve --http=127.0.0.1:4200
Restart=on-failure
RestartSec=5

# Defense in depth
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=no
LockPersonality=true
RestrictRealtime=true
ReadWritePaths=/etc/quantum-atlas /var/lib/quantum-atlas /home/timidly/.local/share/quantum-atlas

[Install]
WantedBy=multi-user.target
```

!!! warning "ReadWritePaths 坑"
    `ReadWritePaths=` 里列的目录**任何一个不存在就让 service 拿 `status=226/NAMESPACE`**，但**老进程因为 namespace 已建好不受影响**——所以坏配置可能潜伏到下次 restart 才暴露。改 `.env` 的存储路径后要：

    ```bash
    mkdir -p /var/lib/quantum-atlas/{raw,data,pb_data}     # 确认新路径存在
    sudo systemctl daemon-reload                            # 让 systemd 重读 unit
    sudo systemctl restart qatlasd                    # 试一次 restart
    ```

### 验证

```bash
# 服务状态
qatlasd service status
# 或等价
systemctl status qatlasd

# 看日志
journalctl -u qatlasd -n 50

# 健康检查
curl http://127.0.0.1:4200/api/health | jq
```

应该看到 `"status": "healthy"` 或 `"degraded"`（degraded 不一定是 bug——可能只是某些 dependency 没配；看 `checks` 详情）。

## 升级 binary

不复杂——再跑一次 install 脚本即可（脚本会覆盖旧 binary）：

```bash
# 装 v0.2.9
curl -fsSL https://quantum-atlas.ai/install-qatlasd.sh | sh -s -- --version v0.2.9

# 让 service 用新 binary
sudo systemctl restart qatlasd

# 看新版本起来没
curl http://127.0.0.1:4200/api/health | jq .data.version
```

详细的升级 + 备份策略见 [备份与升级](backup-and-upgrade.md)。

## 卸载

```bash
# 停 + 删 unit
qatlasd service uninstall

# 删 binary
trash-put ~/.local/bin/qatlasd

# pb_data / wiki / raw 不会被自动删 —— 你自己决定是否保留
# trash-put ~/.local/share/quantum-atlas/
```

## 不用 service 也行（开发 / 容器场景）

直接前台跑：

```bash
QATLAS_DOTENV=/path/to/.env qatlasd serve --http=0.0.0.0:4200
```

在 Docker / systemd 别的方式管理。
