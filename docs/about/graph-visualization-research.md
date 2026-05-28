# 图谱可视化前端调研（待实现）

> **状态**：待实现 · 仅为工具选型调研记录，未进入开发计划
>
> **背景**：当前 `web/src/routes/graph.index.tsx` 中的 explorer 区块是手写的静态占位（三个圆 + 两条线），仅用于布局演示。后端 `/api/graph/stats`、`/api/graph/query`、`/api/graph/schema`（Neo4j 驱动）已就绪，缺一个能渲染真实节点边数据的前端图谱库。本页记录候选工具的对比与推荐，供后续真正开发图谱视图时参考。

## 兼容性结论

**图数据库（Neo4j）和前端图谱库不在同一层，原生兼容**：Neo4j 在后端通过 Cypher 查询返回 `{nodes, relationships}`，FastAPI 序列化成 JSON，前端图谱库只需要把 JSON 喂给布局算法和渲染器。中间没有协议绑定，任何前端图谱库都能用。

下文比较的关键不是"能不能配 Neo4j"，而是**节点规模、交互需求、生态成熟度、与 React 19 的集成体验**。

## 候选库对比

| 库 | 渲染 | 规模上限 | React 集成 | 布局算法 | Neo4j 集成案例 | 适用场景 |
|---|---|---|---|---|---|---|
| **Cytoscape.js** | Canvas | ~5k 节点流畅，>5k 需要降采样 | `react-cytoscapejs`（社区维护） | 内置 cose / cola / dagre / fcose 等 10+ | Neo4j 官方 sandbox、Memgraph Lab 内置 | 通用知识图谱、节点-关系探索（**首选**） |
| **Sigma.js** | WebGL | 数万节点 | 有 wrapper 但偏底层 | 配合 graphology 生态（forceAtlas2 等） | Linkurious 商用版基于此 | 大规模图、力导向、社区检测 |
| **react-force-graph** | Three.js / Canvas | ~10k 节点 | React 原生 | 内置 force / radial / hierarchical | 较少 | 力导向 3D / 2D，演示效果好 |
| **@xyflow/react** (React Flow v12) | DOM / SVG | <500 节点最佳 | React 原生（最佳） | 内置（侧重布局编辑而非物理） | 几乎没有 | **节点编辑器 / 流程图 / 电路设计器**，不适合知识图谱 |
| **vis-network** | Canvas | ~3k 节点 | 第三方 wrapper（维护一般） | 内置 hierarchical / physics | NebulaGraph Studio 用它 | 通用，但生态比 Cytoscape 差 |
| **D3-force** | SVG / Canvas | <1k 节点 | 手动集成（自由度最高） | 自己写 force simulation | — | 完全定制，开发成本高 |
| **G6**（AntV） | Canvas / WebGL | 大规模可（v5 改进显著） | `@antv/g6-react-node` | 内置 + 自定义 | 国内项目较多 | 中文生态友好，文档较繁但功能全 |

## 推荐方案

按 QuantumAtlas 当前阶段的图谱规模（论文 / primitive / algorithm 节点估计低于一千），优先级从高到低：

### 1. 主方案：Cytoscape.js + react-cytoscapejs（首选）

**适用场景**：`/graph` 知识图谱总览、`/graph/node/$` 节点邻居探索

**理由**：
- Neo4j → Cytoscape 是最成熟的搭配，社区示例直接可抄
- `fcose` 布局对中等规模（几百到几千节点）效果最好
- Canvas 渲染在 1k 量级流畅，浏览器内存占用低
- API 稳定，TypeScript 类型完整
- 节点样式、边样式、tooltip、点击交互都原生支持

**安装**：
```bash
npm i cytoscape cytoscape-fcose react-cytoscapejs
npm i -D @types/cytoscape
```

**最小数据流**：
```
Cypher (后端) -> /api/graph/query -> JSON ({nodes:[{data:{id,label,type}}], edges:[{data:{id,source,target,type}}]})
                                  -> <CytoscapeComponent elements={...} layout={{name:'fcose'}} stylesheet={...}/>
```

**后端要补的端点**（已存在 `POST /api/graph/query`，确认一下返回格式贴近 Cytoscape elements 即可）：
- `GET /api/graph/neighbors/{node_id}?depth=1` —— 取一阶邻居用于 explorer 点击展开

### 2. 升级路径：Sigma.js（节点突破 5k 时）

**触发条件**：知识库扩张后 `/api/graph/stats` 报告节点 > 5k 或 relationships > 20k

**迁移成本**：中等。Cytoscape 和 Sigma 的数据结构都是 `{nodes, edges}`，但样式 API 不同。可保留 `lib/graph/transform.ts` 这层 adapter，下游切换库不改业务代码。

### 3. 平行方向：@xyflow/react（电路设计器，独立路由）

**触发条件**：用户提出过"designer / 电路图编辑器"需求，那是另一类 UI——节点是用户自己拖出来的，不是从 Neo4j 查的。

**做法**：另开 `/designer` 路由，用 `@xyflow/react`，**和 Neo4j 解耦**。两套图谱并存不冲突，因为它们的数据源、交互模型完全不同（一个是 read-only 探索，一个是 read-write 编辑）。

## 不推荐

- **vis-network**：能用，但 Cytoscape 在所有维度都更强，没理由选它。
- **直接用 D3**：除非有特殊定制需求，自己造力导向轮子是反复出现的项目坑。
- **Neovis.js**（Neo4j 官方曾推荐过的 vis-network 包装）：基本停止维护，文档过时。
- **Linkurious / yWorks 等商用方案**：闭源、收费、不必要。

## 决策时再确认的几个问题

实际开工时这些点要先和数据对齐，避免选错库：

- 知识图谱当前 / 三个月内的节点和边规模（看 `/api/graph/stats`）
- 是否需要在浏览器里编辑图（增删节点 / 改属性），还是只读探索
- 是否需要时间维度（节点的 `created_at`、`updated_at` 演化）
- 是否需要分组着色 / 社区检测 / 路径高亮等高级交互
- 是否要在节点详情页（`/graph/node/$`）画一个局部子图

## 参考链接

- Cytoscape.js: <https://js.cytoscape.org/>
- react-cytoscapejs: <https://github.com/plotly/react-cytoscapejs>
- Cytoscape fcose layout: <https://github.com/iVis-at-Bilkent/cytoscape.js-fcose>
- Neo4j Cytoscape integration tutorial: <https://neo4j.com/developer/cytoscape/>
- Sigma.js + graphology: <https://www.sigmajs.org/>
- react-force-graph: <https://github.com/vasturiano/react-force-graph>
- @xyflow/react (原 React Flow): <https://reactflow.dev/>
- AntV G6: <https://g6.antv.antgroup.com/>
