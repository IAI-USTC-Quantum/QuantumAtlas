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
**不**强制要求重新写 `.env`：什么都不写就走 XDG 默认。`pb_data` 在
PocketBase 那侧通过 `--dir=` 命令行参数控制；server 启动时
`cmd/server/main.go::injectPBDataDirFlag` 检查 os.Args，没带就自动
补 `--dir=$QATLAS_PB_DATA_DIR`。

## 个人开发机：把仓库内的 wiki/raw/data/pb_data 搬出来

下面假设你的仓库在 `~/TiMidlY-projects/QuantumAtlas/`，旧的状态目录
都在那里面，目标是把它们挪到 `~/.local/share/quantum-atlas/` 下、
让 git checkout 重新干净。

```bash
# 0. 停掉本地 server，避免边搬边写
#    （根据你的运行方式，二选一）
systemctl --user stop qatlas.service                # systemd user
pkill -f 'quantumatlas serve' || true                # 手起 binary

# 1. 准备 XDG 目标目录
xdg_root="${XDG_DATA_HOME:-$HOME/.local/share}/quantum-atlas"
mkdir -p "$xdg_root"

cd ~/TiMidlY-projects/QuantumAtlas

# 2. 搬迁前快照（rsync --link-dest 会做硬链接快照，几乎不耗空间，
#    但若任一目录在 FUSE / CIFS 上则跨 fs，硬链接会失败 → 退化为
#    普通 cp。下面的 cp -a 是最安全的兜底）
for d in wiki raw data pb_data; do
    [ -d "$d" ] || continue
    cp -a "$d" "$d.bak-$(date +%s)"
done

# 3. 实际搬迁
#    - mv 在同一 filesystem 内是原子的
#    - 跨 filesystem 时退化成 cp + rm，看下面注意事项
for d in wiki raw data pb_data; do
    [ -d "$d" ] || continue
    mv "$d" "$xdg_root/${d#}"                       # raw -> $xdg_root/raw 等
done

# 4. wiki 特殊处理：默认指 ../QuantumAtlas-Wiki（兄弟 checkout），
#    不是 $xdg_root/wiki。如果你打算把 wiki 当独立 git repo 维护：
mv "$xdg_root/wiki" ../QuantumAtlas-Wiki
cd ../QuantumAtlas-Wiki && git init && git add -A && git commit -m "import from QuantumAtlas/wiki/"
cd -

# 5. 启动验证
~/.local/bin/quantumatlas serve --http=127.0.0.1:4200 &
sleep 2
curl -s http://127.0.0.1:4200/health                # {"status":"healthy",...}
curl -s http://127.0.0.1:4200/api/stats | jq .      # total_pages 应非 0
kill %1
```

### 跨 filesystem 注意（CIFS / FUSE）

如果 `raw/` 是 rclone SMB 挂载（指向团队网盘），`mv raw $xdg_root/raw`
会变成"复制几十 GB 数据到本地 → 删云端原件"，几乎一定不是你想要的。
正确做法：**保留挂载点，只在 `.env` 里把 `QATLAS_RAW_DIR` 指过去**：

```bash
# 不要 mv，改成在 .env 显式覆盖默认：
echo 'QATLAS_RAW_DIR=/mnt/team/QuantumAtlas/raw' >> .env
# 或者
echo 'QATLAS_RAW_DIR=~/TiMidlY-projects/QuantumAtlas/raw' >> .env
```

显式覆盖永远赢默认。.env 里有这行，server 就忽略 XDG 默认，直接用挂载点。

## 1810 prod 迁移（CIFS 挂载场景）

1810 后端的 `/mnt/team/QuantumAtlas/{raw,data}` 是团队网盘上的 CIFS
挂载，**不能动 mount 配置**——只能改 server 怎么找它。所以 prod 不
需要"搬"任何数据，只需要在 `/home/timidly/QuantumAtlas/.env` 里显式
钉住这两个路径（其实当前就已经这么配，新代码不会改变这个行为，但
为了防御新人 `unset RAW_DIR` 后 server 默写 XDG 默认丢人，必须在
`.env` 里钉着）：

```env
# /home/timidly/QuantumAtlas/.env 必须显式有：
QATLAS_RAW_DIR=/mnt/team/QuantumAtlas/raw
QATLAS_DATA_DIR=/mnt/team/QuantumAtlas/data
```

`pb_data` 之前在 `/home/timidly/QuantumAtlas-go/pb_data/`，跟仓库分开。
新代码不再要求 systemd unit 显式带 `--dir=`，但如果你想保持现有路径
（不挪到 XDG 默认），有两种等价做法，**选其中一种**：

A. systemd unit 显式带（不动 .env）：
```ini
ExecStart=/home/timidly/.local/bin/quantumatlas serve --http=0.0.0.0:4200 --dir=/home/timidly/QuantumAtlas-go/pb_data
```

B. `.env` 写 `QATLAS_PB_DATA_DIR`（不动 unit）：
```env
QATLAS_PB_DATA_DIR=/home/timidly/QuantumAtlas-go/pb_data
```

两条等效。**B 更符合 12-factor 配置在 .env 的原则**，迁过去后 unit
就跟模板版本完全一致，减少特例。

### 1810 一次性迁移步骤（B 路线）

```bash
# 假设你 ssh 进 1810 之后：
ssh 1810

# 1. 在 .env 里显式钉住所有不走默认的路径
cd ~/QuantumAtlas
grep -q '^QATLAS_RAW_DIR=' .env || \
    echo 'QATLAS_RAW_DIR=/mnt/team/QuantumAtlas/raw' >> .env
grep -q '^QATLAS_DATA_DIR=' .env || \
    echo 'QATLAS_DATA_DIR=/mnt/team/QuantumAtlas/data' >> .env
grep -q '^QATLAS_PB_DATA_DIR=' .env || \
    echo 'QATLAS_PB_DATA_DIR=/home/timidly/QuantumAtlas-go/pb_data' >> .env

# 2. 改 systemd user unit，去掉硬写的 --dir=
nano ~/.config/systemd/user/qatlas.service
# 把
#   ExecStart=...quantumatlas serve --http=0.0.0.0:4200 --dir=/home/timidly/QuantumAtlas-go/pb_data
# 改成
#   ExecStart=...quantumatlas serve --http=0.0.0.0:4200

# 3. reload & restart
systemctl --user daemon-reload
systemctl --user restart qatlas

# 4. 验证
curl -s http://127.0.0.1:4200/health
journalctl --user -u qatlas -n 50 | grep -E 'loaded \.env|pb_data|listening'
# 期望看到：
#   loaded .env path=/home/timidly/QuantumAtlas/.env
#   ... QATLAS_PB_DATA_DIR resolved to /home/timidly/QuantumAtlas-go/pb_data
#   listening on 0.0.0.0:4200
```

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
~/.local/bin/quantumatlas serve --http=127.0.0.1:4200 &
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
