# Provider 模板（冻结版 v1）

冻结日期：2026-07-24（2026-07-25 按 Remote MCP 优先与 providerkit 并入 core 修订；
补 Remote MCP 形态的 Definition 模板）

这套模板覆盖两种 Provider 形态，**Definition 模板按形态分成两份，其余文件共用**。
动手前先按`docs/blueprint.md` §8 做形态选型，**默认 Remote MCP**：

| 形态 | Definition 模板 | 还需要 | 现存实例 |
|---|---|---|---|
| Remote MCP（默认） | `definition.remote.go.preview` | validator、测试、Provider 文档 | github / gitlab / google_ads / linear / notion / stripe |
| Managed REST（降级） | `definition.managed.go.preview` | 上面全部，**再加**`client.go`+`managed.go` | datadog（onedrive 把 client 并进了`managed.go`） |

走 Remote MCP 的 Provider 只有`definition.go` + `validator.go`，**没有**`client.go`
和`managed.go`；复制模板时不要连带把这两个文件带过去。gmail 是混合形态：两条路的
文件都有，Definition 里同时存在`RemoteMCPBackend`和`ManagedBackend`的 Tool。

它固定的是文件职责、运行时边界和 review 门槛，不固定任何 Provider 的 API 语义。
所有 Go 模板都使用`.preview`后缀，包含故意未定义的`TODO_*`标识；复制到
`packages/connectors/<去掉下划线的 connector_type>/`（`one_drive`→`onedrive`）、
去掉`.preview`并完成全部 TODO 前不会进入 Go 编译。两个 Definition 模板都落成同一个
`definition.go`（`definition.remote.go.preview`→`definition.go`），形态后缀只存在于
模板目录。

复制后必须先把每个 Go 文件的`package TODO_PROVIDER_PACKAGE`统一替换为实际 Go
package 名（通常是去掉`connector_type`下划线后的目录名），再处理其余 TODO。

OAuth、动态 tenant origin、bot install、multipart/file transit 和异步任务不能靠
删 TODO 套用本模板；应先满足对应公共能力和外部审核门槛，再在本结构上增量设计。

## 文件职责

| 模板 | 复制后的文件 | 唯一职责 |
|---|---|---|
| `definition.remote.go.preview` | `definition.go` | **仅 Remote MCP**：静态 Definition，含`RemoteMCPServers`（endpoint／AuthBinding／Provenance）与`RemoteMCPBackend`的 Tool；InputSchema 参数直通 |
| `definition.managed.go.preview` | `definition.go` | **仅 Managed**：静态 Definition 与`ManagedBackend`的 Tool；封闭 InputSchema＋必填 OutputSchema |
| `client.go.preview` | `client.go` | **仅 Managed**：`providerkit` policy、凭证呈现和 Provider 失败映射 |
| `managed.go.preview` | `managed.go` | **仅 Managed**：参数到固定 API 操作的映射、严格成功响应归一化 |
| `validator.go.preview` | `validator.go` | 使用同一 guarded client 验证凭证并返回真实 profile/scope |
| `provider_test.go.preview` | 拆成 `definition_test.go`、`validator_test.go`，Managed 形态再加 `managed_test.go` | 无公网的合同、安全和回归测试 |
| `real_smoke_test.go.preview` | `real_smoke_test.go` | 默认跳过、显式启用的真实账号 smoke |
| `provider.md.preview` | `docs/providers/<connector_type>.md` | 来源、配置、限制、smoke 记录 |

`REVIEW_CHECKLIST.md`是 Provider PR 的冻结 review checklist。实现时复制一份到 PR
说明并逐项给出证据；仅勾选“有文件”不算完成。

## 标准实施顺序

1. **先做形态选型**：按`docs/blueprint.md` §8 判断走 Remote MCP 还是降级到
   Managed REST。默认 Remote MCP；只有命中 §8.1 的降级条件才降级到 Managed REST，届时才用
   本模板的`client.go`/`managed.go`。选型结论决定第 4 步复制哪几个文件。
2. 确认该 Provider 依赖的公共能力已经存在，且外部审核前置（OAuth app/global
   client、Marketplace、privileged scope 等）已经启动或不适用。
3. 以当前官方文档重新选择 3–8 个工作流，冻结 Tool ID、认证方式、API 版本和来源。
   Remote MCP 的 tool 名与 InputSchema 只有`mise run mcp-probe`拉到的
   `tools/list`是权威来源（§8.2）；参考实现只能作为 metadata 线索。
4. **按形态复制对应的模板文件**，替换所有文件的`TODO_PROVIDER_PACKAGE`，再先完成
   Definition；Registry 测试必须在写 handler 前通过。

   | 走哪条路 | 复制这些 | 不要复制 |
   |---|---|---|
   | Remote MCP | `definition.remote.go.preview`、`validator.go.preview`、`provider_test.go.preview`、`real_smoke_test.go.preview`、`provider.md.preview` | `client.go.preview`、`managed.go.preview`（没有 handler，`newHandlers`留空） |
   | Managed REST | 全套七个模板（`definition.managed.go.preview`＋上面四个＋`client.go.preview`＋`managed.go.preview`） | — |

   两条路都必须有 validator：Remote MCP 的 tool 执行不经过我们的 HTTP 层，凭证是否
   有效只有 validator 能提前判定。
5. 用`providerkit.Factory`实现 client、handler 和 validator（Remote MCP 只需
   validator）。不得引入`http.DefaultClient`、`http.DefaultTransport`、无界读取
   或用户可控 origin/path。
