# Provider PR review checklist（v1）

> 冻结 v1，最后核实 2026-07-25。

标注 **[Managed]** 的检查项只适用于 Managed REST 形态，标注 **[Remote]** 的只适用于
Remote MCP 形态；其余对两种形态都必须满足。形态选型本身见`docs/blueprint.md` §8。

## 排期与声明

- [ ] 形态选型有结论：默认 Remote MCP；降级到 Managed REST 时逐条对上
      `docs/blueprint.md` §8.1 的降级条件，并把命中的条件写进 Provider 文档。
- [ ] 该 Provider 依赖的公共能力已经存在，所有不可并行前置已完成。
- [ ] Marketplace、OAuth app/global client、privileged scope/intent、partner 或
      security review 等外部 gate 已批准并留存非敏感证据。
- [ ] 未通过本清单和真实 smoke 前，Catalog、README 和发布说明均未写成 supported
      （状态词汇见`docs/blueprint.md` §10）。

## Definition 与来源

- [ ] `connector_type`、目录名（去掉下划线）、AuthMethod key、Tool ID 稳定且一致；
      Managed Tool 的 HandlerKey 与 Remote Tool 的 `ServerKey`/`RemoteToolName`
      与 Definition 一一对应。
- [ ] 所有模板文件的`TODO_PROVIDER_PACKAGE`已替换为同一个实际 package 名，仓库中
      不残留任何`TODO_*`标识。
- [ ] **[Remote]** provider 目录里没有`client.go`/`managed.go`，`all.go`的
      `newHandlers`留空，Definition 里没有残留`ManagedBackend`的 Tool。混合形态
      （如 gmail）例外，但必须在 Provider 文档写明哪个 tool 为什么走 Managed。
- [ ] `docs/providers/<connector_type>.md`的 Source 段记录本次实际审阅的当前官方
      文档、API 版本和 review date。
- [ ] 同一 Source 段为每个参考实现记录仓库 URL、完整 40 位 commit、实际查看的路径
      和许可证；第三方材料不是行为事实源。注意
      `TestRegisteredProviderDocumentation`当前**无条件**要求 Source 段出现
      `https://github.com/oomol-lab/open-connector`、`Apache-2.0`、`src/providers/`、
      一个 40 位 SHA、一个 ISO 日期和 “official” 字样，缺任何一项都不过测试。
- [ ] 只选择 3–8 个高频工作流；没有通用 proxy、任意 URL/path/header/body 或原始
      Provider response 工具。
- [ ] **[Managed]** 每个 input schema 顶层为 closed object，包含字符串/数组/数值/
      嵌套对象边界和必要的互斥约束（`TestManagedToolSchemasAreClosed` 强制）。
- [ ] **[Managed]** 每个 structured success 都有严格 output schema；nullable、缺失、
      分页和 union 语义与官方合同一致。新增 tool 不得进入
      `openManagedOutputSchemaDebt` 技术债名单。
- [ ] **[Remote]** 每个 tool 名与 InputSchema 都来自 `mise run mcp-probe` 实拉的
      `tools/list`，PR 里给出探测日期；参数直通，schema 保持
      `additionalProperties: true`，不封闭上游契约。没有 probe 证据的 tool 不合入。
- [ ] **[Remote]** `docs/providers/<connector_type>.md` 为每个 tool 标注了 tool 名
      与 InputSchema 的来源等级和置信度：`mcp-probe` 实拉的 `tools/list`（权威）＞
      厂商官方文档或第三方 client 对**同一 endpoint** 的 `tools/list` 快照（tool 名
      可信、参数常缺）＞ 由 REST 文档／开源实现推导（近似）。任何非 probe 来源的条目
      都写明「未经 probe 校准」并留下 probe 待办。这条是**追加**要求，不放宽上一条的
      probe 硬门槛。
- [ ] **[Remote]** 每个 tool 的 `InputSchema` 顶层是 `additionalProperties: true`
      的参数直通形状，没有照抄 Managed 的封闭契约把上游合法参数挡在门外
      （`TestManagedToolSchemasAreClosed` 跳过非 Managed backend，不会替你发现）。
