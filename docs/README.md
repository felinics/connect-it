# connect-it 文档索引

> 这是 `docs/` 的入口，只做导航：说清每份文档的职责边界、按任务指路、以及冲突时听谁的。
> 具体内容一律留在被指向的文档里，本文不复述。
> 仓库根 [`README.md`](../README.md) 是另一类入口——面向使用者的快速开始、环境变量、
> mise 任务与接入指南；`docs/` 面向的是要改这个服务的人。

## 各份文档的职责边界与现状

「最后核实」是该文档正文里自己声明的核实日期；没有声明的文档不该存在，发现了就补上。

| 文档 | 行数 | 最后核实 | 是什么 | 不是什么 |
|---|---:|---|---|---|
| `README.md`（本文） | 87 | 2026-07-25 | **索引与术语表**：职责边界、按任务导航、冲突裁决顺序 | 不是任何事实的依据（见文末「冲突时以谁为准」第 4 条） |
| [`blueprint.md`](blueprint.md) | 512 | 2026-07-25 | **架构层面的唯一事实源**：定位、模块划分、领域模型、关键流程、安全模型、交付现状与债务 | 不是操作手册，不记录单个 Provider 的配置细节 |
| [`provider-mcp-decision-table.md`](provider-mcp-decision-table.md) | 848 | 2026-07-24 调研 / 2026-07-25 仓库现状复核 | **调研数据**：139 个常见 SaaS 的 MCP 可用性逐条判定，喂给 blueprint §8 的选型；§5.5 另外跟踪仓库内 7 个 Remote Provider 的契约校准状态 | 不是结论本身；与 blueprint 冲突时以 blueprint 为准 |
| [`provider-egress-security.md`](provider-egress-security.md) | 81 | 2026-07-25 | **出网边界规格**：`core/providerkit` 受控 transport 的默认拒绝规则与两条子路径 | 不覆盖入站（入站安全见 blueprint §6） |
| [`providers/<type>.md`](providers/) | 9 份，177–296 行 | 每份 Source 段各自声明（2026-07-23 ～ 07-25） | **每个 Provider 的逐个事实源**：来源、配置步骤、限制、真实账号 smoke 记录 | 不是发布状态机（状态词汇见 blueprint §10） |
| [`runbooks/fresh-database-bootstrap.md`](runbooks/fresh-database-bootstrap.md) | 38 | 2026-07-25 | **运维手册**：带外执行的一次性/周期性操作 | 目前只有一份（空库初始化），不是完整运维体系 |
| [`templates/provider/`](templates/provider/) | [`README.md`](templates/provider/README.md) 149 + [`REVIEW_CHECKLIST.md`](templates/provider/REVIEW_CHECKLIST.md) 159 + 8 个 `.preview` | 2026-07-24 冻结 / 2026-07-25 修订 | **新增 Provider 的脚手架**：`.preview` 文件模板 + `REVIEW_CHECKLIST.md` review 门槛 | 不固定任何 Provider 的 API 语义 |

## 术语：一个概念只有一个写法

同一件事在不同文档里换名字，是这套文档最容易累积的债。以下写法是唯一的：

| 规范写法 | 指什么 | 不要再写成 |
|---|---|---|
| **Remote MCP** | 把 `tools/call` 转发到厂商托管 MCP server 的 Provider 形态 | 远程 MCP、remote MCP |
| **Managed REST** | 我们自己写 HTTP client、请求构造与输出投影的 Provider 形态 | 手写 REST、Managed 型、托管 REST |
| **混合** | 同一个 Provider 两种 backend 都有（目前只有 `gmail`：2 Remote + 2 Managed） | 半 Remote、部分 MCP |

