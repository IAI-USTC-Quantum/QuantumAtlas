# 存储路径迁移

> 历史文档：0.x 阶段默认存储路径变更的记录，给操作员排查"旧 install 找不到 pb_data"用。新 install 不需要看。

## 关键变更

| 版本 | 变更 |
|---|---|
| v0.5.x → v0.6.x | `WIKI_DIR` / `RAW_DIR` / `DATA_DIR` / `pb_data` 默认从 git checkout 内移到 XDG 数据目录；git checkout 不再放状态目录 |
| v0.17.0 | server XDG 子目录从 `quantum-atlas/` 改为 `qatlasd/`（跟 binary 名一致）|

## 当前默认

| 字段 | 默认 |
|---|---|
| `WIKI_DIR` | `<.env 所在目录>/../QuantumAtlas-Wiki` |
| `RAW_DIR` / `DATA_DIR` / `PB_DATA_DIR` | `${XDG_DATA_HOME:-$HOME/.local/share}/qatlasd/{raw,data,pb_data}` |

Client 路径由 platformdirs 选定（Linux `~/.config/qatlas/`、macOS `~/Library/Application Support/qatlas/`、Windows `%APPDATA%\qatlas\`），与 server 路径无关。

## 迁移要点

- **显式覆盖永远赢默认**：想钉住某个固定路径就在 `.env` 写 `QATLAS_RAW_DIR` / `QATLAS_DATA_DIR` / `QATLAS_PB_DATA_DIR`；留空走 XDG 默认。
- **systemd unit 不要硬写 `--dir=`**：server 启动时从 `QATLAS_PB_DATA_DIR` 自动注入 PocketBase 的 `--dir=`；unit 里再写会绕开 `.env`。
- **从旧 XDG 路径升级**：停服 → `mv ~/.local/share/quantum-atlas ~/.local/share/qatlasd` → 升 binary → 重启。不想搬就在 `.env` 显式指回旧路径。
- 忘了迁移 → server 会在新位置建一份空 pb_data（OAuth/PAT 看似"消失"），老数据还在旧目录、server 看不到；停服把数据搬过去即可。

迁移后验证：`curl http://127.0.0.1:4200/api/health` 正常，`/api/stats` 的 `total_pages > 0`。