- [ ] **[Remote]** `OutputSchema` 可以不声明；一旦声明，必须来自真实响应采样并按
      `README.md`「安全流程（四步）」留证，禁止照 REST 文档反推。
- [ ] **[Remote]** `InputMapperKey`/`OutputMapperKey` 全部为空（引擎第一期只支持
      参数直通，非空值在触达 backend 前被拒）。
- [ ] required scope 与 read/write/destructive risk 逐 Tool 人工审阅，没有从名称或
      第三方参考实现自动推断。
- [ ] credential/config field 的 secret、default、pattern/options 和
      `PolicyIdentity`语义已审阅；固定 SaaS 不接受用户 origin。

## Runtime 与安全

- [ ] 所有 Managed、validator、OAuth token/profile/refresh 或 Remote MCP outbound
      都由 composition root 注入的`providerkit.Factory`构造。
- [ ] policy 使用精确 canonical origin、正确 PublicOnly/self-hosted gate、
      redirect 策略、总 timeout、response cap 和安全 retry。
- [ ] 生产代码没有`http.DefaultClient`、`http.DefaultTransport`、环境代理、无界
      `io.ReadAll`、真实 sleep 或包级可变 endpoint。
- [ ] credential 只通过经过审阅的 authorizer 位置发送；不会进入 URL、日志、错误、
      observer label、Tool output 或测试失败信息。
- [ ] **[Managed]** handler 不接受可切换 origin、HTTP method、任意 path/header 或
      provider-specific raw payload；路径段和 query/form/JSON 各自按上下文编码。
- [ ] **[Managed]** 非幂等操作默认单次尝试；只有稳定 idempotency contract 才允许
      write retry。
- [ ] **[Managed]** direct handler 调用也检查 connector/tool identity、精确参数集合
      和 credential shape；非法输入在 HTTP 前失败。
- [ ] **[Managed]** success response 严格解码、类型/marker/数量/字节数受限，未知
      字段不会被原样转发；归一化 structured output 也有上限。
- [ ] **[Remote]** 每个固定 endpoint 是 https、主机名落在
      `Provenance.AllowedHostnames` 内、origin 可规范化且满足 `PublicOnly` policy
      （`TestRemoteMCPFixedEndpointsSatisfyEgressPolicy`）；endpoint 由运营方填写时
      `Provenance.Kind` 必须是 `self_hosted`（Registry 强制），且在连接配置落库处
      校验 origin 策略。
- [ ] **[Remote]** 没有为该 Provider 引入任何自定义 outbound header 通道。上游存在
      会**改 tool 名**的 header 时——GitLab 的
      `X-Gitlab-Mcp-Server-Tool-Name-Prefix` 会给所有 tool 加前缀，硬编码的
      `RemoteToolName` 会全部失配——PR 明确记录未发送，且 `mcp-probe` 与生产用的是
      同一组 header（同一个 `mcpclient`），probe 结果才代表运行时会拿到的 tool 名。
- [ ] **[Remote]** `AuthBinding.CredentialFieldByAuthMethod` 只为
      api_key/custom_credential 的 auth method 建立映射且指向 `Secret: true` 字段
      （Registry 强制）；OAuth 方法没有条目，凭证绑定不依赖声明顺序。
- [ ] Provider status/envelope 映射为稳定`ToolFailure`；原始 body/message 不出现在
      Agent、日志或数据库；只有明确失效信号触发 credential-invalid。

## Credential、scope 与授权代际

- [ ] **[Managed]** validator 使用与 handler 相同的 policy、base URL 和 credential
      presentation。
- [ ] **[Remote]** validator 打的是能证明 tool 调用会成功的那个接口。上游对
      MCP 与 REST 常有不同 scope 语义（GitLab 的 `api` 不蕴含 `mcp`，必须打
      `/oauth/token/info` 而非 `/api/v4/user`），选错会把合法凭据判成失效。
- [ ] validator 调用最小只读身份/连通性接口；有官方身份时返回稳定 AccountID，
      没有时不伪造。
