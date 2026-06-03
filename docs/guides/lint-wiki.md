# Lint 与校验

`qatlas wiki lint` 检查 Wiki 仓库里所有页面的 frontmatter、链接、孤儿状态、过时等问题。CI 应该跑这个。

## 用法

```bash
# 全部检查，紧凑输出
qatlas wiki lint

# 详细输出（带建议 + 文件路径）
qatlas wiki lint --verbose

# 尝试自动修复（保守，只动安全的项）
qatlas wiki lint --fix
```

## 错误码 W001–W008

| 代码 | 严重度 | 检查 | 怎么修 |
|---|---|---|---|
| **W001** | ERROR | 必填 frontmatter 字段缺失 | 加 `id` / `title` / `type` |
| **W002** | ERROR | frontmatter 字段值非法 | `type` 必须是 `concept/entity/source/comparison`（**新页面统一用 `concept`**，entity/comparison 为 legacy 兼容值）；`status` 必须是 `draft/review/published` |
| **W003** | INFO | 孤儿页面（无任何入链）| 找一个相关页面加 `[[page-id]]`；或确认它确实独立 |
| **W004** | WARNING | 断链：`[[xxx]]` 指向不存在的页面 | 改 id 或建对应页面 |
| **W005** | INFO | 概念引用没有对应 Concept 页面 | 考虑加 Concept 页面解释 |
| **W006** | ERROR | 页面 ID 重复 | 改其中一个 id（必须全 repo 唯一）|
| **W007** | INFO | 页面 30 天没更新 | 复审或更新 `updated_at` |
| **W008** | WARNING | 词条页面没有 tags | 加 `tags: [...]` 用于过滤 / 图谱属性 |

## 典型修复模式

=== "W001 / W002 frontmatter 问题"

    ```diff
    -id: prim_foo
    +id: prim-foo
    -title:
    +title: "Foo's Concept"
     type: concept
    +category: primitive
    ```

    !!! note
        id 用 kebab-case，前缀按类型（`prim-` / `algo-` / `paper-arxiv-` / `comp-` / `person-` / `concept-`）。文件名要跟 id 一致（加 `.md`）。

=== "W004 断链"

    `[[prim-qft]]` 指向不存在的页面：

    1. 是不是手抖？`qatlas wiki search "qft"` 找正确 id
    2. 是不是页面没建？`qatlas wiki create prim-qft --title "Quantum Fourier Transform" --type concept --category primitive`

=== "W006 重复 id"

    两个文件用了同一个 id：

    ```
    wiki/entities/primitives/prim-foo.md       id: prim-foo
    wiki/concepts/concept-foo.md               id: prim-foo   ← 错
    ```

    全 repo 改另一个，且文件名同步：

    ```bash
    cd ~/projects/QuantumAtlas-Wiki
    git mv wiki/concepts/concept-foo.md wiki/concepts/concept-foo-thing.md
    sed -i 's/^id: prim-foo$/id: concept-foo-thing/' wiki/concepts/concept-foo-thing.md
    grep -r 'prim-foo' wiki/   # 看还有没有别处引用旧 id
    ```

=== "W008 词条没 tags"

    ```diff
     ---
     id: prim-grover
     title: Grover's Search
     type: concept
     category: primitive
    +tags: [search, oracle, amplification]
    ```

    tag 用 kebab-case，3–6 个为宜。会变成 Neo4j 节点属性。

## 在 CI 跑 lint

在 [QuantumAtlas-Wiki](https://github.com/IAI-USTC-Quantum/QuantumAtlas-Wiki) 仓库的 `.github/workflows/lint.yml`：

```yaml
name: Wiki Lint
on: [push, pull_request]
jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: astral-sh/setup-uv@v3
      - run: uv tool install quantum-atlas
      - run: QATLAS_WIKI_DIR=. qatlas wiki lint
```

W001 / W002 / W006 是 ERROR 级别，会让 lint 非 0 退出阻塞合并。

## 已知 limitations

!!! info "Go server 还没有 lint"
    Server 端 `/api/lint` endpoint 当前返回固定空结果（占位）—— 完整 lint 仍在 Python 这边跑。CI 应该 `uv tool install quantum-atlas` 然后跑 `qatlas wiki lint`，不要依赖 server 端 endpoint。

## 下一步

- 完整 schema：[Wiki schema 参考](../reference/wiki-schema.md)
- 怎么写页面：[写 Wiki 页面](write-wiki-pages.md)
