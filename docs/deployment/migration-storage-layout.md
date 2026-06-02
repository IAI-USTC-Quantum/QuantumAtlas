# 存储路径迁移：把 wiki / raw / data / pb_data 搬出 git checkout

## 背景

历史上 `WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `pb_data` 都默认指向 git
checkout 内的 `wiki/` / `raw/` / `data/` / `pb_data/`。这有几个长期
痛点：

- `.gitignore` 必须维护一长串"ignore wiki/raw/data/pb_data/..."
  规则，新人很容易看花眼或忘掉某一项。
- 仓库内的 `raw/` 在某些机器上是 FUSE 挂载（rclone SMB → 团队网盘），
  `go ./...` / `find ./` 会触发 FUSE 拉云端 listing，10 分钟都不一定
  回来（参考 software skill `mount.md`）。
- 跟 [XDG Base Directory][xdg]、12-factor、FHS 都背道。
- 用户新 clone 不会自动知道这些目录不能 walk。

[xdg]: https://specifications.freedesktop.org/basedir-spec/latest/

## 新默认

| 字段 | 新默认 | 实现位置 |
|---|---|---|
| `WIKI_DIR` | `<.env 所在目录>/../QuantumAtlas-Wiki` | `internal/config/config.go::defaultWikiDir` |
| `RAW_DIR` | `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/raw` | `defaultXDGSubdir("raw")` |
| `DATA_DIR` | `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/data` | `defaultXDGSubdir("data")` |
| `PB_DATA_DIR` | `${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas/pb_data` | `defaultXDGSubdir("pb_data")` |
| `.env` | **保持 `<repo>/.env`** | 12-factor / dotenv 主流做法，未改 |

所有默认都是显式覆盖优先（QATLAS_* > 旧 alias > 内置默认），生产部署
**不**强制要求重新写 `.env`：什么都不写就走 XDG 默认。`pb_data` 的位置
完全由 `QATLAS_PB_DATA_DIR` 控制；server 启动时
`cmd/qatlasd/main.go::injectPBDataDirFlag` 会自动把它注入 PocketBase
（在 `os.Args` 里补 `--dir=$QATLAS_PB_DATA_DIR`），所以 systemd unit /
启动脚本里**不需要**也**不应该**再硬写 `--dir=`，整套配置走 `.env`
统一管。

## 个人开发机：把仓库内的 wiki/raw/data/pb_data 搬出来

假设你的仓库在 `<APP_HOME>`（举例 `~/QuantumAtlas/`），旧的状态目录都在
那里面，目标是把它们挪到 `$XDG_DATA_HOME/quantum-atlas/` 下、让 git
checkout 重新干净。

```bash
APP_HOME=~/QuantumAtlas                    # 改成你自己的 checkout 路径

# 0. 停掉本地 server，避免边搬边写
#    （根据你的运行方式，二选一）
systemctl --user stop qatlas.service                # systemd user
pkill -f 'qatlasd serve' || true                # 手起 binary

# 1. 准备 XDG 目标目录
xdg_root="${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas"
mkdir -p "$xdg_root"

cd "$APP_HOME"

# 2. 搬迁前快照（cp -a 是最安全的兜底，跨 fs 也 OK）
for d in wiki raw data pb_data; do
    [ -d "$d" ] || continue
    cp -a "$d" "$d.bak-$(date +%s)"
done

# 3. 实际搬迁
#    - mv 在同一 filesystem 内是原子的
#    - 跨 filesystem 时退化成 cp + rm
for d in wiki raw data pb_data; do
    [ -d "$d" ] || continue
    mv "$d" "$xdg_root/$d"
done

# 4. wiki 特殊处理：默认指 ../QuantumAtlas-Wiki（兄弟 checkout），
#    不是 $xdg_root/wiki。如果你打算把 wiki 当独立 git repo 维护：
mv "$xdg_root/wiki" ../QuantumAtlas-Wiki
cd ../QuantumAtlas-Wiki && git init && git add -A && git commit -m "import from QuantumAtlas/wiki/"
cd -

# 5. 启动验证
~/.local/bin/qatlasd serve --http=127.0.0.1:4200 &
sleep 2
curl -s http://127.0.0.1:4200/health                # {"status":"healthy",...}
curl -s http://127.0.0.1:4200/api/stats | jq .      # total_pages 应非 0
kill %1
```

## 生产迁移：从旧布局切到新代码

无论旧布局是什么样的（in-repo 子目录、共享盘挂载、独立分区都可能），
新代码的契约就一条：**.env 显式覆盖永远赢默认**。

按下面三步切换：

1. **决定每个路径要不要保留**。如果当前生产已经把 `RAW_DIR` /
   `DATA_DIR` / `PB_DATA_DIR` 钉在某个固定位置（共享盘挂载、独立分区
   等）且想保留，就在 `.env` 里**显式写出来**——这样未来 `unset`
   或更新代码默认值都不会让 server 偷偷漂走。如果当前位置就是 XDG
   默认或愿意接受默认，留空即可。

2. **删 systemd unit / 启动脚本里的 `--dir=`**。新代码会自动从
   `.env` 的 `QATLAS_PB_DATA_DIR` 注入；unit 里再硬写 `--dir=` 会绕开
   .env，让"`.env` 是唯一配置来源"的承诺破功。如果当前路径不在
   `.env` 里，先 `echo QATLAS_PB_DATA_DIR=... >> .env` 再删 unit 里的
   `--dir=`，**顺序不能反**——反了那一拍重启会让 pb_data 漂到新位置、
   登录态/PAT 记录"消失"（其实只是不在新库里，老库还在旧位置）。

3. **`daemon-reload` + `restart` + 验证**：

   ```bash
   systemctl --user daemon-reload && systemctl --user restart qatlas.service
   # 或 system unit：sudo systemctl daemon-reload && sudo systemctl restart qatlas

   curl -sf http://127.0.0.1:4200/health
   journalctl --user -u qatlas -n 50 | grep -E 'loaded \.env|pb_data|listening'
   # 期望看到：
   #   loaded .env path=<APP_HOME>/.env
   #   ... QATLAS_PB_DATA_DIR resolved to <你期望的路径>
   #   listening on 0.0.0.0:4200
   ```

   浏览器再访问 `https://<HOST>/pat` 看是否还能列出之前的 PAT 列表
   ——空了说明 pb_data 路径漂了，回到 step 1 检查 .env 的
   `QATLAS_PB_DATA_DIR` 是否指向真实有数据的那个目录。

## 旧用户：保持原有 in-repo 路径（不迁移）

如果你坚持把 wiki/raw/data/pb_data 留在 git checkout 内（不推荐，但
可以），就在 `.env` 里**显式钉住**：

```env
QATLAS_WIKI_DIR=./wiki
QATLAS_RAW_DIR=./raw
QATLAS_DATA_DIR=./data
QATLAS_PB_DATA_DIR=./pb_data
```

`./wiki` 等相对路径会以 `.env` 所在目录为 anchor 解析（即 git checkout
根）。但注意：

- `.gitignore` 已经把 `/wiki/`、`/raw/`、`/data/`、`pb_data/` 的 ignore
  规则**删了**——这些目录现在会出现在 `git status` 的 untracked 列表里。
  你可以在自己 `~/.config/git/ignore`（global） 或 `.git/info/exclude`
  （local-only，不进 commit）里加回去，不要再改 repo 内的 `.gitignore`。
- 仓库里再不维护这些路径的特殊处理，未来 .gitignore / docs 都假设
  XDG 默认。

## 验证清单

迁移完之后跑一遍：

```bash
# Go 侧
pixi run vet                                                # 0 warning
pixi run test-go                                            # config tests pass
~/.local/bin/qatlasd serve --http=127.0.0.1:4200 &
sleep 2
curl -s http://127.0.0.1:4200/health
curl -s http://127.0.0.1:4200/api/stats | jq .total_pages   # 应 > 0
curl -s http://127.0.0.1:4200/api/server/info | jq .        # engine: go+pocketbase
kill %1

# Python client（如果你跑 qatlas CLI）
qatlas wiki list | head                                     # 应正常列页面
qatlas wiki show <some-page-id>                             # 应能取到
```

如果哪一步报"directory not found"，回到 `.env` 检查覆盖路径是否正确——
默认值 ≠ 错误值，server 创建子目录是 lazy 的，第一次写时才 mkdir，
所以"目录不存在"本身不是 bug，写一次就有了。