- [ ] `ScopesKnown=false`与“已知空 scope”严格区分；scope matcher 只在 Provider
      语义需要时注册且不能扩大权限。
- [ ] OAuth Provider 使用当前标准 parser，并覆盖 refresh、reauth、token
      scheme/scope 变化、generation 与旧 Session 失效。
- [ ] 若本卡首次引入 tenant hostname、签名 callback、bot install、resource binding
      或非标准 token response，最小能力在本 Provider 卡内实现并评审：精确签名/
      resource proof、加密绑定上下文、重放拒绝、失败不提交、generation 与旧 Session
      失效均有测试；没有第二个真实消费者前不新增全局 adapter registry。

## 确定性测试

- [ ] Definition 可注册，auth/tool/schema/scope/risk 与 backend key 有精确断言。
- [ ] **[Managed]** 每个 Tool 至少覆盖成功 request mapping、严格 normalized output
      和 schema。
- [ ] **[Managed]** 覆盖空值、边界值、Unicode、额外字段、错误类型、超长输入和
      aggregate URL/body 上限；非法输入证明没有 Provider request。
- [ ] **[Managed]** 覆盖分页终止/游标、GET retry 与 write single-attempt。
- [ ] 出网路径（Managed handler 或 validator）覆盖
      400/401/403/404/408/409/422/429/5xx、malformed/HTML/oversize response、
      Retry-After 和 credential redaction；测试不访问公网、不真实 sleep。
- [ ] 覆盖 DNS/IP/redirect/credential footprint，
      `packages/core/providerkit/testkit`只出现在`*_test.go`
      （`TestProviderTestkitIsNeverImportedByProductionCode` 强制）。
- [ ] runtime bundle 测试证明 Definition 引用的 handler、validator 和 scope matcher
      全部装配且没有 orphan 实现。
- [ ] `go test -race -count=3 ./<provider>`、connectors 全量 test/vet 和
      `git diff --check`通过。

## 文档、smoke 与发布

- [ ] `docs/providers/<connector_type>.md`记录认证、固定 endpoint/version、Tool、
      scope/risk、分页/限流、错误、安全边界、已知限制和升级注意事项。
- [ ] real smoke 经`internal/smoketest`的`Config`/`Values`默认 skip（不自己写
      `os.Getenv` 门），环境变量命名唯一，write/destructive 的精确 opt-in 走
      `smoketest.Exactly`，使用 sandbox/测试账号与资源/短期最小权限凭证，且不会
      输出秘密或业务数据。
- [ ] smoke 覆盖 validator、全部首版 Tool、关键 negative auth/permission/rate-limit
      路径和清理；记录日期、环境类别、API 版本、结果与清理状态，不记录凭证。
- [ ] release smoke 只通过`scripts/run-provider-real-smoke.sh`执行；固定
      Provider/package/harness/cleanup_mode 映射已写进
      `scripts/provider-real-smoke-manifest.tsv`（runner 与
      `scripts/test-provider-real-smoke-gate.sh`共读同一份清单，后者写死的 Provider
      条数已同步），runner 绑定 full clean `HEAD`、拒绝 SKIP/FAIL，并生成绝对路径、
      mode `0600`、不可覆盖且不含秘密的 receipt。receipt 的`cleanup_mode`不是
      cleanup `PASS`。
- [ ] Provider 文档的 smoke evidence 表把账号/认证类别、API/server 版本和清理状态
      作为独立必填列；未执行项明确写`PENDING — not run`，不得把缺失字段折叠进
      `Coverage`或仅留在 PR 评论。
- [ ] real-account evidence 按最新在前；提升为`production_supported`时，首行使用
      `PASS; implementation_commit=<full SHA>; harness=Test<Provider>RealSmoke`绑定已执行
      harness 与实现，write/destructive Provider 的 cleanup 独立为`PASS`。
- [ ] 管理台完成配置、连接、重授权和健康查看；Swagger/SDK 在 API 合同变化时同步。
- [ ] 外部审核和 smoke 均完成后才更新根 README supported 列表；Provider 文档中的
      状态与代码事实一致。
