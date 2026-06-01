# 安装与 service 注册

两步：

1. **`install-server.sh`** 下载 binary（不动 systemd）
2. **`qatlasd service install`** 注册成 systemd / launchd / SCM 服务

两步分开是有意的——install 脚本要 POSIX sh 极简，service 注册需要交互 / 多 flag，分开后各自负责自己的事。

## 一行装 binary

```bash
# 装最新 release 到 ~/.local/bin/qatlasd
curl -fsSL https://quantum-atlas.ai/install-server.sh | sh

# 锁定版本
curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- --version v0.2.8

# 装到指定目录
curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- --dir /opt/qatlas/bin

# 环境变量同义
QATLAS_VERSION=v0.2.8 QATLAS_INSTALL_DIR=/opt/qatlas/bin \
    curl -fsSL https://quantum-atlas.ai/install-server.sh | sh
```

支持 `linux/{amd64,arm64}` + `darwin/{amd64,arm64}` 四个平台。脚本自动 SHA256 校验。

### 脚本内部行为

1. 检测 OS/arch
2. 解析 GitHub Release 的 `latest` redirect 拿 tag（不调 API，避免限流）
3. 下载 `qatlasd-<os>-<arch>` 到目标目录
4. 下载 `checksums.txt` 验 SHA256
5. `chmod +x`
6. 打印 next-step 提示

**全 POSIX sh**（不依赖 bash），所以 Alpine / BusyBox / macOS sh 都能跑。

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
curl -fsSL https://quantum-atlas.ai/install-server.sh | sh -s -- --version v0.2.9

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