例外只有一种：**引用厂商官方文档原文或代码注释里的英文原话时照抄，不改**
（例如决策表里 Azure 那条引用微软自己写的 `"remote MCP"`）。领域词（Connection、
MCP session、Tool、Definition）以 [blueprint §3](blueprint.md#3-核心领域模型) 为准。

## 按任务导航

**我要新增一个 Provider**（唯一有固定顺序的任务，别跳步）

1. [`blueprint.md` §8](blueprint.md#8-provider-形态选型remote-mcp-优先)：先做形态选型。
   默认 Remote MCP；§8.1 的六条降级条件命中任意一条才写 Managed REST。
2. [`provider-mcp-decision-table.md`](provider-mcp-decision-table.md)：在表里找到该 SaaS，
   看它有没有可用的官方 Remote MCP。表里没有的，按 §8.1 的四条标准自己核。
3. Remote MCP 形态还要定 tool 定义的来源——[`§8.2`](blueprint.md#82-tool-定义从哪来三条路成本递增)
   给了三条路。**唯一权威的一条**是拿一次性 token 跑
   `mise run mcp-probe -- -endpoint <url> -token <token>`（只读，只问 `tools/list`，
   与生产同一个 `mcpclient`）。猜出来的 schema 比没有 schema 更糟。
4. [`blueprint.md` §9](blueprint.md#9-新增-provider-清单第-1457-条由契约测试强制其余由-review_checklist-把关)：
   逐条清单，其中 4 条由契约测试强制。
5. [`templates/provider/`](templates/provider/)：复制模板（Definition 按形态分两份），
   最后过 [`REVIEW_CHECKLIST.md`](templates/provider/REVIEW_CHECKLIST.md)。

**我要理解整体架构** → [`blueprint.md`](blueprint.md)。按需跳：§1 定位与「明确不做」、
§2 模块与依赖方向、§3 领域模型（connection / MCP session / 三种令牌）、§4 关键流程
（OAuth、刷新、exec 管线、`/mcp`）、§6 安全模型、§7 构建与生成链。

**我要知道某个 Provider 怎么配、有什么限制、验没验过** →
[`providers/<type>.md`](providers/)（`github` / `gitlab` / `gmail` / `one_drive` /
`google_ads` / `datadog` / `linear` / `notion` / `stripe`，文件名带下划线）。

**我要知道出网是怎么管的** → [`provider-egress-security.md`](provider-egress-security.md)。
新增出站路径必须由组合根注入的 `providerkit.Factory` 构造，不能另起 client。

**我要初始化数据库** → [`runbooks/fresh-database-bootstrap.md`](runbooks/fresh-database-bootstrap.md)。
生产只支持空库，没有 in-place upgrade；开发/CI 的 bootstrap 脚本不是生产接口。

**我要改路由 / API 形状** → [`blueprint.md` §7.1](blueprint.md#71-生成链改路由后必跑) 的生成链
（`mise run swagger` → `mise run sdk`；改 SQL 则 `mise run sqlc`）。生成物不要手改。

**我要部署 / 配环境变量** → [`blueprint.md` §12](blueprint.md#12-环境变量权威全表server-二进制实际读取)
是 server 二进制实际读取的权威全表（根 `README.md` 的那份不全，见 §11 债务表）。

**我要知道现在还缺什么** → [`blueprint.md` §10](blueprint.md#10-现状与路线图2026-07-25-口径)（现状）
与 [`§11`](blueprint.md#11-已知缺口与债务蓝图如实记录)（债务）。两条当下最要紧的：
**7 个** Remote Provider 的 50 个 remote tool，InputSchema 全部是推导出来的近似值、
无一经 `mcp-probe` 校准（不止 notion/stripe/linear/gitlab 那四个，逐项状态见
[决策表 §5.5](provider-mcp-decision-table.md#55-remote-mcp-契约校准状态跟踪)）；
9 个 Provider 的真实账号 smoke **一个都没有**。

## 冲突时以谁为准

1. **代码和测试** —— 任何行为细节的最终裁决。文档描述与实现不符，是文档的 bug。
2. **`blueprint.md`** —— 架构层面（模块边界、领域模型、流程、安全模型）的唯一事实源。
   决策表、模板、Provider 文档与它冲突时，以它为准。
3. **`providers/<type>.md`** —— 单个 Provider 的来源、限制与 smoke 证据，以它为准；
   blueprint §10 只给汇总口径，不承载逐个细节。
4. 本文只是索引，**不作为任何事实的依据**。发现它和上面三条不一致，改本文。