6. 先跑确定性 mock/testkit 测试；把 connector type、package、精确
   `Test<Provider>RealSmoke`和 cleanup mode 增行写进
   `scripts/provider-real-smoke-manifest.tsv`（runner 与 fake gate 测试共读这一
   份清单，`scripts/test-provider-real-smoke-gate.sh`里写死的 Provider 条数也要
   同步），再只通过该 commit-bound、non-SKIP gate 运行真实账号 smoke。
7. 在`packages/connectors/all.go`的`providerRegistrations`增加一个条目（Remote MCP
   形态留空`newHandlers`），并更新完整性测试、Provider 文档和根 README。
8. 只有`REVIEW_CHECKLIST.md`全部通过且真实 smoke 留有证据时，才能把 Provider 标成
   `production_supported`（状态词汇见`docs/blueprint.md` §10）。

## 不应再手写的能力

- canonical origin、DNS/IP、redirect、proxy、timeout、response cap 和 retry：
  使用`providerkit`；
- Bearer/API key/Basic 等凭证呈现：使用`providerkit` authorizer；
- validator 输入形状和安全错误：使用
  `packages/connectors/internal/credentialvalidator`；
- Tool 参数强制、失败构造、REST transport 和 real-smoke 门/清理循环：分别使用
  `internal/toolargs`、`internal/toolfail`、`internal/restkit`、`internal/smoketest`
  （`managed.go.preview`里的`validateToolCall`/`structuredResult`/`failureResult`
  是教学用的展开写法，实际实现应直接复用这些包）；
- schema/default、scope、risk、Session grant 和 output schema：由 Registry/Engine
  执行，Provider 仍须声明并测试；
- Handler、validator、scope matcher 与其他 adapter 的引用完整性：由 runtime
  bundle 启动校验。

模板不提供“通用代理”“任意 URL”“原始 request/response”工具，也不提供自动从
第三方 schema 推断 risk、scope 或官方来源的捷径。

## 给 Remote MCP tool 补 OutputSchema 的安全做法

**先说结论：Remote MCP tool 可以有输出契约，但只能从真实响应里长出来，不能靠猜。**

引擎侧已经支持：`callManaged`和`callRemote`收敛到同一个`finish()`，它无条件调用
`validateBackendResult(result, schemas.Output)`（`packages/service/exec/schema_pipeline.go`）
——声明了`OutputSchema`就校验，没声明就跳过。**backend 类型不影响这条路径，补
schema 不需要任何引擎改动。**目前 7 个 Provider 的全部 50 个 Remote tool（github 5 /
gitlab 14 / google_ads 2 / linear 12 / notion 10 / stripe 5，以及 gmail 混合形态里的
2 个 remote tool）**一个都没有声明**，是缺采样凭证的历史选择，不是架构限制；
`TestManagedToolSchemasAreClosed`也只对 Managed tool 强制 output schema 与封闭契约。

注意区分：`OutputSchema`是**校验**形状；`RemoteMCPBackend.OutputMapperKey`是
**重塑**形状，尚未实现，非空值在 backend 触达前被拒。所以这里写的 schema 必须描述
**厂商实际返回的形状**，不能描述我们希望它返回的形状。

### 为什么要补

没有`OutputSchema`时，厂商 MCP server 返回什么，Agent 就看到什么。厂商改了输出
形状——preview 期的 MCP server 经常改——**Agent 侧的契约会静默漂移，我们不会收到
任何信号**。声明之后，同一个漂移立刻变成一次显式的`invalid_response`失败：从不可
观测变成可观测。

### 为什么不能拍脑袋补

schema 写错一个字段，那个 tool 会在运行时**直接返回`FailureInvalidResponse`**——
把一个本来能用的 tool 写死，且失败信息不回显 Provider 原始 body，排查成本很高。
厂商文档普遍**不描述 MCP tool 的输出形状**，照着 REST API 文档反推同样不可靠：
MCP server 返回的往往是重新组织过的结构，不是 REST 响应体。

### 安全流程（四步，不可跳步）

1. **采样**：用真实账号调用该 tool 若干次，覆盖典型分支（空结果、单条、多条、
   带可选字段、分页有无）。样本从`real_smoke_test.go`产生，走
   `scripts/run-provider-real-smoke.sh`的 commit-bound gate；**样本里的账号数据不
   进仓库**，只把归纳出的形状写进 schema。
2. **归纳最小封闭 schema**：只声明所有样本里**都**出现的字段为`required`；其余
   写成可选。object 拒绝未声明属性、array 声明`items`、值类型显式（同
   `registry.LintClosedSchema`的规则）。**样本没证明的字段一律不写死。**
3. **先在`real_smoke_test`里验证**：把候选 schema 对全部样本跑一遍校验，确认零
   失败，再考虑扩大采样。这一步是把「写错 schema」的代价从生产挪到 smoke。
4. **最后才写进 Definition**，并在`docs/providers/<connector_type>.md`的 smoke
   evidence 段记录采样日期、样本数与覆盖的分支。

### 硬性规则

> **宁可暂时不声明`OutputSchema`，也不要声明一个猜的。**

不声明只是维持现状（没有输出契约、漂移不可观测）；声明错则是**主动制造一个运行时
故障**。缺采样凭证时，正确动作是把这条记进 provider 文档的待办，而不是先写一个
「看起来差不多」的 schema。
