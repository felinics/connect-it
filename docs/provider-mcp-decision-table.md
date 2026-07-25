# Provider MCP 决策表

> **本文档是 connect-it「每个 provider 该怎么实现」的调研数据源。**
> 调研日期：**2026-07-24**（含对抗性复核，复核结论优先于原始判定）
> 仓库现状复核：**2026-07-25**
> 适用对象：准备新增 provider 的工程师。动手写代码前，先在本表里找到该 provider。
>
> **架构事实源是 [`blueprint.md` §8「Provider 形态选型：Remote MCP 优先」](blueprint.md#8-provider-形态选型remote-mcp-优先)。**
> 本表是喂给 §8 的调研数据；两者冲突时以 blueprint 为准。

---

## 1. 这份文档解决什么问题

connect-it 把 SaaS 能力包装成 Agent 可调用的 Tool。实现一个 provider 有两条路径，
**本仓库实测成本差 4.5 倍**（下表的行数区间是排期估算带，4.5 倍是四个 provider 改写后的实测值）：

| 路径 | 做法 | 代码量 | 说明 |
|---|---|---|---|
| **Remote MCP** | 声明厂商托管的 MCP endpoint + 认证绑定 + tool 映射 | **≈ 350–700 行**（随 tool 数量增长） | 厂商替我们维护 tool 实现，我们只做转发和裁剪 |
| **Managed REST** | 手写 HTTP client、请求构造、响应解析、输出投影 | **≈ 1,500–3,300 行** | 每个 tool 都要自己写一遍 |

### 本仓库的实测佐证（2026-07-25 口径）

以下数字来自 `packages/connectors/<dir>/*.go`（排除 `_test.go`，不含 `internal/` 共享工具）：

| Connector | 路径 | 非测试代码行数 | Tool 数 |
|---|---|---:|---:|
| `stripe` | Remote MCP | **361** | 5 |
| `googleads` | Remote MCP（自托管 endpoint） | 380 | 2 |
| `github` | Remote MCP | 388 | 5 |
| `notion` | Remote MCP | **414** | 10 |
| `gitlab` | Remote MCP（自托管 endpoint） | 641 | 14 |
| `linear` | Remote MCP | 707 | 12 |
| `gmail` | 混合（2 Remote + 2 Managed） | 491 | 4 |
| `onedrive` | Managed REST | 450 | 2 |
| `datadog` | Managed REST | **1,469** | 5 |

**决定性证据是这个仓库自己做过一次改写。** `notion` / `stripe` / `linear` / `gitlab` 四个
2026-07-25 从 Managed REST 改成 Remote MCP：本表旧版记录的 3,296 / 2,287 / 2,463 / 1,601
（合计 **9,596 行**）→ 实测 **2,123 行**，**省 7,473 行（−78%，4.5 倍）**，而 tool 数量从 27 涨到 41。
对照仍是 Managed 的 `datadog`：1,469 行只换来 5 个 tool，`notion` 414 行换来 10 个。

**这不是理论差距，是同一个仓库里已经发生的事实。** 而 Notion 恰恰有官方托管的 Remote MCP
—— 我们当初不知道，写了 3,296 行才发现（详见 [第 4 节](#4-重点提示看起来该手写其实有官方-mcp)）。

---

## 2. 判定标准（四条，必须同时满足才算 ✅ Remote MCP）

> 这四条与 [blueprint §8.1 「四条可用性标准」](blueprint.md#81-降级到-managed-rest-的条件) 是同一套，
> 本表只是把它们逐个 provider 应用了一遍。
> ⚠️ **四条全过 ≠ 一定走 Remote MCP**：同一节还有六条降级条件（preview / 功能天花板 /
> 许可证门槛 / 传输 / 拿不到账号 / 必须精确控制输出形状）。本表的 ✅ 只表示**协议层可用**，
> 产品层是否降级由 §8.1 决定 —— `onedrive` 就是本表判 ✅ 却按 §8.1 走 Managed 的例子。

| # | 条件 | 不满足会怎样 |
|---|---|---|
| **1** | **厂商官方**：server 由 SaaS 厂商自己开发并背书，且有**官方文档 URL** 佐证 | 社区/第三方实现一律不算。厂商没文档记载的端点也不算（无 SLA、无变更通知、随时可能消失） |
| **2** | **厂商托管**：厂商自己运营的公网 HTTPS endpoint | 本地 stdio 包（`npx` / `uvx` / `docker run`）不算；需要客户自己部署的 self-hosted 也不算 |
| **3** | **Streamable HTTP 传输** | **本项目不支持 stdio，也不支持旧式 SSE 双端点。** SSE-only 直接出局 |
| **4** | **认证可用 `Authorization: Bearer <token>` 表达** | 见下方框架约束 |

### ⚠️ 最容易误判的地方

> **「厂商有官方 MCP」≠「我们能用」。**

本表里被否决的条目，绝大多数不是因为「没有 MCP」，而是因为：

- MCP 是**本地 stdio** 的（Google Ads、Adyen、Coinbase CLI、ElevenLabs、Groq、Perplexity、CircleCI、Docker Hub、QuickBooks、Clay）
- MCP 只能**查文档、不执行 API**（Twilio、Coinbase CDP Docs、OpenAI、Perplexity docs、Amplitude docs）
- MCP 方向**反了** —— 厂商做的是 MCP **Client**（Zendesk、ElevenLabs Agents、Mistral Connectors、Groq tool-use）
- MCP 是**买家侧**而非商家侧（Shopify Storefront、BigCommerce Storefront）
- 传输是 **SSE-only**（Square 文档层面、Replicate 文档层面）
- 认证**不是 Bearer**（飞书 `X-Lark-MCP-UAT`、Freshdesk 裸 key、Bitbucket Basic、Zoho 凭据内嵌 URL、WooCommerce `X-MCP-API-Key`、Jenkins Basic）
- 有**商务门槛**挡住第三方 client（Vercel redirect URI 白名单、Azure DevOps 仅 VS/VS Code、Braze EA 白名单、ClickUp 需审核）

### 框架侧的硬约束（决定第 4 条能不能过）

已核实 `packages/core/connector/types.go`：

```go
type MCPAuthBinding struct {
    Scheme string // 目前仅 "bearer"
    CredentialFieldByAuthMethod map[string]string
}
```

- ✅ **endpoint 可配置**已支持：`Endpoint{Source: EndpointFixed}` 或 `EndpointConfigField`。
  所以 instance 级 endpoint（GitLab self-managed、NetSuite、Snowflake、Databricks、Outline、
  Metabase、Looker、Chargebee、LangSmith、W&B、Smartsheet、以及各家 EU/AU 区域端点）**都能表达**。
- ❌ **不支持 `scheme=basic`**
- ❌ **不支持静态附加 header**（如 `Amazon-Ads-ClientId`、`x-dbt-prod-environment-id`、`Close-Scope`、`Stripe-Account`、`X-Lark-MCP-Allowed-Tools`）

这两个缺口卡住了不少条目，扩展它们的 ROI 见 [第 5.3 节](#53-两个框架改动能翻盘一批条目)。

### 判定标记

| 标记 | 含义 | 排期含义 |
|---|---|---|
| ✅ **Remote MCP** | 四条全过 | 可直接按 ≈400 行排期 |
| 🔧 **Managed REST** | 至少一条不过，但有可用的公开 REST API | 按 1,500–3,300 行排期 |
| ⛔ **Blocked** | 两条路都走不通 | 不排期 |
| ❓ **待验证** | 证据不足以定档 | **禁止直接排期**，先做 spike |

> ❓ 的存在是刻意的。规则是：**宁可标 ❓，也不要猜。**
> 猜错的代价是按 400 行排期、结果发现要写 3,000 行。

---

## 3. 决策表

每组内按 tier1 → tier3 排序（tier1 = 最常见）。
跨类别重复的 provider 会标注「同 X 类别」——**同一台 server 只需实现一次**。

### 3.1 开发工具 / 代码托管

| Provider | 常见度 | 判定 | 传输 | 认证 | MCP Endpoint | 官方依据 | 备注 |
|---|---|---|---|---|---|---|---|
| **GitHub** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 / PAT | `https://api.githubcopilot.com/mcp/` | [remote-server.md](https://github.com/github/github-mcp-server/blob/main/docs/remote-server.md) | 本仓库标杆（388 行）。可用子路径裁剪工具集：`/x/issues`、`/x/pull_requests`、`/x/repos`，加 `/readonly` 只读。**别用同名的本地 stdio 二进制**。GitHub Enterprise Server 自建版不支持此端点 |
| **GitLab** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2（scope `mcp`） | `https://<host>/api/v4/mcp` | [docs.gitlab.com](https://docs.gitlab.com/user/model_context_protocol/mcp_server/) | **endpoint 必须可配置**（内置在 GitLab 本体，instance 级）。18.6 起 beta，19.2 起 Free 可用。**前置条件：管理员需把 GitLab Duo availability 设为 Always on / On by default，否则端点不通** —— 这是最常见的接入失败原因，要写进错误提示。另有独立的 Orbit MCP（`/api/v4/orbit/mcp`），不是同一个东西。**本仓库已按此实现**（`packages/connectors/gitlab`，641 行 / 14 tool，endpoint 走 `EndpointConfigField`；凭据是 `mcp` scope 的 OAuth access token，PAT / project / group token 都不被 `/api/v4/mcp` 接受） |
| **Jira / Confluence**（Atlassian Rovo） | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp.atlassian.com/v1/mcp/authv2` | [support.atlassian.com](https://support.atlassian.com/atlassian-rovo-mcp-server/docs/getting-started-with-the-atlassian-remote-mcp-server/) | 同「文档」「项目管理」类别，**一台 server 覆盖 Jira + JSM + Confluence + Compass，72+ tools**，建议在我们侧做白名单裁剪。**仅 Atlassian Cloud**，Data Center / Server 完全不支持。旧 SSE 端点 `/v1/sse` 2026-06-30 后停止支持 |
| **Linear** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth2 / API key | `https://mcp.linear.app/mcp` | [linear.app/docs/mcp](https://linear.app/docs/mcp) | 同「项目管理」类别。**最干净的一条**：OAuth 2.1（含 DCR）或直接 `Bearer <Linear API key>`，两条都能喂进 MCPAuthBinding。只读变体 `/mcp/readonly`。无套餐门槛。旧 `/sse` 已 deprecated。**本仓库已按此实现**（`packages/connectors/linear`，707 行 / 12 tool，2026-07-25 由 2,463 行 Managed REST 改写而来） |
| **Sentry** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp.sentry.dev/mcp` | [getsentry/sentry-mcp](https://github.com/getsentry/sentry-mcp) | 官方另提供非标准的 `Authorization: Sentry-Bearer <token>` 直传上游 token —— **我们只用标准 OAuth 路径**。self-hosted Sentry 不支持远程端点 |
| **Cloudflare** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth2 / API token | `https://mcp.cloudflare.com/mcp` | [developers.cloudflare.com](https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/) | 同「云」类别。官方文档原文：「support both the streamable-http transport via `/mcp` and the sse transport (deprecated) via `/sse`」。**可直接把 Cloudflare API token 当 bearer**，最省事。主入口覆盖 2500+ API endpoint；另有 16 个分产品 endpoint 可做细粒度 profile |
| **Netlify** | tier3 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://netlify-mcp.netlify.app/mcp` | [docs.netlify.com](https://docs.netlify.com/build/build-with-ai/netlify-mcp-server/) | 同「云」类别。**典型「本地包 vs 远程托管」双版本坑**：GitHub 仓库 README 只写 `npx -y @netlify/mcp`（stdio），光看仓库会误判；docs.netlify.com 才给出远程端点并推荐优先使用。scopes 含 `offline_access, read, write, claudeai`。端点挂在 `*.netlify.app` 而非品牌域名，建议做成配置项 |
| **Bitbucket** | tier2 | ❓ 待验证 | Streamable HTTP | ⚠️ 无 OAuth | `https://mcp.atlassian.com/v1/mcp` | [api-token 配置](https://support.atlassian.com/atlassian-rovo-mcp-server/docs/configuring-authentication-via-api-token/) | **【复核降级】** 原判 REMOTE_MCP。已证实 OAuth 路径**完全够不到 Bitbucket**（PRM 的 `scopes_supported` 只有 jira/confluence/compass/twg，零个 bitbucket scope）。仅剩 API token 路径：个人 token 用 `Basic base64(email:token)`（**与 scheme=bearer 不兼容**），service account API key 用 `Bearer <key>`（兼容）。但厂商同页警告「Some MCP tools may not be available when you use authentication via API token」，且**未公布哪些工具幸存**。<br>➡️ **下一步**：30 分钟 spike——建 service account API key，对 `/v1/mcp` 打 `tools/list`，看 Bitbucket 工具是否出现。不出现或不接受组织级凭据 → 退 Managed REST（Bitbucket Cloud REST API 2.0 公开完备） |
| **Azure DevOps** | tier2 | ❓ 待验证 | Streamable HTTP | Entra ID OAuth | `https://mcp.dev.azure.com/{organization}` | [learn.microsoft.com](https://learn.microsoft.com/en-us/azure/devops/mcp-server/remote-mcp-server?view=azure-devops) | **【复核降级】** 技术侧全对（微软托管、Streamable HTTP、`X-MCP-Toolsets`/`X-MCP-Readonly` 普通 header 可透传）。**但 client 门槛是死的**：FAQ 原文「Other client tools such as CodeX, Claude Desktop, Claude Code, and ChatGPT require dynamic registration of an OAuth Client ID in Microsoft Entra... **For now, only Visual Studio and Visual Studio Code are supported**」。且 `login.microsoftonline.com/organizations/v2.0` 的 OIDC 配置里**根本没有 `registration_endpoint`**。「预注册多租户 Entra 应用」这条绕行路径无任何文档背书、未经验证。仍是 public preview；on-prem Server 不支持<br>➡️ 要么先做 Entra spike，要么按 Managed REST 排期，把 Remote MCP 当作 DCR 开放后的升级项 |
| **Vercel** | tier2 | 🔧 Managed REST | — | Bearer token | ~~`https://mcp.vercel.com`~~ → `https://api.vercel.com` | [vercel.com/docs](https://vercel.com/docs/agent-resources/vercel-mcp) | **【复核推翻，实测决定性】** 协议层四条全过，**但 client 审核是代码级强制的**。对 DCR 端点 `https://api.vercel.com/login/oauth/register` 实测三组 redirect URI：`http://localhost:8080/callback` → 200 发 client_id；`https://claude.ai/api/mcp/auth_callback` → 200 发 client_id；`https://connect-it.example.com/oauth/callback` → **400 `invalid_redirect_uri` "The provided redirect URIs are not approved for use by this authorization server"**。Vercel 维护 redirect-URI 白名单，只放行 localhost 与已批准厂商的回调域名。**我们是托管多租户 connector，回调域名是自己的域名 —— 正是被拒的那一类，OAuth 流程无法完成**。<br>➡️ 走 Vercel REST API（公开、`Authorization: Bearer`）。只有 Vercel 把我们的 redirect URI 加进白名单才翻回 ✅ |
| **Jenkins** | tier2 | 🔧 Managed REST | （有 Streamable HTTP） | ⚠️ HTTP Basic | — | [plugins.jenkins.io/mcp-server](https://plugins.jenkins.io/mcp-server/) | 官方（jenkinsci/mcp-server-plugin）**确实支持 Streamable HTTP**（`/mcp-server/mcp`）。否决理由两条：(1) 无厂商 SaaS，endpoint 完全是客户自建实例；(2) 认证是 API token 走 **HTTP Basic**，`scheme=bearer` 表达不了。<br>➡️ **反向机会**：若框架支持 `scheme=basic` + endpoint 从配置读，Jenkins 可从 ~2,000 行直降到 ~400 行。建议做成通用特性而非为 Jenkins 硬编码 |
| **CircleCI** | tier3 | 🔧 Managed REST | stdio only | API key | — | [circleci.com/product/mcp](https://circleci.com/product/mcp/) | 已确认无厂商托管端点。官方原话：「The CircleCI MCP server is now built into the CircleCI CLI」，独立仓库已标 deprecated。三种部署都是本地或**客户自建** docker。<br>➡️ CircleCI API v2，`Circle-Token` header。**若日后上线托管端点，这条要第一时间复查** |

### 3.2 通讯 / 协作

| Provider | 常见度 | 判定 | 传输 | 认证 | MCP Endpoint | 官方依据 | 备注 |
|---|---|---|---|---|---|---|---|
| **Slack** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2（confidential） | `https://mcp.slack.com/mcp` | [docs.slack.dev](https://docs.slack.dev/ai/slack-mcp-server/) | 官方明写「We do not support SSE-based connections or Dynamic Client Registration」——无 DCR 对我们无碍（自带 client_id/secret）。authorize `slack.com/oauth/v2_user/authorize`，token `slack.com/api/oauth.v2.user.access`。**硬门槛：必须绑定一个已注册 Slack app，且该 app 须为 Directory 上架应用或企业内部应用 —— unlisted app 被明令禁止用 MCP**，即上线前要过应用目录审核。工具面只覆盖 Web API 精选子集（search / messages / canvases / users），files、reminders、workflow triggers、admin 类都没有 |
| **Microsoft Teams** | tier1 | ✅ Remote MCP | Streamable HTTP | Entra ID OAuth | `https://agent365.svc.cloud.microsoft/agents/tenants/{tenantId}/servers/mcp_TeamsServer` | [learn.microsoft.com](https://learn.microsoft.com/en-us/microsoft-copilot-studio/mcp-teams-work-iq) | Work IQ Teams MCP（Agent 365 工具网关）。scope `McpServers.Teams.All`，需管理员在 M365 admin center 同意。**三个坑**：(1) endpoint 含 `{tenantId}`，必须从配置读；(2) 需 Microsoft 365 Copilot 许可证；(3) preview，微软明说可能改工具名和参数，「Avoid hard-coded dependencies」。别与 Teams SDK 那篇「把自己的 bot 包成 MCP server」混淆 |
| **Zoom** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp.zoom.us/mcp/zoom/streamable` | [developers.zoom.us](https://developers.zoom.us/docs/mcp/servers/connect-to-zoom-mcp-servers/) | 官方注册表 `github.com/zoom/mcp-registry` 列出全部远程 server：workspace（聚合入口）/ team-chat / meetings / docs / whiteboard / tasks / revenue-accelerator，各自独立 endpoint —— **connector 要么选 workspace 聚合入口，要么把 endpoint 做成可配置**。官方写明需 manual client registration（无 DCR），需 PKCE |
| **Google Chat** | tier1 | ✅ Remote MCP | Streamable HTTP | Google OAuth2 | `https://chatmcp.googleapis.com/mcp/v1` | [developers.google.com](https://developers.google.com/workspace/chat/api/guides/configure-mcp-server) | 工具：`search_conversations` / `list_messages` / `search_messages` / `send_message`。**只读工具只需启用 Chat API，但 `send_message` 要求在自己的 GCP 项目里配置 Chat app**。实际需 Workspace 租户（个人 gmail 无 Chat API 空间） |
| **Cisco Webex** | tier2 | ✅ Remote MCP | Streamable HTTP（**推断**） | OAuth2 | `https://mcp.webexapis.com/mcp/webex-messaging`（另有 `/webex-meeting`） | [developer.webex.com](https://developer.webex.com/mcp/docs/messaging-mcp-server) | **【复核修正 endpoint，并降置信度】** 原判「只有 Meetings，消息侧没有官方 MCP、需 Managed REST」**是错的** —— Messaging MCP 确实存在，24 个工具覆盖 messages / rooms / memberships / webhooks / files / threading。主 endpoint 应改为 messaging（或双挂可配置）。<br>⚠️ **但 Cisco 全套 MCP 文档通篇没有出现 Streamable HTTP / SSE / transport 任何字样**，传输方式是从 URL 路径形制 + 全站无 `/sse` 反推的。**接入前先发一个带 `Accept: application/json, text/event-stream` 的 initialize POST 实测握手**。管理员须在 Webex Control Hub 启用该 MCP server |
| **Intercom** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth2 / PAT | US `https://mcp.intercom.com/mcp`<br>EU `https://mcp.eu.intercom.com/mcp` | [developers.intercom.com](https://developers.intercom.com/docs/guides/mcp) | 同「客服/营销」类别。**区域化 endpoint 必须可配置**；AU 托管工作区暂不支持。`/sse` 标 Legacy。工具偏读（搜索 conversations/contacts/events，带 query DSL），大量写操作需先跑 `tools/list` 核实 |
| **Discord** | tier1 | 🔧 Managed REST | 无 | Bot token / OAuth2 | — | [docs.discord.com](https://docs.discord.com/developers/reference) | 已核实官方文档全站无官方 MCP。2026-03 开发者通讯宣布的是把开发文档做成 llms.txt / 文档检索 MCP —— **那是给写代码的人查文档用的**。市面 5+ 个 Discord MCP 全是社区实现。<br>➡️ `https://discord.com/api/v10`，`Authorization: Bot <token>`，另需合法 User-Agent。注意 Discord 能力分裂为 REST + Gateway WebSocket，Managed 只覆盖 REST，实时事件要另做 |
| **Telegram** | tier1 | 🔧 Managed REST | 无 | token in URL | — | [core.telegram.org/bots/api](https://core.telegram.org/bots/api) | 已核实官方无 MCP，搜到的全是社区实现。<br>➡️ `https://api.telegram.org/bot<token>/METHOD`。**坑：token 拼在 URL path 里，不是 Authorization header** —— 凭据注入逻辑与其他 provider 不同，**日志/错误信息必须做 token 脱敏**。Bot API 只能看到 bot 可见的会话；用户级会话需 MTProto（完全不同的协议，不建议纳入） |
| **飞书 / Lark** | tier1 | ❓ 待验证 | Streamable HTTP | ⚠️ **结论冲突** | `https://mcp.feishu.cn/mcp`（CN）<br>`https://mcp.larksuite.com/mcp`（国际） | [open.feishu.cn](https://open.feishu.cn/document/mcp_open_tools/developers-call-remote-mcp-server) | **⚠️ 全表唯一的内部结论冲突，见 [4.3 节](#43-飞书--lark结论冲突未决)。** 两组调研员给出相反结论：一组读文档认为只有私有头 `X-Lark-MCP-UAT`/`TAT`、判 Managed REST；另一组实测拿到 `401 + WWW-Authenticate: Bearer realm="mcp"`、PRM `authorization_servers=["https://accounts.feishu.cn/mcp"]`、`bearer_methods_supported=["header"]`，判 Remote MCP。<br>**且实测方额外发现**：每个请求须声明 `X-Lark-MCP-Allowed-Tools` 头 —— **这一条 MCPAuthBinding 表达不了**。<br>➡️ 定档前必须实测一次完整 OAuth + `tools/list` |
| **企业微信 WeCom** | tier2 | 🔧 Managed REST | （有 Streamable HTTP） | ⚠️ 凭据内嵌 URL | — | [developer.work.weixin.qq.com](https://developer.work.weixin.qq.com/document/path/101468) | 官方确有 streamableHTTP 形态的 MCP（「API 模式机器人」），但三点致命：(1) 能力只有文档/智能表格，**不含消息收发**；(2) URL 是按机器人生成的个人链接、凭据内嵌，不是 OAuth Bearer；(3) 官方写明「当前授权有效期为 7 天」，无法做长期托管连接。官方帮助中心说明主要面向 10 人以下企业。<br>➡️ `qyapi.weixin.qq.com/cgi-bin/*`，access_token 走 **query 参数**，需自行缓存刷新（7200s），注意 IP 白名单与企业/应用双层 secret |
| **钉钉 DingTalk** | tier2 | 🔧 Managed REST | stdio only | 自定义 header | — | [open-dingtalk/dingtalk-mcp](https://github.com/open-dingtalk/dingtalk-mcp) | 官方 MCP 是 `npx -y dingtalk-mcp@latest` + 环境变量，**纯本地 stdio**。<br>⚠️ **诚实说明残留不确定性**：open.dingtalk.com 的 MCP 文档页是 JS 渲染 SPA，四次抓取都只拿到导航目录、拿不到正文，「钉钉是否另外托管了 streamable HTTP endpoint」**没有 100% 排除**；但开放平台索引、官方仓库、中文搜索里都没出现任何托管地址。<br>➡️ 先按 `https://api.dingtalk.com` + `x-acs-dingtalk-access-token` 头做，**同时标记需人工复核 open.dingtalk.com 正文** |
| **Twilio** | tier1 | 🔧 Managed REST | （文档 MCP 是 Streamable HTTP） | ⚠️ HTTP Basic | — | [twilio.com/docs/ai/mcp](https://www.twilio.com/docs/ai/mcp) | 同「客服/营销」类别。**最容易误判**：`https://mcp.twilio.com/docs` 确实是厂商托管、Streamable HTTP、**且完全不需要认证**，但官方一句话定性：「The server provides API search and documentation retrieval. **It does not execute API calls on your behalf.**」只有 `twilio__search` / `twilio__retrieve` 两个工具，**发不了短信、打不了电话**。<br>➡️ `api.twilio.com/2010-04-01/...`，**HTTP Basic**（AccountSid:AuthToken），不是 Bearer。官方 roadmap 提到未来会出「Execute-ready, OAuth-authenticated MCP tools」——**本类别最值得设复查提醒的一条** |

### 3.3 文档 / 知识库

| Provider | 常见度 | 判定 | 传输 | 认证 | MCP Endpoint | 官方依据 | 备注 |
|---|---|---|---|---|---|---|---|
| **Notion** ★ | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp.notion.com/mcp` | [developers.notion.com](https://developers.notion.com/guides/mcp/get-started-with-mcp) | **★ 本仓库曾用 3,296 行 Managed REST，2026-07-25 已改写为 Remote MCP（414 行 / 10 tool）**，见 [第 4.1 节](#41-头号案例notion已改造完成)。官方明确 Streamable HTTP 为推荐传输，`/sse` 是 legacy。<br>⚠️ **重要澄清**：官方原文「Notion MCP requires user-based OAuth authentication and does not support bearer token authentication」指的是**不接受长期的 internal integration token（`ntn_...`）**，**不是**「不能用 `Authorization: Bearer` 这个 header」；走完 OAuth 后 access token 仍然是 `Authorization: Bearer` 提交，**符合 MCPAuthBinding**（`packages/connectors/notion/definition.go` 就是 `MCPAuthBinding{Scheme: "bearer"}`）。支持 DCR (RFC 7591)。代价：MCP 的 OAuth app 与现有 REST OAuth app 是两套凭证，不能复用；且无法做无人值守的纯机器连接（Definition 里因此只有 oauth 一种 auth method）。⚠️ **三代 Notion MCP 的 tool namespace 零重叠**，远程版是 `notion-` 前缀那套 |
| **Confluence**（Atlassian Rovo） | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth 2.1 / API token | `https://mcp.atlassian.com/v1/mcp/authv2` | [support.atlassian.com](https://support.atlassian.com/atlassian-rovo-mcp-server/docs/getting-started-with-the-atlassian-remote-mcp-server/) | **与「开发工具」的 Jira 条目是同一台 server，只需实现一次。** 一个 endpoint 覆盖 Confluence + Jira + JSM + Bitbucket + Compass，72+ tools —— 若拆成多个 connector，需在网关层按 tool 名过滤。**能力受认证方式影响**：JSM 与 Bitbucket 的 tool 只在 API token 下可用，Compass 的 tool 只在 OAuth 2.1 下可用。**Cloud only**，Data Center / Server 用户只能走 Managed REST |
| **Google Docs** | tier1 | ✅ Remote MCP | HTTP（文档未逐字写 Streamable） | Google OAuth2 | `https://docsmcp.googleapis.com/mcp/v1` | [developers.google.com](https://developers.google.com/workspace/guides/configure-mcp-servers) | 同一套 URL 模式覆盖全家桶：Drive `drivemcp` / Sheets `sheetsmcp` / Slides `slidesmcp` / Gmail `gmailmcp` / Calendar `calendarmcp` / Chat `chatmcp` / People。**本仓库 `gmail` connector 已在用这套端点。**<br>⚠️ 属 **Developer Preview**，且需在客户自己的 GCP project 里逐个启用 8 个 API + 对应 MCP service + 配 OAuth consent screen —— **不是「OAuth 一下就能用」**。文档写「Transport: HTTP」未逐字写 Streamable HTTP，故置信度 medium |
| **SharePoint / Microsoft 365**（Work IQ） | tier1 | ✅ Remote MCP | Streamable HTTP（`type: "http"`） | Entra ID OAuth | `https://agent365.svc.cloud.microsoft/agents/tenants/{tenantId}/servers/mcp_SharePointRemoteServer` | [learn.microsoft.com](https://learn.microsoft.com/en-us/microsoft-copilot-studio/mcp-sharepoint-work-iq) | 同系列还有 OneDrive (`mcp_OneDriveRemoteServer`)、Word、Mail、Calendar、Teams。**endpoint 含 `{tenantId}` 必须从配置读**。<br>⚠️ **硬门槛**：需 M365 Copilot 许可证 + Entra 注册 enterprise application + 管理员对**每个 MCP server 单独授权**；管理员可在 M365 admin center 全局 block。仍是 preview，官方明写工具名/参数可能变。**旧的 SharePoint/OneDrive Remote MCP 已于 2026-03-13 弃用，别用旧文档端点** |
| **Box** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth 2.0 | `https://mcp.box.com`（**根域名，无 `/mcp` 后缀**） | [developer.box.com](https://developer.box.com/guides/box-mcp/remote) | 同「存储」类别。2025-08 GA。**【复核已钉死 endpoint】** 原调研标注「未确认是否要加 `/mcp`」；实测 PRM 自报 `"resource": "https://mcp.box.com/"`，官方指南也给根路径。`https://mcp.box.com/mcp` 同样返回 401，裸探测**无法区分**，但**用 `/mcp` 会在 OAuth token 交换时产生 audience/resource 不匹配风险**。<br>**复核另修正两点**：文档写的是 OAuth **2.0** 且未提 PKCE（原调研写成「OAuth 2.1 + PKCE」）；Enterprise Advanced 许可**只针对 `docgen.readwrite` scope**，`root_readwrite`/`ai.readwrite` 无此要求。需管理员在 Box Admin Console → Integrations 启用预置 integration |
| **Dropbox** | tier2 | ✅ Remote MCP | Streamable HTTP | Dropbox OAuth 2.0 | `https://mcp.dropbox.com/mcp` | [help.dropbox.com](https://help.dropbox.com/integrations/connect-dropbox-mcp-server) | 同「存储」类别。scopes 含 `files.content.read` / `files.content.write` / `files.metadata.read` / `sharing.*` —— **读写文件内容都在**，够做文件连接器。**别与 Dropbox Dash MCP（`https://mcp.dropbox.com/dash`）混用**，那是跨应用企业搜索，另一个产品。2026-03 起 open beta |
| **Coda** | tier2 | ✅ Remote MCP | Streamable HTTP | **OAuth2**（含 DCR） | `https://coda.io/apis/mcp` | [help.coda.io](https://help.coda.io/hc/en-us/articles/44722661982989-Connect-to-the-Coda-MCP) | **【复核修正认证方式】** 原判 `bearer_pat`（因为 help.coda.io 对抓取返回 403，只能读搜索引擎摘要）。实测服务本体推翻了它：`401 + WWW-Authenticate: Bearer realm="mcp", scope="mcp:all"`，PRM `authorization_servers=["https://coda.io"]`，AS metadata 暴露 `registration_endpoint=https://coda.io/v4/api/oauth2/register` —— **完整 RFC 7591 DCR**。<br>➡️ **不需要**让用户手动「生成 restriction type = MCP 的 token」，走标准授权码流即可。集成路径比原报告好得多。（Coda 已被 Grammarly/Superhuman 收购，文档正迁往 help.superhuman.com，URL 可能变） |
| **GitBook** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth2 / PAT | `https://mcp.gitbook.com/mcp` | [gitbook.com/docs](https://gitbook.com/docs/getting-started/ai-documentation/gitbook-mcp) | 本类别条件最干净的一个。官方原文「GitBook MCP supports streamable HTTP」，且**明确 stdio 与 SSE 都不支持**。读写都支持（建站、改内容、开 change request）。无套餐限制。<br>⚠️ **别混淆**：每个已发布文档站会自动生成一个只读的 per-site MCP（`https://<site>/~gitbook/mcp`），面向读者；我们要接的是平台级的 `mcp.gitbook.com/mcp` |
| **Guru** | tier2 | ✅ Remote MCP | HTTP（未逐字写 Streamable） | OAuth / API token | `https://mcp.api.getguru.com/mcp` | [developer.getguru.com](https://developer.getguru.com/docs/guru-mcp-server-overview) | 官方仓库 `guruhq/remote-mcp-server`。官方只说「支持最新 MCP spec」，未逐字写 Streamable HTTP，**接入时先发一个 initialize POST 实测**。能力偏检索：search / 问答 / Knowledge Agents |
| **Outline** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth / API key | `https://{subdomain}.getoutline.com/mcp` | [docs.getoutline.com](https://docs.getoutline.com/s/guide/doc/mcp-6j9jtENNKL) | 官方原文「Outline only supports the Streamable HTTP transport」。**endpoint 是 instance 级**（云端按 workspace 子域，self-hosted 是客户自己的域名），必须从配置读 —— 与 GitLab 同类问题。workspace admin 可在 Settings → AI 整体关掉 MCP，**连接失败时要能给出这个提示** |
| **Slite** | tier3 | ✅ Remote MCP | 未逐字声明（推断） | OAuth | `https://api.slite.com/mcp` | [slite.com/help](https://slite.com/help/77mvFqJWG1tduF/Slite-MCP) | 2026-02-16 发布，厂商集中托管。**官方已明确弃用本地 self-hosted MCP，远程 endpoint 是唯一支持路径** —— 对我们是好消息。官方未逐字声明 Streamable HTTP vs SSE，建议实测 |
| **Nuclino** | tier3 | ✅ Remote MCP | Streamable HTTP（stateless） | OAuth 2.0 + PKCE / API key | `https://api.nuclino.com/mcp` | [help.nuclino.com](https://help.nuclino.com/70af7f4f-connect-nuclino-to-ai-assistants-with-mcp) | 官方帮助中心 + 官方博客双重确认。「MCP over HTTPS, JSON responses, stateless mode」。**认证特别友好**：主用 OAuth 2.0 + PKCE，不支持 OAuth 的客户端可直接 `Bearer <API_KEY>` —— 两条路都落到 `scheme=bearer`。官方写明「Available on all plans」，无套餐门槛 |
| **Craft** | tier3 | ✅ Remote MCP | 未逐字声明 | OAuth 风格 | `https://mcp.craft.do/my/mcp` | [craft.do](https://www.craft.do/imagine/guide/mcp/mcp) | ⚠️ **官方两篇文档自相矛盾**：craft.do 指南给固定 URL，support.craft.do 却写「为每个连接生成一个 unique MCP endpoint」。**若实际是 per-connection 带密钥的 URL，MCPAuthBinding 会不适配**，需要支持把整个 endpoint URL 存成连接配置。**接入前必须实测一次握手** |
| **飞书云文档 / Lark Docs** | tier2 | ❓ 待验证 | Streamable HTTP | ⚠️ **结论冲突** | `https://mcp.feishu.cn/mcp` | [open.feishu.cn](https://open.feishu.cn/document/mcp_open_tools/developers-call-remote-mcp-server) | **与「通讯」「存储」「项目管理」类别的飞书条目是同一台 server，结论冲突未决，见 [4.3 节](#43-飞书--lark结论冲突未决)。**<br>本类别调研员判 Managed REST，理由：(1) 凭证走自定义头 `X-Lark-MCP-UAT`/`TAT`，官方明说不是 Authorization header；(2) 终端用户模式把密钥内嵌 URL，官方称其「相当于个人密钥」，且部分文档已标 Deprecated。<br>**但「存储」类别调研员实测到了标准 Bearer 路径。**<br>➡️ 无论结论如何，都建议记一条 backlog：**给 MCPAuthBinding 加可配置 header 名，飞书就能从 ~3,000 行降到 ~400 行**，钉钉/企业微信很可能同形态 |
| **Slab** | tier3 | 🔧 Managed REST | 无 | Bearer PAT | — | [help.slab.com](https://help.slab.com/en/articles/6545629-developer-tools-api-webhooks) | 已核实无厂商官方 MCP，只有 GraphQL API + webhooks。搜索里的「Slab MCP」全是他方产物（个人项目 / Dust 平台自包的连接器）。<br>➡️ GraphQL `https://api.slab.com/v1/graphql`，`Authorization: Bearer <token>`（token 绑定一个 Bot user）。⚠️ **硬性商务限制：API 与 webhooks 仅对 Business / Enterprise 套餐开放** —— 限制实际可用人群，排期可降优先级。GraphQL 结构（一个 endpoint + 多个 query/mutation）可能比 Notion 的 3,300 行略省 |
| **Obsidian** | tier2 | ⛔ Blocked | stdio only | — | — | [obsidian-local-rest-api](https://github.com/coddingtonbear/obsidian-local-rest-api) | **两条路都走不通。根本原因是产品形态**：Obsidian 本地优先，vault 就是用户磁盘上的 Markdown 文件，**厂商侧根本没有承载内容的云服务可供调用**。Obsidian Sync 是端到端加密同步管道、Publish 是静态发布，都没有第三方读写 API。所有 Obsidian MCP 都跑在用户自己机器上（最主流的插件监听 `https://127.0.0.1:27124/mcp/`）。<br>➡️ **不排期。** 若未来真有需求，唯一形态是让用户自己暴露本地 endpoint 并把 URL+token 填进连接配置 —— 属于另一种 connector 模型，不在当前两条路径内 |

### 3.4 CRM / 销售

| Provider | 常见度 | 判定 | 传输 | 认证 | MCP Endpoint | 官方依据 | 备注 |
|---|---|---|---|---|---|---|---|
| **Salesforce** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2（`mcp_api`） | `https://api.salesforce.com/platform/mcp/v1/platform/sobject-reads`（另有 `sobject-all` 等） | [developer.salesforce.com](https://developer.salesforce.com/docs/platform/hosted-mcp-servers) | Hosted MCP Servers 2026-04 GA。**四个坑**：(1) **不是单一 endpoint** —— 按能力拆成 `sobject-all` / `sobject-reads` / `sobject-mutations` / `sobject-deletes` / Data 360 / Tableau Next，connector 要暴露 server 选择或默认 `sobject-all`；(2) **无 DCR** —— 每个客户 org 必须自建 External Client App 并把 consumer key/secret 交给我们，**client_id/secret 必须存在 per-connection 配置里，不能用单一全局 app**；(3) 需 Enterprise Edition 以上且在 Setup 里开启；(4) 自定义 server 有独立 URL，endpoint 必须配置驱动 |
| **HubSpot** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth 2.1 + PKCE | `https://mcp.hubspot.com`（**根路径**） | [developers.hubspot.com](https://developers.hubspot.com/docs/apps/developer-platform/build-apps/integrate-with-the-remote-hubspot-mcp-server) | 同「客服/营销」类别。2026-04-13 GA 到所有账号，**读写都有**（contacts / companies / deals / tickets / quotes / invoices / orders / products / subscriptions / lists / campaigns / landing pages / blog posts）。⚠️ **endpoint 是裸根路径，`https://mcp.hubspot.com/mcp` 返回 404**。⚠️ **不支持 DCR** —— 需在 HubSpot 账号里手工创建 "MCP auth app" 拿 client_id/secret，**这一步无法自动化，要做进 onboarding 流程**。无套餐门槛 |
| **Pipedrive** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth2 / API token | `https://mcp.pipedrive.ai/mcp` | [support.pipedrive.com](https://support.pipedrive.com/en/article/mcp-claude) | ⚠️ **注意域名是 `.ai` 不是 `.com`**，极易写错。401 响应带 `mcp-session-id` header（Streamable HTTP 规范头，传输方式的决定性证据）。401 body 提示「No API token or authorization header provided」，即**也接受 Pipedrive API token**。**所有套餐可用，但用量计入套餐 token 限额**（可加购）—— 应作为配额风险提示给用户 |
| **Attio** | tier2 | ✅ Remote MCP | Streamable HTTP | **仅 OAuth** | `https://mcp.attio.com/mcp` | [docs.attio.com](https://docs.attio.com/mcp/overview) | 2026-02 GA，~37 tools（records / lists / comments / notes / tasks / meetings / emails / workspace / reporting）。官方明确「no API keys required」，操作以授权用户身份执行。传输方式由排除法确认：`/sse` 返回 404，**不存在 SSE 端点**。仅 `query-particle-sql` 一个工具受套餐限制 |
| **Apollo.io** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth 2.0 | `https://mcp.apollo.io/mcp` | [docs.apollo.io](https://docs.apollo.io/docs/apollo-mcp) | 官方原文「if your client asks for a transport type, choose Streamable HTTP」，无 API key、无本地安装。scope 列表很大（people/org search+enrich、contacts、accounts、opportunities、campaigns、lists、conversation intelligence、tasks、custom objects）。⚠️ **enrichment/search 会消耗 Apollo credits，套餐/API/credit 限额同样适用于 MCP —— 要把 credit 耗尽做成一等错误** |
| **Close** | tier3 | ✅ Remote MCP | Streamable HTTP | OAuth 2.0（含 DCR） | `https://mcp.close.com/mcp` | [developer.close.com/mcp](https://developer.close.com/mcp) | 官方文档直写 transport = HTTP Streamable。scopes：`mcp.read` / `mcp.write_safe` / `mcp.write_destructive` / `offline_access`。<br>⚠️ **实现阻塞点**：写权限由**额外的非认证 header `Close-Scope`** 控制。**MCPAuthBinding 目前只建模 Authorization header** —— 若转发层不能注入静态附加 header，**Close 只能是只读的**。见 [5.3 节](#53-两个框架改动能翻盘一批条目) |
| **Gong** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp.gong.io/mcp` | [collective.gong.io](https://collective.gong.io/integrations/claude-mcp-client) | ⚠️ **三个严重限制，排期前先确认业务需求**：(1) **无 DCR** —— Gong 技术管理员必须手工创建 MCP integration 并**把我们的 redirect URI 加白名单**，client 凭据与 redirect 注册都是 per-customer，**onboarding 不是自助的**；(2) **只读且极窄，只有 3 个工具**（`ask_account` / `ask_deal` / `generate_brief`），**没有原始通话/转录列表** —— 若产品需要转录级访问，Remote MCP 不够，仍需 Managed REST；(3) 需套餐开启 MCP Server access |
| **Outreach** | tier3 | ✅ Remote MCP | Streamable HTTP | OAuth 2.1 + **DCR** | `https://api.outreach.io/mcp` | [support.outreach.io](https://support.outreach.io/support/solutions/articles/159000425158-outreach-mcp-server-overview) | **支持 DCR，无需 per-customer app 注册 —— 比 Salesforce/Gong 友好得多**。<br>⚠️ **硬商务门槛**：需 org 开启 **Amplify add-on** + 有效 credits + 有 licensed seat，**很多 Outreach 客户没有**。RBAC 跟随用户 Profile；有 API 限流 |
| **Salesloft** | tier3 | ❓ 待验证 | ⚠️ 存疑 | OAuth2（含 DCR） | `https://mcp.salesloft.com/mcp` | ⚠️ 无有效官方技术文档 | **【复核降级】** 两条硬伤：(1) **引用的官方依据不成立** —— 那个 newsroom URL 返回 200，**但任意伪造 slug 也返回 200**（软 404），citation 什么都证明不了；`developers.salesloft.com/docs/mcp` 与 `/mcp` 均 404，**找不到任何厂商技术文档**。(2) **传输证据指向反面** —— `https://mcp.salesloft.com/` 公开 manifest 自报 `"mcp_version": "2024-11-05"`，**这是 Streamable HTTP 之前的 spec 版本**（Streamable HTTP 是 2025-03-26 才有的）。其余信号（RFC9728 PRM、GET `/mcp` 返回 401 而非 405）又与 Streamable HTTP 一致 —— 信号矛盾。<br>**复核补充的两条实用发现**：**它是只读的**（manifest 列 15 个工具，全部 `readOnlyHint: true`，AS scope 全是 `:read`）；**支持 DCR**（`/auth/register`）。域名确属 Salesloft（TLS SAN `*.salesloft.com`），但 manifest 自称 "Specula MCP Server (SalesLoft)"，暗示背后是第三方平台 |
| **Microsoft Dynamics 365 Sales** | tier2 | ❓ 待验证 | ⚠️ 文档歧义 | Entra ID OAuth | `https://agent365.svc.cloud.microsoft/mcp/environments/<EnvID>/servers/msdyn_SalesMCPServer`；Dataverse `https://<org>.crm.dynamics.com/api/mcp` | [learn.microsoft.com](https://learn.microsoft.com/en-us/dynamics365/sales/connect-agents-to-model-context-protocol) | **【复核降级】** 卡在传输这一条 —— 恰恰是本文档警告最容易搞错的地方。文档**从未声明 Streamable HTTP**，它说的是「select the server type as **HTTP (HTTP or Server-Sent Events)**」，**这个 VS Code 标签把 HTTP 与 SSE 打包成一个选项，是构造性歧义而非确认**。<br>也无法用探测解决：用文档自带的示例环境 ID POST 会得到 `400 EndpointInvalid`，**微软在发出任何认证挑战之前就校验租户路径段** —— 拿不到 `WWW-Authenticate`、拿不到 RFC9728 发现、拿不到握手。<br>**另两条已确认的阻塞**：文档开篇即写「**Claude Desktop isn't supported at this time**」；前置条件要求 Dynamics 365 Sales **与** Copilot Studio 双管理员权限、为非 Copilot-Studio 客户端打开「Allow access to Dataverse MCP Server from MCP clients」开关、以及**足够的 Copilot Studio credits**。<br>➡️ 若要 spike，聚焦 Dataverse endpoint（真正做记录 CRUD 的是它）。⚠️ 自 2025-12-15 起，非 Copilot-Studio agent 调用 Dataverse MCP 工具**按 Copilot Credits 计费** |
| **Clay** | tier3 | ❓ 待验证 | Streamable HTTP（推断） | OAuth2（含 DCR） | `https://api.clay.com/v3/mcp` | ⚠️ 厂商文档只记载本地 stdio | **【复核降级 —— 技术上没问题，卡在证据规则】** 实测结果其实比原报告更好：`401` + PRM `{resource: .../v3/mcp, authorization_servers: [https://api.clay.com], scopes_supported:[mcp]}`，AS 暴露 `registration_endpoint=/oauth/register`、`authorization_endpoint=https://app.clay.com/oauth/authorize`，**DCR 可用、明确是厂商自有基础设施上的 OAuth2**；`GET /v3/mcp` 返回 405（POST-only，符合 Streamable HTTP）。<br>**但 Clay 自己的文档只描述本地 stdio 产品**（`{"command":"clay","args":["mcp"]}`，并说明它「needs a coding-agent host that can run `clay mcp` as a local process」）。远程 URL **只有第三方目录佐证** —— 本项目规则明令不能据此判定。**厂商不记载的端点 = 厂商没有承诺维护它**，是实打实的 sunset 风险 |
| **Zoho CRM** | tier2 | 🔧 Managed REST | ⚠️ SSE 形制 | ⚠️ 凭据内嵌 URL | — | [zoho.com/crm/developer](https://www.zoho.com/crm/developer/docs/mcp/setup/claude.html) | **看起来像 Remote MCP 的陷阱。** Zoho 确有官方托管 MCP 门户（`mcp.zoho.com` 已验证存活），但**两条判据都不过**：(1) 用户在门户生成的是 per-org URL `https://zoho-crm-<type>-<orgId>.zohomcp.in/mcp/<token>/message` —— **凭据在 URL path 里**，Zoho 自己的帮助文本都说「handle your MCP server URL like a password」，`scheme=bearer` 表达不了；(2) 结尾的 `/message` 是**旧式 HTTP+SSE 的 message 端点形制**，不是单一 Streamable HTTP 端点。另有四个 server 变体和 per-org 主机名。<br>➡️ Zoho CRM v8 REST API + OAuth2（`Zoho-oauthtoken` header）。⚠️ **多 DC 问题**：`.com`/`.eu`/`.in`/`.com.au`/`.jp` 账号 API base host 不同，**必须从 token 响应的 `api_domain` 取**。**约两个季度后复查一次** |
| **Copper** | tier3 | 🔧 Managed REST | 无 | API key | — | [developer.copper.com](https://developer.copper.com/) | **确认无官方 MCP，不是「没搜到」**：查过开发者门户与帮助中心开发者集合，**全站不提 MCP 或 agent 集成**；`mcp.copper.com` DNS 不解析。所有命中都是第三方（Zapier / Pipedream / Apideck / n8n / CData）。<br>➡️ `https://api.copper.com/developer_api/v1`，`X-PW-AccessToken` + `X-PW-Application` + `X-PW-UserEmail` 三个头，~180 req/min。tier3 且确实小众（Google Workspace 生态的 SMB CRM），**优先级排在 Pipedrive/Attio/Close 之后** |

### 3.5 支付 / 财务

| Provider | 常见度 | 判定 | 传输 | 认证 | MCP Endpoint | 官方依据 | 备注 |
|---|---|---|---|---|---|---|---|
| **Stripe** ★ | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2（含 DCR）/ restricted key | `https://mcp.stripe.com/` | [docs.stripe.com/mcp](https://docs.stripe.com/mcp) | **★ 本仓库曾用 2,287 行 Managed REST，2026-07-25 已改写为 Remote MCP（361 行 / 5 tool）。** 同「电商」类别。官方文档直接给 curl 示例：`POST https://mcp.stripe.com/` + `Authorization: Bearer <secret_key>`。两种认证都支持，官方推荐 agent 用 restricted key（`rk_...`）限权。无套餐门槛、无 SSE-only 限制。<br>⚠️ 可选的 `Stripe-Account: acct_xxx` header 支持 Connect 子账号 —— **若只注入 Authorization 会丢失这个能力**，Connect 场景需要额外的 header 透传 |
| **PayPal** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp.paypal.com/mcp`（沙箱 `https://mcp.sandbox.paypal.com/mcp`） | [developer.paypal.com/blog](https://developer.paypal.com/community/blog/streamable-paypal-mcp/) | 同「电商」类别。官方博客明写「We have implemented streamable HTTP alongside our existing SSE implementation」，SSE 只是向后兼容。<br>⚠️ **文档已滞后，路径有坑（两轮独立实测一致）**：官方 quickstart 写的 `/http` 路径**实测 prod 与 sandbox、GET 与 POST 四种组合全部 404**；而 `/mcp` 返回 401 + 标准 `WWW-Authenticate: Bearer` + PRM。**endpoint 用 `/mcp`，别照抄文档的 `/http`。**<br>⚠️ 「`/mcp` 承载的是 Streamable HTTP 而非另一个 SSE 挂载点」目前**只有推断、无厂商文档背书** —— 接入前拿真实 token 做一次 initialize 握手确认 |
| **Oracle NetSuite** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth 2.0 + PKCE | `https://<accountid>.suitetalk.api.netsuite.com/services/mcp/v1/suiteapp/com.netsuite.mcpstandardtools` | [docs.oracle.com](https://docs.oracle.com/en/cloud/saas/netsuite/ns-online-help/article_0902023450.html) | **官方文档写得最清楚的一条。** Oracle 的「NetSuite AI Connector Service」FAQ 逐条列出对 AI client 的要求：「Remote MCP / Protocol version 2025-06-18 / **Streamable HTTP** / **OAuth 2.0 Authorization Code Grant with PKCE**」—— **四条判定标准全中**。**endpoint 是 per-account 的**，必须从配置读 accountid 拼接。前置条件：客户需装免费的「MCP Standard Tools」SuiteApp，启用 Server SuiteScript / REST Web Services / OAuth 2.0 三个 feature。官方原文「not a paid feature」。文档 2026-07-10 更新，非常新 |
| **Brex** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth2 / API key | `https://api.brex.com/mcp` | [developer.brex.com/docs/mcp](https://developer.brex.com/docs/mcp) | 两种认证都行：OAuth2（AS `https://accounts-api.brex.com/oauth2/default`）或直接 `Bearer <BREX_ACCESS_TOKEN>`（适合无 OAuth 能力的程序化客户端）。<br>⚠️ **开通门槛要写进 UI 提示**：账号 admin 需先在 Settings → Developer 接受 Developer API 协议，**并在 Settings → Beta features 打开「Brex in AI assistants」**；工具可见性还受用户 Brex 角色约束。别用社区包 `crazyrabbitLTC/mcp-brex-server` |
| **Ramp** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp.ramp.com/mcp`（demo 数据 `https://demo-mcp.ramp.com/mcp`） | [docs.ramp.com](https://docs.ramp.com/developer-api/v1/guides/ramp-mcp-remote) | ⚠️ 官方文档 HTML 是 JS 壳，**真正内容在 `https://docs.ramp.com/llms-full.txt`**。<br>⚠️ **两条关键限制**：(1) 官方原文「External MCP clients are supported, but you must authorize through the **shared Ramp MCP OAuth client**, not through your own Developer API OAuth application」；(2) 「Embedding Ramp MCP in your own product, or routing through a gateway... Ramp must **allowlist the redirect URI** first」—— 需提 support ticket 报备我们的 redirect URI，**不支持通配符子域，必须精确 host**。这是一次性商务动作，**不阻塞技术路径但要排期**。<br>多业务体：`https://mcp.ramp.com/<business-identifier>/mcp`（保留字 `developer`/`employee`/`mcp`/`ramp-data`/`ramp-rate` 不可用） |
| **Mercury** | tier3 | ✅ Remote MCP | Streamable HTTP | OAuth 2.0 + **DCR** | `https://mcp.mercury.com/mcp` | [docs.mercury.com](https://docs.mercury.com/docs/connecting-mercury-mcp) | **支持 DCR（RFC 7591，注册端点 `/register`）—— 不用人工申请 client_id/secret，对我们特别友好。**<br>⚠️ **三条限制**：(1) 仍标 Beta；(2) **只读** —— 官方明确 MCP 无法发起转账或修改账户数据，工具集为 `search_transactions` / `get_account_balance` / `get_card_details` / `get_recipient_details`；(3) **OAuth session 3 天过期需重新授权**，token 刷新逻辑要做。写操作（发起支付）只能另走 Mercury REST API |
| **Chargebee** | tier3 | ✅ Remote MCP | Streamable HTTP | **Bearer API key**（优先） | `https://<site>.mcp.chargebee.com/<server-slug>`（EU `mcp.eu.` / AU `mcp.au.`） | [chargebee.com/docs](https://www.chargebee.com/docs/billing/2.0/ai-in-chargebee/custom-mcp-server) | **endpoint 是 per-site + per-region，必须从配置读**（同 NetSuite）。<br>认证优先走 `Authorization: Bearer YOUR-API-KEY`（在 agent 配置页生成）—— **正好匹配 `scheme=bearer`**。OAuth 也支持但官方注明「不支持 Multi-Business Entity 启用的站点」，且 OAuth 2.1 仍标 coming soon。<br>💡 **传输判读要点**：共享入口实测 POST 返回 `401` + `content-type: text/event-stream`，body 是 SSE 帧包裹的 JSON-RPC error —— **这是标准 Streamable HTTP（POST 响应以 SSE 编码是规范允许的），不是旧式 SSE 双端点**。别误判 |
| **Plaid** | tier2 | ✅ Remote MCP<br>（⚠️ 能力受限） | Streamable HTTP | OAuth（`mcp:dashboard`） | `https://api.dashboard.plaid.com/mcp/` | [plaid.com/docs](https://plaid.com/docs/resources/mcp/) | **技术上四条全中，但功能覆盖是个大坑，务必先读完再排期。**<br>工具集只有 `plaid_debug_item` / `plaid_get_link_analytics` / `plaid_get_usages` / `plaid_list_teams` / `plaid_get_tools_introduction` —— **纯运维诊断面**。Plaid 真正的业务面（Link token、Item access token、`/transactions`、`/auth`、`/balance` 这些 per-end-user 资金数据）**完全不在 MCP 里**。<br>➡️ **目标是「读用户银行流水」→ 按 Managed REST 排期；目标是「给 agent 看自家集成健康度」→ 才是 400 行的 Remote MCP。** 建议在 catalog 里标成能力受限。⚠️ token 仅 15 分钟有效，需实现频繁刷新 |
| **QuickBooks Online**（Intuit） | tier1 | ❓ 待验证 | stdio（官方）/ ? | OAuth2 | ⚠️ 未证实：`https://ai-inc.quickbooks.intuit.com/v1/mcp` | [intuit/quickbooks-online-mcp-server](https://github.com/intuit/quickbooks-online-mcp-server) | **故意标 ❓，既不要按 Remote MCP 排期，也不要急着按 Managed REST 排期。**<br>**已确认**：Intuit 官方 2025-10 发布的 MCP server README 明写「this is a **local** MCP server. It runs as a **stdio subprocess**」——按标准出局。<br>**存疑**：多个第三方目录一致声称存在托管端点 `https://ai-inc.quickbooks.intuit.com/v1/mcp`。两路验证都无法定论 —— curl 与 WebFetch 对该 URL 及其 PRM **均被 Akamai 拦为 403 Access Denied（是 WAF 拦爬虫，不是 404，说明主机确实存在）**，且 Intuit 官方站找不到任何一页描述它。**反向证据**：help.developer.intuit.com 上 2026-05 仍有开发者在问「Is there a plan or timeline for when Intuit will provide a hosted QuickBooks MCP server」。<br>➡️ **验证动作**：(1) 从非数据中心 IP / 真实浏览器访问看是否返回 MCP 401；(2) 查 Claude/ChatGPT connector 目录是否有 Intuit 官方 connector 并抓其 endpoint；(3) 提 Intuit developer ticket 直接问 |
| **Square** | tier1 | ❓ 待验证 | ⚠️ 文档为 SSE-only | OAuth2 | ⚠️ 文档端点 `https://mcp.squareup.com/sse`；`/mcp` 存在但传输未确认 | [developer.squareup.com/docs/mcp](https://developer.squareup.com/docs/mcp) | **【复核推翻 —— 正是本文档警告的失败模式】** 原判 REMOTE_MCP 完全建立在一个**未文档化路径的匿名 401 探测**上。<br>**Square 官方文档明确是 SSE-only**：「The Square Remote MCP server is hosted at:」后面的代码块**只有** `https://mcp.squareup.com/sse`；该 URL 全页出现 12 次，每个客户端配方都用 `npx mcp-remote` 包一层（8 次）；**`/mcp` 字样与「Streamable HTTP」字样全页零出现**。<br>**为何无法用探测定论**：认证网关在传输层之前，`/mcp` 与 `/sse` 行为完全无法区分 —— POST/GET/OPTIONS 在两条路径上返回完全相同的形状（401 只差 resource_metadata 后缀，OPTIONS 都是 204）。**401 只能证明路由存在，不能证明它说 Streamable HTTP。**<br>**复核新发现的阻塞**：Square 的 DCR 端点**强制 redirect-URI 白名单** —— 注册返回 `400 {"error":"invalid_redirect_uri","error_description":"Invalid redirect URI: ... domain not in allowlist"}`，**和 Ramp 一样需要商务/工单步骤**。加上仍是 Beta，不应按 400 行排期 |
| **Xero** | tier1 | ❓ 待验证 | Streamable HTTP（推断） | OAuth2 | `https://mcp.xero.com/mcp` | ⚠️ 无官方文档记载该端点 | **【复核推翻 —— 卡在证据规则】** 原引用的「官方依据」是 `https://mcp.xero.com/` 本身，**那是服务自己的落地页，不是文档**，不满足「必须提供厂商官方文档 URL」。<br>**负面发现是彻底的**：`developer.xero.com/ai` 提到 MCP 25 次，**唯一给出的 URL 是本地 npx/stdio 包 `github.com/XeroAPI/xero-mcp-server`**，`mcp.xero.com` 零命中；`central.xero.com` 的 Xero MCP 文章、`developer.xero.com/documentation/ai/mcp`、`/documentation/mcp` 同样零命中；`llms.txt` 404。<br>**端点本身是真的**（`<title>Xero MCP Server</title>`，PRM `authorization_servers=["https://identity.xero.com/"]`，`/sse` 返回 404 —— 这点利好 Streamable HTTP），**但我们能不能用是未证实的**：`mcp.xero.com` **不提供 AS metadata（404）、不提供 DCR（`/register` 404）**，而 `identity.xero.com` 的 metadata **没有 `registration_endpoint`**，即需手工注册 Xero app，且**没有任何文档说明这样的 app 如何签发 `mcp.xero.com` 会接受的 audience token**。<br>⚠️ **另：scopes 基本只读**（`accounting.settings` + invoices/balancesheet/P&L/aged reports 的 `.read`），**任何写入/开发票场景都必须走 Managed REST** |
| **Adyen** | tier2 | 🔧 Managed REST | stdio only | API key | — | [docs.adyen.com](https://docs.adyen.com/development-resources/mcp-server) | **已核实没有官方Remote MCP，不是「没搜到」**：官方文档、官方发布公告、官方仓库全部只描述本地运行 —— 原文「**Run our MCP server locally** to start interacting with our APIs」，`npx -y @adyen/mcp --adyenApiKey=... --env=TEST`，stdio。仍是 **alpha**，只覆盖 Checkout + Management API 一小部分。DNS 层确认 `mcp.adyen.com` 不存在。<br>➡️ Adyen REST API，⚠️ **`X-API-Key` header（不是 `Authorization: Bearer`）** —— auth binding 要支持自定义 header 名 |
| **Coinbase** | tier2 | 🔧 Managed REST | stdio（可执行版） | API key | — | [docs.cdp.coinbase.com](https://docs.cdp.coinbase.com/get-started/build-with-ai/docs-for-ai/cdp-docs-mcp) | **典型的「有官方 MCP 但不是我们要的那个」。** CDP 确有**远程**托管 MCP `https://docs.cdp.coinbase.com/mcp`，但官方白纸黑字「provides documentation **search only**. It **does not execute API calls**」。真正能操作钱包/交易/转账的是 **CDP CLI 的本地 MCP**（`claude mcp add --transport stdio cdp -- cdp mcp`，凭据存本机 keyring）——出局。`mcp.coinbase.com` 不存在，`api.coinbase.com/mcp` 404。<br>➡️ CDP API ⚠️ **JWT/Ed25519 签名，每个请求要现签，不是静态 Bearer，会让 auth binding 复杂化**；或 Coinbase Commerce（`X-CC-Api-Key`）/ Advanced Trade。**选哪个子产品要先定业务场景** |
| **BILL**（Bill.com） | tier2 | 🔧 Managed REST | 无 | API key | — | [developer.bill.com](https://developer.bill.com/docs/home) | **已核实无官方 MCP**：查了 v3 平台首页与 **`developer.bill.com/llms.txt`（他们专门为 AI agent 提供的全站索引）—— 全文 grep `mcp` 命中 0 次**。`mcp.bill.com` 不存在，`api.bill.com/mcp` 404。市面上的全是社区实现。<br>➡️ BILL v3 API（AP/AR）+ Spend & Expense API。⚠️ 鉴权是 sessionId/devKey 体系，**AP/AR 侧的 login→sessionId 模式对我们的 credential 模型不友好，要注意 session 续期**。新集成用 v3 |

### 3.6 云 / 基础设施

| Provider | 常见度 | 判定 | 传输 | 认证 | MCP Endpoint | 官方依据 | 备注 |
|---|---|---|---|---|---|---|---|
| **AWS** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth 2.1 + PKCE / client_credentials | `https://aws-mcp.us-east-1.api.aws/mcp` | [docs.aws.amazon.com](https://docs.aws.amazon.com/agent-toolkit/latest/userguide/oauth-authentication.html) | **重大更正训练知识**：`awslabs/mcp` 那些全是本地 stdio 包，但 **AWS MCP Server 已于 2026-05-06 GA**，AWS 自己托管，覆盖 15000+ API operation。实测 initialize 返回 200 + `mcp-session-id`。<br>认证两条路：OAuth 2.1 authorization code + PKCE；或 `aws signin create-oauth2-token-with-iam --grant-type client_credentials`，返回 `{"tokenType":"Bearer"}` —— **完美匹配 `scheme=bearer`**。<br>⚠️ **坑**：access token 仅 1 小时，client_credentials 流**不发 refresh token**（authorization_code 的 refresh token 12 小时），**刷新逻辑要自己写**；调用方 IAM 需 `signin:AuthorizeOAuth2Access` + `signin:CreateOAuth2Token`；OAuth 模式不支持跨账号 multi-profile 切换。Region：us-east-1 / eu-central-1。<br>💡 另有零认证只读的 AWS Knowledge MCP `https://knowledge-mcp.global.api.aws/mcp`，可作附赠工具 |
| **Google Cloud** | tier1 | ✅ Remote MCP | Streamable HTTP | Google OAuth2 / API key | `https://<SERVICE>.googleapis.com/mcp`（如 `run.googleapis.com/mcp`） | [docs.cloud.google.com](https://docs.cloud.google.com/mcp/supported-products) | Google 把 Remote MCP 直接挂在 googleapis.com 上，**50+ 产品各有独立 endpoint**（BigQuery / Cloud SQL / Spanner / AlloyDB / GKE / Compute / GCS / Pub-Sub / Logging / Monitoring…）。<br>💡 **最省事的一条**：官方明写「You can use an OAuth 2.0 Authorization header to authenticate with an OAuth 2.0 bearer token or an API key」，**且沿用普通 Google OAuth scope，现有 Google connector 凭证可直接复用**。<br>⚠️ **坑**：(1) **没有统一聚合 endpoint**，connector 要按产品拆或做成可配置 service host；(2) 部分 regional endpoint 仍是 Preview；(3) Cloud Run MCP 明确不接受 API key，只接受 OAuth |
| **Cloudflare** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 / API token | `https://mcp.cloudflare.com/mcp` | [developers.cloudflare.com](https://developers.cloudflare.com/agents/model-context-protocol/cloudflare/servers-for-cloudflare/) | **与「开发工具」类别是同一条，两轮独立调研结论一致。** 统一 endpoint 覆盖 2500+ API endpoint；另有 16 个产品专用 server（`docs.` / `bindings.` / `observability.` 等）。标准 MCP Authorization 发现流程齐全，无付费套餐门槛 |
| **DigitalOcean** | tier2 | ✅ Remote MCP | Streamable HTTP | **Bearer PAT** | `https://<service>.mcp.digitalocean.com/mcp`（如 `apps.` / `droplets.` / `databases.` / `doks.`） | [docs.digitalocean.com](https://docs.digitalocean.com/reference/mcp/configure-mcp/) | 官方给了 20+ 个 service 级 endpoint 表。💡 **认证最简单**：直接 `Authorization: Bearer <DO API token>`，**不需要 OAuth 授权码流** —— 对 MCPAuthBinding 是最低摩擦的形态。<br>⚠️ **没有聚合 endpoint**（`https://mcp.digitalocean.com/mcp` 实测无法连接），**connector 必须按 service 维度拆，或把 subdomain 做成配置项** |
| **Supabase** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth2 + DCR / PAT | `https://mcp.supabase.com/mcp` | [supabase.com/docs](https://supabase.com/docs/guides/getting-started/mcp) | 20+ 工具（建表、查询、项目管理）。**认证演进要注意**：早期必须 PAT，现在默认 OAuth2 + DCR；对不支持 DCR 的客户端或 CI 场景，仍可手动建 OAuth app 或直接用 PAT 当 bearer —— **两条路都落在 `scheme=bearer`**。self-hosted 与 CLI 本地开发（`http://localhost:54321/mcp`）都存在，**endpoint 应做成可配置** |
| **Alibaba Cloud 阿里云** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth2 + **DCR** | `https://openapi-mcp.cn-hangzhou.aliyuncs.com/mcp` | [help.aliyun.com](https://help.aliyun.com/zh/openapi/user-guide/openapi-mcp-server-guide) | **【复核修正 endpoint + 大幅提升置信度】** 原判「endpoint 是每账号在控制台生成的长路径 `/accounts/{uid}/custom/{name}/id/{id}/mcp`、无法硬编码、置信度 low」**前提被实测推翻**：**固定基础路径 `/mcp` 本身就是活的公共端点**。<br>**证据链**：无 Authorization 头 → `401 {"error":"Authorization header is missing"}`；带 `Bearer dummy` → **`403 {"error":"Access token type is not match"}`**（状态码与错误语义都变了，**证明服务端确实解析并校验 Bearer 头** —— 直接回答了原调研悬而未决的问题）；PRM `bearer_methods_supported:["header"]`；AS metadata 完整：`authorization_endpoint=https://signin.aliyun.com/oauth2/v1/auth`、`token_endpoint=https://oauth.aliyun.com/v1/token`、**`registration_endpoint=.../register`（支持 DCR）**、PKCE S256、public client。<br>➡️ **我们不需要原调研担心的本地 shim（`npx mcp-remote-alibaba-cloud`）** —— 那只是给不支持 MCP Authorization 规范的旧客户端的兼容层。Custom 版长路径可作为可选覆盖项保留。**残留待验**：拿真 token 后完成 initialize/tools/list 握手 |
| **Microsoft Azure** | tier1 | 🔧 Managed REST | stdio only | Entra OAuth2 | — | [learn.microsoft.com](https://learn.microsoft.com/en-us/azure/developer/azure-mcp-server/how-to/deploy-remote-mcp-server-microsoft-foundry) | **已确认微软没有托管 Azure 平台侧的 Remote MCP。** Azure MCP Server 是本地 stdio 包；2.0 版本新增的所谓 remote 支持是「**self-hosted** remote」—— 官方 how-to 全程在教你用 azd 部署到**自己的** Azure Container Apps，输出 `https://<你的 app>.azurecontainerapps.io`，**没有任何微软运营的公共 endpoint**。<br>➡️ Azure Resource Manager REST API（`management.azure.com`），Entra OAuth2 + `Authorization: Bearer`，**认证模型完全兼容**。<br>⚠️ **别混淆**：Azure **DevOps** 有微软托管的 Remote MCP（见 3.1），但那是 DevOps 不是 Azure 云资源，且本身已被复核降级为 ❓ |
| **Docker Hub** | tier1 | 🔧 Managed REST | stdio only | Bearer PAT | — | [docs.docker.com](https://docs.docker.com/ai/mcp-catalog-and-toolkit/hub-mcp/) | **已确认 Docker 没有托管 Docker Hub MCP**：官方文档三种安装方式全是本地执行（`"command": "node"`），凭证走环境变量 `HUB_PAT_TOKEN` 而非 HTTP header。探测 `https://mcp.docker.com/mcp` 404、`https://hub.docker.com/mcp` 405（是 Catalog 网页不是协议端点）。<br>⚠️ **别被 Docker MCP Catalog / MCP Gateway 误导** —— 那是 Docker 帮**别人**分发 MCP server 的市场和本地网关，不等于 Docker Hub 自己有 Remote MCP。<br>➡️ Docker Hub API v2，PAT 换 JWT 后 `Authorization: Bearer` |
| **Kubernetes** | tier1 | 🔧 Managed REST | stdio only | Bearer（ServiceAccount token） | — | [kubernetes.io](https://kubernetes.io/docs/reference/kubernetes-api/) | **Kubernetes 是开源项目不是 SaaS 厂商，结构上不可能有「厂商托管的 Remote MCP」** —— 没有中心化服务可托管。现存实现全是社区/第三方本地 server。<br>技术上 Managed 可行（API Server 本身就是 REST，原生支持 `Authorization: Bearer <ServiceAccount token>`），但**两个结构性问题**：(1) endpoint 是用户自己集群的地址，**通常在私网/VPC 内不可公网直达**，需用户额外暴露或走隧道；(2) 这更像「instance 级连接器」而非 SaaS 连接器，**与本项目 connection-as-handle 模型契合度低**。<br>➡️ **建议优先做托管 K8s 的云厂商入口替代**（GKE MCP `https://container.googleapis.com/mcp`、DO 的 `https://doks.mcp.digitalocean.com/mcp` 都已是 Remote MCP），裸 Kubernetes 排后面 |
| **HashiCorp Terraform** | tier2 | ❓ 待验证 | Streamable HTTP（实测行为一致） | Bearer（HCP Terraform token） | ⚠️ `https://mcp.terraform.io/mcp`（活的，但官方文档未记载） | [developer.hashicorp.com](https://developer.hashicorp.com/terraform/mcp-server/deploy) | **全表唯一的矛盾证据条目，请勿据此排期。**<br>**文档侧**：官方部署页只列 local 和 **self-hosted** remote 两种，明确写「We recommend running the MCP server locally at 127.0.0.1」，并要求自己做 TLS / API gateway / IP 白名单 —— 按文档应判 Managed REST。<br>**实测侧**：该端点是活的且行为完全像 HashiCorp 托管实例 —— 无 token → `401 "Authorization bearer token is required"`；带 `Bearer dummy` → `401 "Terraform token is unauthorized"`（**说明它在真的校验 HCP Terraform token**）；`/.well-known/oauth-protected-resource` **303 重定向到 `developer.hashicorp.com/terraform/mcp-server`**（证明域名和服务确属 HashiCorp）。<br>➡️ **极可能是未公开/preview 的官方托管实例，但零厂商文档背书、无 SLA、无变更通知。** 下一步：拿真实 HCP Terraform API token 打一次 initialize + tools/list，并**向 HashiCorp 确认该端点是否受支持**。确认则升级 ✅；否则走 Terraform Registry API + HCP Terraform API（都支持 Bearer `TFE_TOKEN`）。Terraform Enterprise 是 self-hosted，endpoint 无论如何都要从配置读 |

### 3.7 数据 / 分析

| Provider | 常见度 | 判定 | 传输 | 认证 | MCP Endpoint | 官方依据 | 备注 |
|---|---|---|---|---|---|---|---|
| **Google BigQuery** | tier1 | ✅ Remote MCP | Streamable HTTP | Google OAuth2 / IAM | `https://bigquery.googleapis.com/mcp` | [docs.cloud.google.com](https://docs.cloud.google.com/bigquery/docs/use-bigquery-mcp) | 实测 initialize 返回 200 + JSON-RPC（serverInfo `StatelessServer/ESF`），`tools/list` 可匿名列出（`list_datasets` / `list_tables` / `get_table` / `execute_sql`），真正调用需 token。PRM `authorization_servers=[accounts.google.com]`，scope `.../auth/bigquery`。**固定全局 endpoint，无需从配置读**。文档明确**不接受 API key**，只能 OAuth2/IAM |
| **Snowflake** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 / **Bearer PAT** | `https://<org>-<account>.snowflakecomputing.com/api/v2/databases/{db}/schemas/{schema}/mcp-servers/{name}` | [docs.snowflake.com](https://docs.snowflake.com/en/user-guide/snowflake-cortex/cortex-agents-mcp) | 2025-11-04 GA。⚠️ **没有厂商统一 URL** —— endpoint 是账号级的，且**要客户先在自己账号里 `CREATE MCP SERVER` 对象**（需 ACCOUNTADMIN）。**account URL + db + schema + server name 四段全可变，必须从配置读。** 官方 quickstart 的 curl 示例即 `Bearer <PAT>`。因是账号内对象、无公开实例，**无法远程探活**；判定依据是官方文档的 endpoint 格式 + Bearer 示例。别与本地 `Snowflake-Labs/mcp` uvx 包混淆 |
| **Databricks** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth (U2M/M2M) / **Bearer PAT** | `https://<workspace-host>/api/2.0/mcp/{genie\|sql\|functions/{catalog}/{schema}\|ai-search/...}` | [docs.databricks.com](https://docs.databricks.com/aws/en/generative-ai/mcp/connect-clients) | 官方明写「the client calls tools over **Streamable HTTP**」，客户端配置 `type=streamable-http`。⚠️ **endpoint 是 workspace 级**（`.cloud.databricks.com` / `.azuredatabricks.net` / GCP 域名各不同），**必须从配置读**；且**不同能力是不同子路径**（Genie 空间 / UC functions / SQL），一个 connection 可能要绑多个 endpoint 或让用户选。⚠️ **Databricks SQL 工具是异步的（调用后要轮询）**。无公开实例，未能远程探活 |
| **Tableau** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth 2.1 | `https://mcp.tableau.com`（**根路径**） | [tableau.github.io/tableau-mcp](https://tableau.github.io/tableau-mcp/docs/hosted-tableau-mcp) | 💡 **本类别最省事的之一**：单一 URL 通过 **pod-aware routing**（解析 OAuth token）自动路由到用户所在 Tableau Cloud pod —— **可以硬编码一个固定 endpoint**。⚠️ **路径坑：MCP 挂在根路径，加 `/mcp` 会 404**。scopes 如 `tableau:mcp:datasource:read` / `workbook:read` / `view:read` / `pulse:read` / `insight:create`。<br>⚠️ **限制**：仅 Tableau **Cloud**（任意 SKU），Tableau Server 客户必须自建、不在此路径内；Pulse Insight Briefs 需 Tableau+，完整 Metadata API 需 Data Management |
| **Microsoft Power BI / Fabric** | tier1 | ✅ Remote MCP | Streamable HTTP（`type:"http"`） | Entra OAuth | `https://api.fabric.microsoft.com/v1/mcp/powerbi` | [learn.microsoft.com](https://learn.microsoft.com/en-us/power-bi/developer/mcp/remote-mcp-server-get-started) | **固定全局 endpoint，无需按租户配置。**<br>⚠️ **五个限制**：(1) Preview，官方明示 tool 定义/请求格式/响应 schema 可能变；(2) **租户管理员必须开启「Users can use the Power BI Model Context Protocol server endpoint (preview)」，否则全租户不可用** —— 会成为开通阻塞点；(3) Generate Query 工具需 Copilot license（消耗 Copilot 容量），可禁用该工具改由自家 LLM 生成 DAX；(4) **Service Principal 认证下 RLS 不生效，安全上必须走用户 OAuth**；(5) 只有 4 个工具，覆盖面窄；工作区管理等能力要另看 Fabric Core MCP（同域其他 `/v1/mcp/*` 路径，也在 preview） |
| **Mixpanel** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth + PKCE / **Service Account bearer** | US `https://mcp.mixpanel.com/mcp`<br>EU `https://mcp-eu.mixpanel.com/mcp`<br>IN `https://mcp-in.mixpanel.com/mcp` | [docs.mixpanel.com/docs/mcp](https://docs.mixpanel.com/docs/mcp) | 官方明写 streamable HTTP，且提示客户端会自动 DCR 重新注册。💡 **Service Account 静态 bearer（base64 凭据）适合无浏览器场景 —— 对机器侧接入很友好**。**区域 endpoint 按 project 数据驻留选，做成配置项。**<br>⚠️ **组织管理员必须先在账号设置里开启 MCP（生效最多 15 分钟）**；限流 600 req/hour/用户。70+ 工具（insights / funnels / dashboards / Lexicon / cohorts / replay） |
| **Amplitude** | tier2 | ✅ Remote MCP | Streamable HTTP | **仅 OAuth 2.0** | `https://mcp.amplitude.com/mcp`（EU `https://mcp.eu.amplitude.com/mcp`） | [amplitude.com/docs](https://amplitude.com/docs/amplitude-ai/amplitude-mcp/other-clients) | 官方写「Streaming HTTP (remote) transport」，并明确「clients that only support stdio-based local servers aren't compatible」。<br>⚠️ **坑：只支持 OAuth 2.0** —— 官方原文「Clients that support only API key authentication or no authentication **can't connect**」，**不能用 Amplitude API key 走 bearer，必须实现完整 OAuth 流**。<br>⚠️ 另有一个免鉴权的**文档站 MCP**（`https://amplitude.com/docs/api/mcp`），**别当成数据 MCP** |
| **PostHog** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth / **Personal API Key** | `https://mcp.posthog.com/mcp` | [posthog.com/docs](https://posthog.com/docs/model-context-protocol/claude-code) | 💡 **对本项目最省事的一条**：官方仓库配置示例即 `POSTHOG_AUTH_HEADER="Bearer <personal_api_key>"`。官方接法 `claude mcp add --transport http`。**免费**；US/EU region 由 PostHog 认证服务器**按账号自动路由，无需分别配置 endpoint**。能力：HogQL 查询、insights、feature flags、experiments、error tracking。早期 SSE 端点已被 `/mcp` 取代 |
| **Metabase** | tier2 | ✅ Remote MCP | Streamable HTTP（`--transport http`） | 内建 OAuth 2.0 | `https://{metabase_host}/api/metabase-mcp` | [metabase.com/docs](https://www.metabase.com/docs/latest/ai/mcp) | 认证是 Metabase 自带的**嵌入式 OAuth 2.0 服务器（不需要外部 IdP）**，token 继承登录用户的 Metabase 权限。<br>⚠️ **三个坑**：(1) **endpoint 是 instance 级** —— Cloud 每租户一个域名，self-hosted 更是任意域名，**必须从配置读**；(2) **要求实例开启 AI features 开关**（Admin settings），未开则不可用；(3) **第三方资料里流传的 `/api/mcp` 路径与官方文档的 `/api/metabase-mcp` 不一致** —— 以官方为准并做一次探测。无公开实例，未能远程探活 |
| **Looker** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth 2.1 | `https://{LOOKER_INSTANCE_URL}/mcp` | [docs.cloud.google.com/looker](https://docs.cloud.google.com/looker/docs/mcp) | 官方接法 `gemini mcp add --transport http looker LOOKER_INSTANCE_URL/mcp`。agent 继承授权用户的 Looker 角色与内容权限。<br>⚠️ **五个坑**：(1) 目前 **Preview**（"available as is"）；(2) endpoint 是 instance 级，必须从配置读；(3) **不支持 customer-hosted / on-prem Looker**；(4) **管理员必须先把 AI agent 注册成 OAuth client，并逐个显式启用工具** —— 有人工配置门槛，影响自助开通体验；(5) 不额外收费但消耗实例 API 配额。别与 MCP Toolbox for Databases 的 Looker source 混淆 |
| **dbt**（dbt platform / Cloud） | tier2 | ✅ Remote MCP<br>（⚠️ 框架阻塞） | Streamable HTTP | `Authorization: Token/Bearer <PAT>` | `https://cloud.getdbt.com/api/ai/v1/mcp/`（多 cell 账号为 `<prefix>.us1.dbt.com`） | [docs.getdbt.com](https://docs.getdbt.com/docs/dbt-ai/setup-remote-mcp) | dbt Labs 官方博客确认服务端跑 Streamable HTTP。PRM scopes：`offline_access, account:read, projects:query, catalog:read, projects:develop, jobs:run`。<br>🚨 **最大的坑（框架阻塞）**：除 Authorization 外**强制要求自定义 header `x-dbt-prod-environment-id`**（数值 ID）；用 `execute_sql` 还需 `x-dbt-dev-environment-id` 和 `x-dbt-user-id`（**且必须用 PAT，service token 不行**）。**MCPAuthBinding 目前只表达 `scheme=bearer` 的 Authorization —— 接入前必须确认转发层能否携带这些静态自定义 header，否则这条会退化成需要改框架。** 见 [5.3 节](#53-两个框架改动能翻盘一批条目)<br>⚠️ endpoint 要从配置读（账号设置 → Access URLs → MCP Endpoint URL）；需 Starter 及以上套餐并开启 AI features |
| **ClickHouse Cloud** | tier3 | ✅ Remote MCP | Streamable HTTP（`--transport http`） | ClickHouse Cloud OAuth | `https://mcp.clickhouse.cloud/mcp` | [clickhouse.com/docs](https://clickhouse.com/docs/use-cases/AI/MCP/remote_mcp) | **固定全局 endpoint。** OAuth 可对接客户 IdP。<br>⚠️ **五个限制**：(1) **仅 ClickHouse Cloud**，自托管不适用（只有本地 stdio）；(2) 需账号里有 running 的 Cloud service，并在控制台**启用 Remote MCP**；(3) **所有 SQL 强制 `readonly=1`，只读，不能写入**；(4) 能力较窄（列库/列表/看 schema/跑 SELECT）；(5) 官方博客曾标 beta/private preview，**文档页未标注 GA 状态，实现前再确认一次** |
| **Google Analytics 4** | tier1 | 🔧 Managed REST | — | Google OAuth2 | **（刻意留空）** | [developers.google.com](https://developers.google.com/analytics/devguides/MCP) | **【复核维持 Managed REST，并清空 endpoint】**<br>**为何 Managed REST**：官方文档页只指向 `github.com/googleanalytics/google-analytics-mcp` —— **本地 stdio 包，页面里没有任何远程 HTTPS endpoint**。**核心报表能力所在的 Data API 主机确认无 MCP**：`analyticsdata.googleapis.com/mcp` 与 `/v1beta/mcp` 实测均 404（Google 通用 404 HTML，说明路由根本不存在）。**`runReport` / `runRealtimeReport` / `batchRunReports` 拿不到，GA 的主要价值必须走 Managed REST。**<br>**为何清空 endpoint**：原记录在一条 Managed 判定上填了 `https://analyticsadmin.googleapis.com/mcp`。该端点确实活着（实测 200，12 个 Admin 只读工具），**但任何 Google 官方文档都没记载它，且它连 PRM 都没有**（`/.well-known/oauth-protected-resource/mcp` 实测 404，对比 BigQuery 同路径正常返回 JSON）—— **没有走标准 MCP OAuth 发现流程，属未公开的内部端点**。写进记录会误导实现方去依赖一个随时可能消失、无 SLA 的 URL。<br>💡 保留为线索：**若只做元数据发现，可额外挂 analyticsadmin 的 MCP 省一部分工作量。** GA 不在 GCP 托管 MCP 支持产品清单里 |

### 3.8 项目管理 / 生产力

| Provider | 常见度 | 判定 | 传输 | 认证 | MCP Endpoint | 官方依据 | 备注 |
|---|---|---|---|---|---|---|---|
| **Asana** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp.asana.com/v2/mcp` | [developers.asana.com](https://developers.asana.com/docs/using-asanas-mcp-server) | V2 于 2026-02 GA，42 工具。<br>🪤 **陷阱：旧 V1 SSE 端点 `https://mcp.asana.com/sse` 已于 2026-05-11 关停 —— 绝对不要接。**<br>⚠️ **集成约束**：**无 DCR** —— 须手工在 `app.asana.com/0/my-apps` 创建类型为 "MCP app" 的应用拿 client_id/secret，authorize 于 `app.asana.com/-/oauth_authorize`，且**必须完全省略 `scope` 参数**（MCP app 不接受 scope）。<br>⚠️ **发给 MCP app 的 token 只能用于 MCP server，不能用于常规 Asana REST API** —— 若日后需要 REST 兜底，得再建第二个 OAuth app。Enterprise+ 管理员可 allow/block 特定 MCP client，企业租户可能需要把我们加白名单 |
| **Atlassian**（Jira / Confluence / JSM / Bitbucket / Compass） | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth 2.1 / API token | `https://mcp.atlassian.com/v1/mcp/authv2` | [support.atlassian.com](https://support.atlassian.com/atlassian-rovo-mcp-server/docs/getting-started-with-the-atlassian-remote-mcp-server/) | **与 3.1 的 Jira、3.3 的 Confluence 是同一台 server。** 品牌名 "Atlassian Rovo MCP Server"，2026-02-04 GA，由 AtlassianEdge 提供。官方文档用 `--transport http`。<br>🪤 **两个陷阱**：(1) 旧 `https://mcp.atlassian.com/v1/sse` **2026-06-30 后停止支持**，SSE-only 路径已死，用 `authv2`；(2) **Cloud only** —— Jira/Confluence Data Center & Server 无法使用且**无公布时间表**，自建客户只能走 Managed REST |
| **Trello** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp.trello.com/v1`（**裸 `/v1`，无 `/mcp` 后缀**） | [support.atlassian.com/trello](https://support.atlassian.com/trello/docs/connect-trello-to-ai-assistants-with-trello-mcp/) | **与 Atlassian/Rovo server 是分开的两台** —— Trello 于 ~2026-07-22 拿到自己的官方远程 server（**非常新**）。响应暴露 `Mcp-Protocol-Version` / `Mcp-Session-Id` CORS 头 = **货真价实的 Streamable HTTP**。scopes：`read:board:trello` / `write:board:trello` / `read:organization:trello` / `write:organization:trello` / `offline_access`。所有 Trello 套餐可用；OAuth 是 workspace 范围；org 管理员可在 Atlassian Administration 限制读/写/搜索。<br>⚠️ **工具仍很早期**（boards / lists / cards / checklists / search）—— 若需要 label / power-up / attachment 深度，仍要 REST 补充 |
| **Linear** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth 2.1 + DCR / API key | `https://mcp.linear.app/mcp` | [linear.app/docs/mcp](https://linear.app/docs/mcp) | **与 3.1 是同一条。本仓库已按此实现（707 行 / 12 tool）。** 💡 认证极灵活：OAuth 2.1（**含 DCR**）或直接 `Bearer <Linear API key>` / personal token —— **API key 路径意味着可以支持无 OAuth 往返的无头连接**。只读变体 `https://mcp.linear.app/mcp/readonly`（只授 `read` scope 的 token 够不到写 API）—— **可用来提供 safe-mode 连接**。无套餐门槛 |
| **monday.com** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp.monday.com/mcp` | [support.monday.com](https://support.monday.com/hc/en-us/articles/28588158981266-Get-started-with-monday-MCP) | PRM 返回 `{"resource":"https://mcp.monday.com/mcp","authorization_servers":["https://mcp.monday.com"],"bearer_methods_supported":["header"]}`。**所有 monday 账号预装**；每个用户各自 OAuth 授权，**只继承自己的权限**。<br>🪤 **大陷阱**：monday 同时提供本地 stdio 包 `npx @mondaydotcomorg/monday-api-mcp -t <API_TOKEN>`，**且 developer.monday.com 大量篇幅在讲那个** —— 与我们无关，**只有托管的 `mcp.monday.com/mcp` 算数**。注意 support.monday.com 拦截服务端抓取（403），验证依赖端点实测 + 官方 GitHub/dev 文档 |
| **ClickUp** | tier1 | ✅ Remote MCP | Streamable HTTP | **仅 OAuth 2.1 + PKCE** | `https://mcp.clickup.com/mcp` | [developer.clickup.com](https://developer.clickup.com/docs/connect-an-ai-assistant-to-clickups-mcp-server) | PRM 显示 `resource_owner=ClickUp`、`resource_server_origin=https://api.clickup.com` —— 第一方无疑。覆盖 tasks / docs / chat。<br>⚠️ **四个坑**：(1) **仅 OAuth** —— 官方明写「you cannot authenticate using your own API keys or Auth access tokens. We only support OAuth」，**没有 PAT 捷径**；(2) **客户端必须先被 ClickUp 审核并加白名单**才能连接，**要预留厂商审批周期**；(3) 仍是 **PUBLIC BETA**；(4) **硬限流**：Free Forever **50 次 MCP 调用 / 24h**，Unlimited+ **300 次 / 24h**（除非购买 "Everything AI" 加购）—— **可用，但不能拿来跑高频 agent 负载** |
| **Airtable** | tier1 | ✅ Remote MCP | Streamable HTTP（`--transport http`） | OAuth / **PAT bearer** | `https://mcp.airtable.com/mcp` | [support.airtable.com](https://support.airtable.com/docs/using-the-airtable-mcp-server) | 💡 **本类别与我们 binding 最契合的一条**：官方同时记载浏览器 OAuth **与** `Authorization: Bearer <pat>` —— **PAT 路径意味着完全无头连接、零 OAuth 流程**。文档指定 transport `"http"` 而非 SSE。**所有套餐可用。** 工具：搜索 bases、列/看 schema、增改记录、`filterByFormula` + sort。<br>⚠️ Airtable 另有官方 CLI（`Airtable/airtable-mcp-cli`），**那是本地便利封装，不是我们要集成的东西**。端点出网被设计为限制在 `*.airtable.com` |
| **Notion** ★ | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp.notion.com/mcp` | [developers.notion.com](https://developers.notion.com/docs/get-started-with-mcp) | **与 3.3 是同一条 —— 两轮独立调研都确认了它，本仓库已于 2026-07-25 按此实现（414 行 / 10 tool），见 [第 4.1 节](#41-头号案例notion已改造完成)。** 官方给 `/mcp` 为 Streamable HTTP 推荐端点，`/sse` 仅作 legacy 客户端兜底。**注意：「不支持 bearer token」指的是不接受 internal integration token / API secret，OAuth access token 仍以 `Authorization: Bearer` 提交，我们的 binding 没问题** —— 但要为每个 workspace 规划交互式 OAuth，**做不了纯机器无人值守连接** |
| **Smartsheet** | tier2 | ✅ Remote MCP | Streamable HTTP（`httpUrl`） | **API token bearer** / OAuth | US `https://mcp.smartsheet.com`<br>EU `https://mcp.smartsheet.eu`<br>AU `https://mcp.smartsheet.au` | [developers.smartsheet.com](https://developers.smartsheet.com/api/smartsheet/ai-integration/mcp-server) | 已 GA，Smartsheet 自建在 AWS 上。安装文档给出 `httpUrl` 配置（**Streamable HTTP，非 SSE**）与两种认证：`Authorization: Bearer ${SMARTSHEET_API_TOKEN}`（**对无头友好**）或 OAuth。**区域 endpoint 必须配置驱动。**<br>⚠️ 实测 `https://mcp.smartsheet.com/` 与 `/v1/mcp` 都返回 401（server: halo）**但 body 为空、无 `WWW-Authenticate` 头**，**确切路径后缀无法从外部 100% 确定 —— 实现时以安装文档的配置片段为准**。<br>⚠️ **套餐门槛：需 Business / Enterprise / Advanced Work Management**；仅商业 US/EU/AU 区域（无 gov cloud） |
| **Wrike** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth 2.0 / **Permanent Access Token** | `https://mcp.wrike.com/app/mcp/stream` | [developers.wrike.com](https://developers.wrike.com/docs/wrike-mcp-server-overview) | 🪤 **经典的「SSE 先行、后加 Streamable」案例** —— **多数第三方文章只提 SSE URL（`/app/mcp/sse`），照着看会错误否决它**。厂商开发者文档同时列出两者，我们只实现 stream。实测 `/app/mcp/stream` 返回 401（Cloudflare），而 `/mcp` 返回 404，**确认了确切路径**。<br>💡 Permanent Access Token **被官方明确定位为给无人值守的自动化/云端 agent 用**，正合我们。<br>⚠️ server 跑在 Wrike 自有基础设施上，**客户防火墙/IP 白名单规则会生效**；EU 数据中心租户可能需要区域 host，**endpoint 按可配置处理** |
| **Todoist** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth（无 PAT） | `https://ai.todoist.net/mcp` | [todoist.com/help](https://www.todoist.com/help/articles/use-chatgpt-with-todoist-mcp-WEeLx9d8h) | ⚠️ **注意域名非常规：`ai.todoist.net`，不是 `mcp.todoist.com`。** 官方帮助文章明写「hosted using **Streamable HTTP**, so you don't need to run anything locally」，接法 `claude mcp add --transport http`。**所有 Todoist 套餐可用**（Beginner/Pro/Business）。<br>⚠️ 托管版**没有记载 PAT 路径**，只能走 OAuth。🪤 Doist 另维护本地 stdio 包（`Doist/todoist-mcp`、`Doist/todoist-ai`）—— **不同产物，忽略** |
| **Shortcut** | tier3 | ✅ Remote MCP | Streamable HTTP | OAuth | `https://mcp.shortcut.com/mcp` | [shortcut.com/help](https://www.shortcut.com/help/integrations/mcp-server/) | 第一方（仓库在 Shortcut 自己的 org 下）。帮助中心原文：「Connect to Shortcut's hosted MCP server at `https://mcp.shortcut.com/mcp`. No API token or local setup required — authentication is handled via OAuth」；VS Code 配置用 `"type": "http"`。实测 401 带规范的 JSON-RPC error body、`www-authenticate: Bearer`，CORS 暴露 `Mcp-Session-Id` 且允许 GET/POST/DELETE —— **教科书式的 Streamable HTTP**。受众小众（研发团队工单），优先级低，但**在 Remote MCP 流水线建好后接入很便宜** |
| **飞书 / Lark** | tier2 | ❓ 待验证 | Streamable HTTP | ⚠️ **结论冲突** | `https://mcp.feishu.cn/mcp`（CN）/ `https://mcp.larksuite.com/mcp`（国际） | [open.feishu.cn](https://open.feishu.cn/document/mcp_open_tools/developers-call-remote-mcp-server) | **见 [4.3 节](#43-飞书--lark结论冲突未决)。** 本类别调研员判 Remote MCP（medium），实测到标准 Bearer + PRM，**且发现 IM read 工具确实存在**（搜群/搜消息/已读状态）——与「通讯」类别「完全不含 IM」的说法矛盾。<br>⚠️ **但同一位调研员指出：每个请求必须声明 `X-Lark-MCP-Allowed-Tools` 头** —— MCPAuthBinding 表达不了。<br>⚠️ 覆盖面窄：**没有多维表格(Base)/任务/日历 工具**，PM 场景仍需 REST。**endpoint 必须区域可配置** |
| **Basecamp** | tier3 | 🔧 Managed REST | 无 | OAuth2 | — | [basecamp/bc3-api](https://github.com/basecamp/bc3-api) | **确认不存在，非「未找到」**：查了 37signals 自己的 GitHub org、官方 BC3 API 仓库/文档（**通篇无 MCP 章节**，integrations 页列的是 Skills/CLI/SDK 类工具）、以及 dev.37signals.com。所有「Basecamp MCP server」命中都是社区实现，且**全部是本地 stdio 包、要求用户自己去 launchpad.37signals.com 注册 OAuth app** —— 同时不满足条件 1 和 2。<br>➡️ 兜底很扎实：`https://3.basecampapi.com/<account_id>/`，OAuth 2.0，文档完善，覆盖 projects/todos/messages/docs/card tables。**但 37signals 在采纳 AI 厂商协议上文化上很慢，别指望短期出 Remote MCP —— 有客户真的开口再做** |
| **Height** | tier3 | ⛔ Blocked | 无 | — | — | [HN 关停讨论](https://news.ycombinator.com/item?id=43454034) | **从路线图移除 —— 产品已经不存在了。** Height 于 2025-03 宣布日落、**2025-09-24 正式关停**，公司清算。实测 `https://mcp.height.app/mcp` 无响应（无 host）。**没有 MCP，也没有 API 可写 Managed connector，因为背后没有服务了。**<br>它出现在候选名单里**大概率是训练数据过时** —— 正好印证 [第 6 节](#6-维护机制这份表会过时) 的必要性。**建议把这个名额换成 Smartsheet 或 Wrike**，两者都活着且可走 Remote MCP |

### 3.9 客服 / 营销 / 邮件

| Provider | 常见度 | 判定 | 传输 | 认证 | MCP Endpoint | 官方依据 | 备注 |
|---|---|---|---|---|---|---|---|
| **Intercom** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth / Bearer token | US `https://mcp.intercom.com/mcp`<br>EU `https://mcp.eu.intercom.com/mcp` | [developers.intercom.com](https://developers.intercom.com/docs/guides/mcp) | **与 3.2 是同一条（两轮独立调研一致）。** 官方文档明写「Streamable HTTP (Recommended)」，SSE 标 legacy/deprecated。**双区域 endpoint 必须可配置**；AU 区域暂不支持 |
| **HubSpot** | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth 2.1 + PKCE | `https://mcp.hubspot.com`（**根路径**） | [developers.hubspot.com](https://developers.hubspot.com/docs/apps/developer-platform/build-apps/integrate-with-the-remote-hubspot-mcp-server) | **与 3.4 是同一条。** ⚠️ **根路径，`/mcp` 返回 404**。⚠️ **不支持 DCR，需手工创建 "MCP auth app"，要做进 onboarding 流程**。HubSpot 同时是 CRM 和 Marketing Hub，两个类别指向同一个 connector |
| **Klaviyo** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth（含 **DCR**） | `https://mcp.klaviyo.com/mcp` | [developers.klaviyo.com](https://developers.klaviyo.com/en/docs/klaviyo_mcp_server) | 同「电商」类别（两轮调研一致）。💡 **自带授权服务器 + DCR，接入成本极低**。可读写 campaigns / flows / profiles。<br>💡 **支持 query 参数收敛能力**：`read-only`、`core-tools-only`、`disable-tools-with-user-generated-content` —— **可作为我们暴露给 Agent 的 scope 收敛手段**。<br>⚠️ 授权用户必须是 Owner / Admin / Manager 角色。🪤 别与本地 `uvx klaviyo-mcp-server@latest`（私有 API key）混淆 |
| **Resend** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth / **API key bearer** | `https://mcp.resend.com/mcp` | [resend.com/docs](https://resend.com/docs/knowledge-base/mcp-server) | 💡 **本类别接入成本最低的一条。** 官方原文「Resend hosts the MCP server at: `https://mcp.resend.com/mcp`」，**无头场景可直接把 Resend API key 当 Bearer 传，官方明确支持**。<br>**证据很硬**：401 body 直接返回 `{"jsonrpc":"2.0","error":{"code":-32000,"message":"Unauthorized: provide credentials via Authorization: Bearer <token>"}}` —— 完美匹配 `scheme=bearer` + PAT。🪤 别用本地 npm 包 `resend-mcp` |
| **Customer.io** | tier2 | ✅ Remote MCP | Streamable HTTP（探测确认） | OAuth | US `https://mcp.customer.io/mcp`<br>EU `https://mcp-eu.customer.io/mcp` | [docs.customer.io](https://docs.customer.io/ai/mcp/get-started/) | 两个区域均已探测存活，**endpoint 必须按 workspace region 配置**。⚠️ 文档页**没有白纸黑字写 "Streamable HTTP"** —— 判定依据是实测 `content-type: application/json` + 标准 `WWW-Authenticate` + PRM（非 SSE-only 行为）。<br>⚠️ **工具设计是 API-passthrough 风格**（`cio_read_api` / `cio_write_api` / `cio_delete_api` / `cio_schema` / `cio_prime` / `cio_skills_*`），覆盖全量 API。**这类通用 passthrough 工具对 Agent 的可用性一般，可能需要我们在转发层做工具裁剪** |
| **Front** | tier2 | ✅ Remote MCP | Streamable HTTP | **仅 OAuth 2.1 confidential** | `https://mcp.frontapp.com/mcp` | [dev.frontapp.com](https://dev.frontapp.com/docs/mcp-server) | 官方明写 "Streamable HTTP"。<br>⚠️ **认证坑**：只接受 OAuth 2.1 **confidential client**（必须带 client_id + client_secret，PKCE 兼容），官方明确**不支持用 Front API token 直接做 Bearer** —— **必须先在 Front 注册 OAuth app，没有 PAT 捷径**。每个 connection 是 per-user token，agent 权限 == 授权 teammate 权限。当前 open beta，所有人可用、无套餐门槛，但官方提示仍在修 bug。<br>⚠️ **`dev.frontapp.com/docs/mcp` 是旧版已废弃页面** —— 以 `/docs/mcp-server` 为准 |
| **Brevo**（ex-Sendinblue） | tier2 | ✅ Remote MCP | Streamable HTTP | **Bearer API key** | `https://mcp.brevo.com/v1/brevo/mcp` | [developers.brevo.com](https://developers.brevo.com/docs/mcp-protocol) | 💡 **这条是搜索引擎完全搜不出来、靠端口探测 + `llms.txt` 挖到的** —— 记录方法本身有参考价值：`mcp.brevo.com` 的 AS metadata 完整（含 DCR，`registration_endpoint=/oauth/register`，PKCE S256），但常见路径（`/mcp`、`/sse`）**全是 404**；真正的端点藏在 `/v1/brevo/mcp`，从 `https://developers.brevo.com/llms.txt` 索引里才找到官方文档确认。<br>💡 官方 integration guide 配置即 `--transport http ... --header "Authorization: Bearer <mcp-token>"` —— **纯 Bearer API key，无需 OAuth 往返，接入成本极低**。token 在 Account > SMTP & API > API Keys 生成。<br>工具面很宽：拆成 27 个子 server（如 `/v1/brevo_contacts/mcp`），聚合 server 覆盖 contacts / email+SMS campaigns / CRM(deals,companies,tasks,pipelines) / WhatsApp / templates / lists / segments / senders / webhooks |
| **Mailchimp Transactional**（Mandrill） | tier3 | ✅ Remote MCP | Streamable HTTP（`type:"http"`） | **Bearer API key** | `https://mandrillapp.com/mcp` | [mailchimp.com/developer](https://mailchimp.com/developer/transactional/guides/how-to-use-mailchimps-transactional-messaging-mcp/) | 同「电商」类别。⚠️ **与 Mailchimp Marketing 是两个不同产品，别合并 —— 只有 Transactional 有官方托管 MCP。**<br>**证据双重确认**：官方配置 `{"type":"http","url":"https://mandrillapp.com/mcp","headers":{"Authorization":"Bearer YOUR_API_KEY"}}`；无凭证 POST 实测直接返回 200 + `{"jsonrpc":"2.0","error":{"code":-32001,"message":"Authorization: Bearer <api_key> required"}}`。<br>💡 **非 docs-only**：9 个工具含 `account_status` / `call_api` / `build_template` / `diagnose_failed_send`，**能真的发邮件和短信**。⚠️ 受限 API key 必须开启 'AI Agents' 权限组。tier3 是因为 Mandrill 是付费加购模块，用户基数远小于 Marketing |
| **Braze** | tier2 | 🔧 Managed REST | （Streamable HTTP 已确认） | OAuth bearer | ~~`https://mcp.braze.com/mcp`~~ | [braze.com/docs](https://www.braze.com/docs/user_guide/brazeai/mcp_server/setup) | **【复核推翻】** 技术事实全对（US/EU 401 与 PRM 都已复现，确是厂商托管 Streamable HTTP + OAuth bearer），**但由此得出的判定对本项目是错的**，两条独立理由 —— 原报告把它们列为「风险」却没让它们改变结论：<br>(1) **可用性**：官方 important 提示框写「The Remote MCP server is in **Early Access**. **Contact your account manager to request access.**」—— 这是 per-customer、非自助的商务门槛，**我们无法为任意 Braze 租户开通**；且**启用了 IP allowlisting 的客户被直接排除**。**一个只能给白名单账号发货的 connector 不是可行的 Remote MCP 路径。**<br>(2) **覆盖面**：官方描述该 server 只给「**non-PII** Braze data」—— 聚合的 Canvas/Campaign analytics、segments、custom attributes。**任何 per-user 场景结构性够不到**，我们无论如何都得再写一遍 Managed REST，最后要维护两套。<br>➡️ Braze 有完整公开 REST API（bearer API key），所以是 Managed REST 而非 Blocked。**建议设复查提醒：若 Braze 转 GA 并放宽到非聚合数据，可干净地翻回 ✅。** 另确认：旧的本地版已官方 deprecated；'Use MCP Server' 权限**默认关闭**需显式授予 |
| **Zendesk** | tier1 | 🔧 Managed REST | 无 | API token / OAuth | — | [support.zendesk.com](https://support.zendesk.com/hc/en-us/articles/10497779528730) | **方向搞反的经典案例。** Zendesk 2026-06 推的是 MCP **Client** EAP（让 Zendesk 自己的 Copilot / AI Agent 去**消费**外部 MCP server），**不是给外部 Agent 调用 Zendesk 的 MCP Server**。官方文档标题就是「Connecting to MCP servers and using MCP tools in action flows (EAP)」。<br>**额外做了 DNS/HTTP 探测**：`mcp.zendesk.com` 虽因 `*.zendesk.com` 泛解析能解析，但 `/`、`/mcp`、`/sse`、`/v1/mcp` **全部 404**（响应头 `x-zendesk-origin-server: classic-app-server`，就是普通应用服务器）——**不存在真实 MCP 服务**。市面上叫 "Zendesk MCP Server" 的全是第三方。<br>➡️ Zendesk Support REST API v2。⚠️ **有传闻 API token 将 sunset、要迁 OAuth，实现时确认一次** |
| **Mailchimp**（Marketing） | tier1 | 🔧 Managed REST | 无 | OAuth2 | — | [mailchimp.com/developer](https://mailchimp.com/developer/marketing/docs/fundamentals/) | 已确认 `mailchimp.com/developer` 首页只列 Marketing API / Transactional / Open Commerce 三个产品，**Marketing 侧完全没有 MCP 提及**；`mcp.mailchimp.com` 无 DNS 解析。所有覆盖 audiences/campaigns 的都是第三方。<br>➡️ Marketing API v3.0，OAuth 2（访问他人账号必须 OAuth）。⚠️ **base URL 带 datacenter 前缀**（`https://<dc>.api.mailchimp.com/3.0`），**dc 从 API key 后缀或 OAuth metadata 端点取，不能硬编码** |
| **SendGrid** | tier1 | 🔧 Managed REST | 无 | Bearer PAT | — | [twilio.com/docs/ai/mcp](https://www.twilio.com/docs/ai/mcp) | SendGrid 归 Twilio，**Twilio 官方唯一托管的 MCP 是 docs-only 的那个**，且其索引范围**明确包含 SendGrid 文档** —— 即 Twilio 已把 SendGrid 纳入那个只读文档 server，**不存在另一个可执行的 SendGrid MCP**。`mcp.sendgrid.com` 无 DNS 解析。所有可执行的 SendGrid MCP 都是第三方（其中 `Garoth/sendgrid-mcp` 自己写了 "not affiliated with, endorsed by, or sponsored by Twilio SendGrid"）。<br>➡️ SendGrid v3 API，`Authorization: Bearer <API key>`，**非常直接**。Twilio 文档提到未来会出 OAuth 认证的可执行版本，**值得每季度复查一次** |
| **Twilio** | tier1 | 🔧 Managed REST | （文档 MCP 是 Streamable HTTP） | ⚠️ HTTP Basic | — | [twilio.com/docs/ai/mcp](https://www.twilio.com/docs/ai/mcp) | **与 3.2 是同一条。最容易误判的一条。** `https://mcp.twilio.com/docs` 实测返回 200 且**完全不需要认证**，但它是 developer-docs 检索工具，官方明写 read-only、「does not execute API calls on your behalf」—— **对「把 SaaS 能力包成 Tool」零价值**。<br>➡️ Twilio REST API，⚠️ **HTTP Basic（AccountSid:AuthToken 或 API Key SID/Secret），不是 Bearer**，Managed 侧要单独处理。**本类别最值得设复查提醒的一条** |
| **Freshdesk**（Freshworks） | tier2 | 🔧 Managed REST | Streamable HTTP | ⚠️ **裸 API key（无 `Bearer ` 前缀）** | `https://<your-freshdesk-domain>/mcp` | [support.freshdesk.com](https://support.freshdesk.com/support/solutions/articles/50000012670-model-context-protocol-mcp-integration-in-freshdesk-eap-) | **判 Managed REST 但情况微妙。** 官方确有 per-instance Remote MCP（Freshworks MCP Gateway），endpoint 是租户级（**必须从配置读**，同 GitLab self-hosted 形态）。官方接法 `claude mcp add --transport http https://<domain>/mcp --header "Authorization: <api-key>"`。<br>**三个否决理由**：(1) 官方原文「Freshdesk allows you to authenticate requests involved in MCP integration by using an **API key only**」，且 **header 是裸 API key、没有 `Bearer ` 前缀** —— 与 `scheme=bearer` 不兼容（**若 binding 支持空 scheme，此条可翻案**）；(2) **仅 Freshdesk Enterprise 套餐且是 EAP 白名单**，绝大多数客户拿不到；(3) EAP 限流严苛：**100 tool calls/min、5000 tool calls/月**。<br>➡️ 先按 Freshdesk API v2（Basic auth，`api_key:X`）。**等它 GA + 支持 Bearer 后可复议。** 🪤 别混淆 `developers.freshworks.com` 的 Developer MCP —— 那是给开发者管 app 生命周期的，不是业务数据 |

### 3.10 存储 / 文件

| Provider | 常见度 | 判定 | 传输 | 认证 | MCP Endpoint | 官方依据 | 备注 |
|---|---|---|---|---|---|---|---|
| **Google Drive** | tier1 | ✅ Remote MCP | Streamable HTTP | Google OAuth2 | `https://drivemcp.googleapis.com/mcp/v1` | [developers.google.com](https://developers.google.com/workspace/drive/api/guides/configure-mcp-server) | 实测 initialize 返回 200（Streamable HTTP，StatelessServer），`tools/list` 无需鉴权即可拿到；带假 Bearer 调 `tools/call` 返回 401 + `WWW-Authenticate: Bearer realm="https://accounts.google.com/"`。<br>⚠️ **工具集实测为只读+创建**：`search_files` / `read_file_content` / `download_file_content` / `get_file_metadata` / `get_file_permissions` / `list_recent_files` / `create_file` / `copy_file` —— **没有 delete/move/更新内容**，完整文件管理仍需 Drive REST 补。<br>⚠️ **三个坑**：(1) Google Workspace **Developer Preview**（非 GA）；(2) 需在自己的 GCP 项目里**同时启用 Drive API 和 Drive MCP API**；(3) **Google 明确不支持 DCR 与 OAuth Client ID Metadata Document，必须用自己注册的 OAuth client + 预登记 redirect URI** |
| **OneDrive / SharePoint** ★ | tier1 | ✅ Remote MCP | Streamable HTTP（`type:"http"`） | Entra ID OAuth | `https://agent365.svc.cloud.microsoft/agents/tenants/{tenantId}/servers/mcp_OneDriveRemoteServer` | [learn.microsoft.com](https://learn.microsoft.com/microsoft-copilot-studio/mcp-onedrive-work-iq) | **【复核推翻 —— 这是 Notion 错误的重演，见 [4.2 节](#42-复核推翻过的每一条)】** 原判 Managed REST，核心论断「微软自营托管 MCP 只有三类，没有面向文件的第一方 MCP」**是事实错误**：**有第四类 —— Work IQ MCP servers**，微软自建、预认证、明确覆盖 OneDrive 和 SharePoint 文件。原报告用微软官方 Learn MCP 检索却漏掉了它。<br>**实测一手证据**：POST 该端点 → `401` + `www-authenticate: Bearer resource_metadata=...`，响应头带 `x-ms-mcpservice`；PRM `{"resource_name":"mcp_OneDriveRemoteServer","authorization_servers":["https://login.microsoftonline.com/organizations/v2.0"],"bearer_methods_supported":["header"]}`。统一端点 `https://workiq.svc.cloud.microsoft/mcp` 同样 401 + Bearer。<br>**工具覆盖面远超原报告所称的「不含任何文件工具」**：Work IQ SharePoint 约 35 个工具，含 `findFileOrFolder` / `readSmallTextFile` / `createSmallBinaryFile` / `createFolder` / `renameFileOrFolder` / `deleteFileOrFolder` / `moveFileOrFolder` / `copyFileOrFolder` / `uploadFileFromUrl` / `shareFileOrFolder` / `setSensitivityLabelOnFile` + 完整 list/listItem/column CRUD。<br>🚨 **必须写进排期的真实门槛**：(a) preview，微软明写「aren't meant for production use」，工具名/参数可能变；(b) **所有文件读写硬性限制 ≤5MB —— 对文件类连接器是实打实的功能天花板**；(c) endpoint 按租户模板化；(d) 需 Entra 应用注册 + 管理员同意 + M365 Copilot 许可或 usage-based 计费。<br>➡️ **本仓库现有 450 行 Managed 实现，2026-07-25 决定继续走 Managed，理由已按 preview / 5MB 上限 / 许可证门槛记录在 [blueprint §8.1](blueprint.md#81-降级到-managed-rest-的条件)（不再是「微软没有第一方 MCP」这个假前提）。** |
| **Dropbox** | tier1 | ✅ Remote MCP | Streamable HTTP | Dropbox OAuth 2.0 | `https://mcp.dropbox.com/mcp` | [help.dropbox.com](https://help.dropbox.com/integrations/connect-dropbox-mcp-server) | **与 3.3 是同一条（两轮调研一致）。** PRM `authorization_servers=["https://www.dropbox.com"]`，scopes 含 `files.content.read` / `files.content.write` / `files.metadata.read` / `sharing.*` / `file_requests.*` / `account_info.read` —— **读写文件内容都在，覆盖面足够**。官方接法 `--transport http`。2026-03 起 open beta。🪤 别与 Dropbox Dash MCP（`/dash`）混用 |
| **Box** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth 2.0 | `https://mcp.box.com`（**根域名**） | [developer.box.com](https://developer.box.com/guides/box-mcp/remote/) | **与 3.3 是同一条。** 2025-08 GA（非 beta）。官方仓库 `box/mcp-server-box-remote` README 明写 "Authorization: Bearer token (via OAuth 2.0)"。工具覆盖 user info、文件/文件夹读取与搜索、Box AI 问答/元数据抽取。<br>⚠️ **三个坑**：(1) **仅 Business 及以上套餐**；(2) **无 DCR** —— 需管理员在 Admin Console → Integrations 启用预置的 "Box MCP Server" 并生成 client ID/secret，**redirect URI 要登记**；(3) 别与同名的自托管本地版 `box-community/mcp-server-box` 混淆 |
| **Google Cloud Storage** | tier2 | ✅ Remote MCP | Streamable HTTP | Google OAuth2 | `https://storage.googleapis.com/storage/mcp` | [docs.cloud.google.com](https://docs.cloud.google.com/mcp/supported-products) | 属 Google Cloud MCP 目录（50+ 产品同一套模式）。💡 **`tools/list` 实测：`list_buckets` / `list_objects` / `create_bucket` / `delete_bucket` / `read_text` / `read_object` / `write_text` / `delete_object` / `get_object_metadata` —— 对象级读写删都有，比 R2/COS 好得多，真能当文件连接器用。**<br>⚠️ Google 明确不支持 DCR / Client ID Metadata Document，**必须自带 OAuth client**；调用需带 `projectId` 参数 |
| **飞书云文档 / Lark Drive** | tier2 | ❓ 待验证 | Streamable HTTP | ⚠️ **结论冲突** | `https://mcp.feishu.cn/mcp` / `https://mcp.larksuite.com/mcp` | [open.feishu.cn](https://open.feishu.cn/document/mcp_open_tools/developers-call-remote-mcp-server) | **见 [4.3 节](#43-飞书--lark结论冲突未决)。本类别调研员给出了最强的正面证据**：实测 401 + `WWW-Authenticate: Bearer realm="mcp"` 带完整 scope 列表；PRM `authorization_servers=["https://accounts.feishu.cn/mcp"]`、`bearer_methods_supported:["header"]`，**且 `resource_documentation` 字段由服务器自己指回 open.feishu.cn 官方文档 —— 官方性是一手证据**。明确指出有**两条鉴权路径**：给 MCP 客户端的 OAuth2 Bearer（我们用这条）与给开发者的自定义头 `X-Lark-MCP-UAT`/`TAT`（**别选错**）。<br>⚠️ **覆盖面限制**：scope 实测为 `mcp-tool:docs:*` / `mcp-tool:common:*` / `mcp-tool:im:*`，即偏云文档 + IM，**云空间完整文件管理（上传/移动/权限）尚未覆盖** —— 若产品要的是网盘式文件操作，仍需补 Managed |
| **Egnyte** | tier3 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp-server.egnyte.com/mcp` | [developers.egnyte.com](https://developers.egnyte.com/docs/Remote_MCP_Server) | 官方原文「fully-managed Model Context Protocol (MCP) server」。💡 **多租户单端点**：**不需要按客户拼 `{domain}.egnyte.com`**，用户在 OAuth 授权页里输入自己的 Egnyte 域名，**端点固定** —— 比 GitLab 那种 instance 级 MCP 实现简单。<br>⚠️ **套餐门槛硬**：需 Gen 4 的 Essential/Elite/Ultimate 或 Gen 3 的 Platform Team/Business/Enterprise Lite/Enterprise。⚠️ **官方明写「Available tools and capabilities vary based on your Egnyte plan」—— 同一份 connector 在不同客户那里可用工具集不同，需要在运行时按 `tools/list` 结果降级。** 别与官方开源本地实现 `egnyte/egnyte-mcp-server` 混淆 |
| **AWS S3** | tier1 | ❓ 待验证 | Streamable HTTP（已确认） | ⚠️ bearer 路径未验证 | `https://aws-mcp.us-east-1.api.aws/mcp` | [docs.aws.amazon.com](https://docs.aws.amazon.com/aws-mcp/latest/userguide/what-is-mcp-server.html) | **【复核降级】** 原调研自己就写了「请先做 PoC 再排期、confidence medium」—— **既然规则是「宁可标 ❓ 也不要猜」，一条自认未验证的关键标准就不该以 REMOTE_MCP 结案**，挂着 ✅ 会被下游按 400 行排期。<br>**标准 1/2/3 已复现成立**：initialize 返回 `200 application/json`，`serverInfo {"name":"AWSMCP","version":"1.0.0"}`。**卡在标准 4**，三处硬伤：(a) **AWS 官方文档全篇不描述 bearer 路径** —— `security-iam.html` 通篇只讲 IAM 身份与 SigV4，**没有任何一处说明如何取得能授权访问某客户 AWS 账号 S3 的 bearer token**；(b) 该 AS 的 `token_endpoint_auth_methods_supported: ["none"]`，**只支持 public client、不支持 client secret，与我们做 confidential-client OAuth 的模型直接冲突**；(c) 它是 AWS **sign-in** 授权服务器（console 身份），与「把某 AWS 账号的 S3 权限授予第三方 app」不是同一件事。<br>⚠️ **工具也是粗粒度 CLI 包装**（`aws___call_aws` / `aws___run_script` / `aws___get_presigned_url` …），**没有一个类型化 S3 工具**，S3 操作要靠拼 `aws s3api ...` 字符串；且是 preview。<br>🚨 **纠正一处退路误判**：原报告说「若 bearer 路走不通，退回 Managed 也很痛」——**实际比「痛」更严重：S3 REST 强制 SigV4，无法用单个 Authorization bearer 表达，MCPAuthBinding 覆盖不了。所以 aws_s3 的真实分支是 ✅（PoC 证明纯 bearer 可行）或 ⛔ Blocked，不存在 Managed REST 这个中间选项。** |
| **Azure Blob Storage** | tier2 | 🔧 Managed REST | stdio only | Entra OAuth2 | — | [learn.microsoft.com](https://learn.microsoft.com/azure/developer/azure-mcp-server/overview) | Azure MCP Server 是**本地安装**的那一类：官方 overview 明写通过 VS Code / Visual Studio / Cursor / IntelliJ 安装，鉴权是「Entra ID through the Azure Identity library」（**本地凭据链，不是远程 endpoint 上的 Authorization header**），前置条件甚至包含 GitHub Copilot 订阅。经 Learn MCP 检索确认微软没有托管的 Azure MCP HTTPS 端点；出现的 "remote MCP" 内容都是 Azure Functions 让你**自己部署**的模板。<br>➡️ Azure Blob REST（`https://{account}.blob.core.windows.net/`），**可用 Entra OAuth2 bearer**（scope `https://storage.azure.com/.default`），**认证模型与本项目兼容，不必用 Shared Key** |
| **阿里云 OSS** | tier2 | 🔧 Managed REST | stdio only | AK/SK | — | [help.aliyun.com](https://help.aliyun.com/zh/oss/developer-reference/oss-mcp-server-alpha) | 查过官方帮助中心，不是靠印象：官方 OSS MCP Server 目前是 **Alpha**，只能 STDIO 或自建 HTTP，**文档原文「OSS MCP Server 仅监听 localhost，远程部署时需通过反向代理进行端口转发」**，鉴权是 `OSS_ACCESS_KEY_ID`/`SECRET` 环境变量 —— 「远程托管」和「bearer」两条都不满足。<br>⚠️ **别用阿里云 OpenAPI MCP 顶替**：它确实是阿里云托管且已在 3.6 判为 ✅，但**覆盖的是 OpenAPI 管控面，OSS 的对象读写走的是 OSS 自有签名的数据面 API，不在其中**。<br>➡️ OSS REST + 阿里云签名（V1/V4），**不是 bearer，需要在 Managed 侧自己签名** |
| **腾讯云 COS** | tier3 | 🔧 Managed REST | stdio only | AK/SK | — | [Tencent/cos-mcp](https://github.com/Tencent/cos-mcp) | 腾讯官方确有 COS MCP Server（`Tencent/cos-mcp`，npm 包 `cos-mcp`），但**是本地 stdio 包**，安装方式是 npx + 填 SecretId/SecretKey，**没有腾讯托管的 HTTPS endpoint**（`mcp.tencentcloud.com/mcp` 实测无响应）。腾讯云开发者站的 MCP 广场条目指向的也是同一个本地包。<br>➡️ COS REST API，签名算法 TC3/HMAC-SHA1，**非 bearer**。⚠️ **功能面注意**：COS MCP 还捆绑了数据万象 CI（图片超分、裁剪、文档转 PDF），**若要对齐这些能力，Managed 代码量会明显超过 1,500 行下限** |
| **Cloudflare R2** | tier3 | 🔧 Managed REST | （Cloudflare MCP 是 Streamable HTTP） | ⚠️ 对象面需 SigV4 | — | [developers.cloudflare.com](https://developers.cloudflare.com/agents/model-context-protocol/mcp-servers-for-cloudflare/) | **这条要读清楚再决策。** Cloudflare **确实**有一批自营托管Remote MCP（见 3.6），四条硬标准形式上都过。**但对「存储/文件」这个用途它不够用**：Cloudflare v4 REST API 对 R2 **只有桶级/配置级操作**（创建桶、CORS、生命周期、自定义域），**对象的 LIST/GET/PUT/DELETE 走的是 R2 的 S3 兼容端点 + AWS SigV4 签名**，既不在那套 API 里，也无法用单个 Authorization bearer 表达。<br>➡️ 做 R2 **文件**连接器仍要走 Managed（S3 兼容 API + SigV4）。💡 **若只是要做「R2 桶管理」这种运维型连接器，那就可以直接用 `https://mcp.cloudflare.com/mcp` 走 Remote 路径。**<br>⚠️ confidence medium 的原因：未能逐一枚举 workers-bindings server 的 R2 工具清单（需 OAuth 才能 `tools/list`）。**若 PoC 发现它已提供对象级工具，可改判 ✅** |

### 3.11 AI / ML 平台

| Provider | 常见度 | 判定 | 传输 | 认证 | MCP Endpoint | 官方依据 | 备注 |
|---|---|---|---|---|---|---|---|
| **Hugging Face** | tier1 | ✅ Remote MCP | Streamable HTTP（stateless） | **Bearer HF_TOKEN** / OAuth | `https://huggingface.co/mcp` | [huggingface.co/blog](https://huggingface.co/blog/building-hf-mcp) | 💡 **本类别最干净的一条。** 实测 initialize 成功返回 `protocolVersion 2025-06-18` + serverInfo `@huggingface/mcp-services v0.3.35`（**无 token 也能匿名 initialize，带 token 解锁个人化工具**）。官方博客原文：生产部署用 Streamable HTTP（stateless direct response），**SSE 已 deprecated、仅保留在自建 Space**。认证 `Authorization: Bearer <HF_TOKEN>`，也支持 OAuth（URL 加 `?login`）。**完全契合 `scheme=bearer`** |
| **Google Gemini / Vertex AI** | tier1 | ✅ Remote MCP | HTTP | Google OAuth2 + IAM | `https://aiplatform.googleapis.com/mcp/{toolset}` | [docs.cloud.google.com](https://docs.cloud.google.com/gemini-enterprise-agent-platform/reference/use-agent-platform-mcp) | 官方明写 Server URL = `https://aiplatform.googleapis.com` + TOOLSET_ENDPOINT，scope `.../auth/aiplatform` 或 `cloud-platform`。实测 `/mcp/generate` 与 `/mcp/models` 的 `tools/list` 直接返回 JSON-RPC result（**文档明说 `tools/list` 不需鉴权**）。<br>⚠️ **四个坑**：(1) **不是单一 endpoint**，按 toolset 拆成 8 个路径（`/mcp/generate`、`/predict`、`/notebook`、`/endpoints`、`/models`、`/tuning`、`/evaluation`、`/prompts`）—— **要么绑多个 connection、要么让 endpoint 可配置**；(2) **裸 `/mcp` 是 404**；(3) 需 GCP 项目开通计费 + 启用 API + IAM 角色 `roles/mcp.toolUser` 与 `roles/aiplatform.user`，**不是注册即用**；(4) **普通 API key 不支持**，必须是绑定 service account 的 key 或 OAuth token |
| **OpenRouter** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth 2.1 + PKCE + **DCR** | `https://mcp.openrouter.ai/mcp` | [openrouter.ai/docs](https://openrouter.ai/docs/guides/overview/mcp-server) | 官方博客 + 官方 docs 给出同一 URL。PRM `{"resource":"https://mcp.openrouter.ai/mcp","authorization_servers":["https://mcp.openrouter.ai"],"bearer_methods_supported":["header"]}`，AS 支持 authorization_code + PKCE S256 + **动态注册（`/oauth/register`）** —— 标准 OAuth 2.1 Remote MCP。15+ 工具（`send-message`、`generate-image`、`list-models`、`get-credits`）。<br>⚠️ **坑：OAuth 流程铸造的 API key 默认 7 天过期 + $10 消费上限 —— 长期 connection 必须处理刷新/续期** |
| **LangSmith**（LangChain） | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth 2.1 + **DCR** / `X-Api-Key` | `https://api.smith.langchain.com/mcp` | [docs.langchain.com](https://docs.langchain.com/langsmith/langsmith-remote-mcp) | 官方原文：「The Remote MCP is stateless and responds with JSON over the standard **Streamable HTTP** transport」。实测 401 + PRM（同域随机路径是 404，**证明 `/mcp` 是真实路由**）。<br>⚠️ **多区域 endpoint 各不相同**：GCP US `api.smith.langchain.com`、EU `eu.api.smith.langchain.com`、APAC `apac.api.smith.langchain.com`、AWS US `aws.api.smith.langchain.com`；self-hosted 是 `https://<your-host>/api/mcp`（需 v0.16+）。**endpoint 必须从配置读**。旧的本地 `langsmith-mcp-server` 已官方标 Deprecated |
| **Weights & Biases** | tier2 | ✅ Remote MCP | Streamable HTTP | **Bearer W&B API key** | `https://mcp.withwandb.com/mcp` | [docs.wandb.ai](https://docs.wandb.ai/platform/mcp-server) | 💡 官方 docs 直接给 `claude mcp add --transport http wandb https://mcp.withwandb.com/mcp --header "Authorization: Bearer [YOUR-WANDB-API-KEY]"` —— **`--transport http` 即 Streamable HTTP，纯 PAT 直塞，不需要 OAuth 流程，与 MCPAuthBinding 完美对齐**。<br>⚠️ Dedicated Cloud / Self-Managed 部署是 `https://[YOUR-INSTANCE]/mcp`，**endpoint 必须可配置**（同 GitLab 那类问题）。🪤 别与本地 stdio 包 `wandb/wandb-mcp-server` 混淆 |
| **Replicate** | tier2 | ❓ 待验证 | ⚠️ 文档为 SSE + stdio | OAuth2 | ⚠️ 文档端点 `https://mcp.replicate.com/sse`；`/mcp` 存在但传输未确认 | [replicate.com/docs](https://replicate.com/docs/reference/mcp) | **【复核推翻】** 原判完全建立在**未文档化路径的匿名 401 探测**上，不满足「endpoint 必须有厂商官方文档 URL」。<br>**厂商自己的托管落地页上每一条客户端指引都指向 `/sse`**：Claude Web/Desktop「Add the MCP server URL: `https://mcp.replicate.com/sse`」；Claude Code `--transport sse`；Cursor 与 Windsurf 用 `mcp-remote` stdio 桥接包同一个 `/sse`；Codex 用 `npx -y replicate-mcp@latest`（stdio）。**「streamable」一词在厂商 MCP 站点上零出现。**<br>**复核还攻击了原探测证据本身**：原论据是「`/mcp` 401 而随机路径 404，说明 `/mcp` 是真实路由」。两个问题：(a) **`GET /mcp` 也返回相同 401，`/sse` 返回逐字节相同的 401 和相同的头** —— 与「一个 auth 中间件挂在整个 MCP 路由前缀上」一致，**401 无法区分 Streamable HTTP handler、SSE handler 还是纯别名**；(b) **OAuth metadata 的 scope 是错的**：`/.well-known/oauth-protected-resource` 返回 `{"resource":"https://mcp.replicate.com/","authorization_servers":["https://mcp.replicate.com/"]}` —— **裸根路径**，而 per-path 文档 `/.well-known/oauth-protected-resource/mcp` 返回 404。对比 OpenRouter 与 LangSmith，两者的 metadata resource 都精确钉在 `/mcp` 上。Replicate 的 `www-authenticate` 也**完全没有 `resource_metadata` 参数**，且不像 OpenRouter 那样在 `access-control-expose-headers` 里暴露 `mcp-session-id` / `mcp-protocol-version` —— **所有在其他条目上确认 Streamable HTTP 的正面信号，这里全部缺席**。<br>➡️ 标 ❓ 而非 🔧，因为既不能确认也不能证伪。**原调研提出的 5 分钟验证（拿真实 `r8_` token 对 `/mcp` 打一次 initialize）能决定性解决它。在此之前按 Managed REST 的量级做规划** —— Replicate 官网自己说托管 server 只是「automatically updated with the latest features from Replicate's HTTP API」，**走 REST 损失不大** |
| **OpenAI** | tier1 | 🔧 Managed REST | （文档 MCP 是 Streamable HTTP） | Bearer PAT | — | [developers.openai.com](https://developers.openai.com/learn/docs-mcp) | **本类别最容易误判的一条。** OpenAI 确实有官方托管、Streamable HTTP 的Remote MCP `https://developers.openai.com/mcp`（实测 initialize 成功，serverInfo `openai-docs-mcp v1.0.0`，**无需认证**），**但官方原话：「This MCP server is documentation-only. It does not call the OpenAI API on your behalf.」** 只能搜文档，**不含任何用户数据、不接 API key、没有 OAuth 绑定 —— 对 connector 场景零价值**。另探测 `api.openai.com/mcp`、`mcp.openai.com/mcp` 均无解析/无路由。<br>⚠️ OpenAI 侧的 MCP 支持**全是客户端方向**（Responses API 的 MCP tool / Connectors、Apps SDK）。<br>➡️ chat/completions、files、batch、fine-tuning、assistants 必须走 Managed REST。💡 若想额外挂一个免鉴权的文档搜索工具，可另开一条 auth-less 的 remote 绑定，**但不要把它算作 openai connector** |
| **Anthropic**（Claude API） | tier1 | 🔧 Managed REST | 无 | ⚠️ `x-api-key` | — | [platform.claude.com](https://platform.claude.com/docs/en/agents-and-tools/mcp-connector) | 已确认：**Anthropic 在 MCP 生态里全部是客户端/宿主角色** —— Messages API 的 MCP connector（消费别人的 Remote MCP）、Claude Code 的 `mcp add`、Connectors Directory（收录第三方 server）。**没有任何 Anthropic 托管的、暴露 Claude API 自身能力的 Remote MCP server。** 探测：`api.anthropic.com/mcp` → 404；`platform.claude.com/mcp` → 404；`mcp.claude.com`、`mcp.anthropic.com` → DNS 不解析；`docs.claude.com/mcp` → 301 到文档页（只是文档重定向，不是 MCP 端点）。<br>➡️ Messages / Files / Batches / Models 只能走 Managed REST。⚠️ **认证是 `x-api-key` header，不是 `Authorization: Bearer`**，Managed 实现时注意这个差异 |
| **ElevenLabs** | tier2 | 🔧 Managed REST | stdio only | API key | — | [elevenlabs/elevenlabs-mcp](https://github.com/elevenlabs/elevenlabs-mcp) | 官方 MCP server **纯本地 stdio**：`uvx elevenlabs-mcp` / `pip install elevenlabs-mcp`，凭 `ELEVENLABS_API_KEY` 环境变量，README 无任何托管 URL。探测 `mcp.elevenlabs.io`（DNS 不解析）、`api.elevenlabs.io/mcp`（404）、`elevenlabs.io/docs/mcp`（404）。<br>🪤 **别被官方文档 `elevenlabs.io/docs/eleven-agents/customization/tools/mcp` 误导** —— 那是 ElevenLabs Agents **作为客户端**去连别人的 MCP server，**方向相反**。<br>➡️ TTS / STT / voice cloning / dubbing 走 Managed REST（`xi-api-key` header） |
| **Perplexity** | tier2 | 🔧 Managed REST | stdio only | Bearer API key | — | [docs.perplexity.ai](https://docs.perplexity.ai/docs/getting-started/integrations/mcp-server) | 官方集成文档只给 stdio 配置（`npx -y @perplexity-ai/mcp-server` + `PERPLEXITY_API_KEY`），全文无 https endpoint。`mcp.perplexity.ai` DNS 不解析。<br>🪤 **陷阱一**：`docs.perplexity.ai/mcp` 实测能 initialize 成功，**但 serverInfo 是 Mintlify 自动生成的文档搜索服务**，跟 Sonar API 能力无关。🪤 **陷阱二**：帮助中心那篇 "Local and Remote MCPs for Perplexity" 讲的是 Perplexity/Comet **作为客户端**连别人的 MCP，方向相反。<br>➡️ Sonar search/chat completions 走 `api.perplexity.ai`，`Authorization: Bearer` |
| **Groq** | tier2 | 🔧 Managed REST | stdio only | Bearer PAT | — | [groq/groq-mcp-server](https://github.com/groq/groq-mcp-server) | 官方是本地 stdio 包（`uvx groq-mcp` + `GROQ_API_KEY`），README 无托管 endpoint。探测 `api.groq.com/mcp`（404）、`/v1/mcp`（404）、`console.groq.com/mcp`（404）、`mcp.groq.com`（DNS 不解析）、`groq.com/mcp`（405 openresty，非 MCP）。🪤 `console.groq.com/docs/tool-use/remote-mcp` 是 Groq API **作为客户端**调用Remote MCP 的特性，**方向相反**。<br>➡️ Groq 的 OpenAI 兼容 REST（`Authorization: Bearer gsk_...`）。💡 **接口是 OpenAI 兼容的，可复用 OpenAI connector 的大部分代码，实际工作量应远低于 1,500 行下限** |
| **Mistral AI** | tier2 | 🔧 Managed REST | 未知 | Bearer PAT | — | [docs.mistral.ai](https://docs.mistral.ai/vibe/work/connectors/mcp-connectors) | MCP 相关内容**全是消费方向**（Le Chat / Agents API / Vibe 连外部 MCP server，以及「Mistral Connectors 作为托管 MCP 代理」）。找不到任何 Mistral 托管的、暴露自家 API 能力的 Remote MCP server 文档。<br>💡 **一个未解释的线索**：实测 `POST https://api.mistral.ai/mcp` 返回 `401 {"detail":"Unauthorized"}`，而同域随机路径返回 Kong 的 `404 {"message":"no Route matched with those values"}` —— **说明 `/mcp` 确实是一条已注册、需鉴权的路由**；但它**没有 `WWW-Authenticate` 头、没有 PRM、且零官方文档**，倾向是 Mistral Connectors 代理的内部入口而非面向我们的 server。<br>➡️ **证据不足以判 ✅，故按 Managed REST 走**（chat/completions、embeddings、OCR、agents 都有完备 REST）。**若日后 Mistral 公开这条路由的文档，此条应重判** |

### 3.12 电商 / 广告

| Provider | 常见度 | 判定 | 传输 | 认证 | MCP Endpoint | 官方依据 | 备注 |
|---|---|---|---|---|---|---|---|
| **Meta Ads**（Marketing API） | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 / user access token | `https://mcp.facebook.com/ads` | [developers.facebook.com](https://developers.facebook.com/documentation/ads-commerce/ads-ai-connectors/ads-mcp-server/ads-mcp-server-get-started/) | 💡 **最干净的一条。** 官方原文「The server is remote-hosted at `https://mcp.facebook.com/ads`」，并直接给出 `claude mcp add --transport http` 与 `curl -H "Authorization: Bearer <ACCESS_TOKEN>"` 的 JSON-RPC 示例 —— **Streamable HTTP + 纯 bearer，完全匹配 MCPAuthBinding**。<br>scopes：`ads_mcp_management, ads_read, ads_management, catalog_management, business_management, pages_show_list, instagram_basic`。**可写**（文档示例含「创建 traffic campaign」），**新建实体默认 paused**。⚠️ 需要自己的 Meta developer app 并加上 "Create & manage ads with ads MCP server" use case。2026-04-29 开放 beta，2026-07-16 正式对开发者公开 |
| **Stripe** ★ | tier1 | ✅ Remote MCP | Streamable HTTP | OAuth2 / restricted key | `https://mcp.stripe.com` | [docs.stripe.com/mcp](https://docs.stripe.com/mcp) | **与 3.5 是同一条（两轮独立调研一致）。本仓库 `packages/connectors/stripe` 已于 2026-07-25 改写为 Remote MCP（361 行 / 5 tool）。** 两种认证都能用单个 Authorization header 表达 |
| **PayPal** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth2 | `https://mcp.paypal.com/mcp` / `https://mcp.sandbox.paypal.com/mcp` | [docs.paypal.ai](https://docs.paypal.ai/developer/tools/ai/mcp-quickstart/) | **与 3.5 是同一条。⚠️ 官方文档写的 `/http` 路径实际不存在（prod/sandbox × GET/POST 四组组合全部 404），勿照抄；用 `/mcp`。**<br>💡 传输判读要点：文档正文确认「supports two types of transport: Server-sent events (SSE), Streamable HTTP」，页面把 SSE 映射到 `/sse`、把 Streamable HTTP 映射到 `/http`（死链）。合理推断 `/mcp` 承载 Streamable HTTP（返回标准 PRM 挑战），**但这一条只有推断、无文档背书 —— 实施前拿真实 token 做一次 initialize 握手确认**。<br>🪤 文档远程配置示例用 `npx mcp-remote` 桥接，**那只是给不支持Remote MCP 的客户端用的，不影响判定** |
| **Klaviyo** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth（含 DCR） | `https://mcp.klaviyo.com/mcp` | [developers.klaviyo.com](https://developers.klaviyo.com/en/docs/klaviyo_mcp_server) | **与 3.9 是同一条。** PRM `resource_name="Klaviyo MCP Server"`、`authorization_servers=["https://mcp.klaviyo.com"]`、`bearer_methods_supported=["header"]` —— 自带授权服务器，接入成本极低 |
| **Amazon Ads** | tier2 | ✅ Remote MCP | Streamable HTTP | OAuth 2.1（LwA） | NA `https://advertising-ai.amazon.com/mcp`<br>EU `https://advertising-ai-eu.amazon.com/mcp`<br>FE `https://advertising-ai-fe.amazon.com/mcp` | [advertising.amazon.com](https://advertising.amazon.com/API/docs/en-us/mcp/get-started) | **【复核维持判定，但降置信度并修正一条 load-bearing 断言】**<br>✅ 已证实：官方 OAuth 页配置为 `{"type":"http","url":"..."}` 且 `claude mcp add --transport http`；三区域 endpoint **必须从配置读，不可硬编码**；PRM `{"authorization_servers":["https://lwa.amazon.com"],"scopes_supported":["advertising::campaign_management"]}`；OAuth 2.1 支持 public client（仅 clientId）与 private client（clientId+secret，自动刷新）。<br>🚨 **修正点**：原记录称「走 OAuth 2.1 模式官方文档不再要求 `Amazon-Ads-ClientId` header，所以纯 bearer 绑定可行」并给 high confidence —— **这与官方 Get Started 页正文直接冲突**，该页「Required Headers」一节写的是**无条件表述**：「each request to the Amazon Ads MCP server **requires the following headers**: `Amazon-Ads-ClientId` ... `Authorization: Bearer ...`」。OAuth 页的 config 示例不含 headers 字段，**但那只是客户端配置省略，不等于服务端不校验**。用 bogus token 带/不带该 header 实测两次均返回相同 401（token 先失败），**该点无法证伪也无法证实**。<br>**这很关键**：`MCPAuthBinding` 不支持任意静态附加 header；若该 header 确为必需，**需先扩展 binding**（好在该值是我们自己的 LwA client id，是常量而非 per-user 密钥，扩展成本不大）。<br>⚠️ 另两坑属实：账户上下文默认 Dynamic 模式（LLM 先调 `query_advertiser_accounts` 拿 profileId）；**前置条件是已获批的 Amazon Ads API developer 账号 + LwA security profile，非自助注册**。2026-02 起 open beta，200+ tools（SP/SB/SD/DSP/AMC）<br>➡️ **实施前用真实 token 做一次带/不带 `Amazon-Ads-ClientId` 的对照实验** |
| **Wix**（Wix Stores / Headless） | tier2 | ✅ Remote MCP | Streamable HTTP | **OAuth（必须）** | `https://mcp.wix.com/mcp` | [dev.wix.com](https://dev.wix.com/docs/sdk/articles/use-the-wix-mcp/about-the-wix-mcp) | 官方远程配置即 `{"type":"http","url":"https://mcp.wix.com/mcp"}`（文档说 type 可选 `sse` 或 `http`，**我们取 http**）。<br>⚠️ **坑：API key 模式下 header 是 `Authorization: <API_KEY>`（没有 `Bearer ` 前缀）且需要额外的 `wix-account-id` header —— 这条路不匹配 `scheme=bearer`，必须走 OAuth 那条。**<br>⚠️ **工具集偏「文档搜索 + 通用 API 代理」**（`ListWixSites` / `CallWixSiteAPI` / `ManageWixSite`）：可覆盖 Wix Stores 的商品与订单操作，**但不是细粒度电商 tool，Agent 体验可能不如手写 tool 精确 —— 写进权衡**。🪤 `npx @wix/mcp-remote` 只是桥接，不影响判定 |
| **Mailchimp Transactional**（Mandrill） | tier3 | ✅ Remote MCP | Streamable HTTP | **Bearer API key** | `https://mandrillapp.com/mcp` | [mailchimp.com/developer](https://mailchimp.com/developer/transactional/guides/how-to-use-mailchimps-transactional-messaging-mcp/) | **与 3.9 是同一条（两轮调研一致）。⚠️ 只覆盖 Transactional，不覆盖 Marketing 平台，所以单独列条。** 工具含通用 API 代理 `call_api` |
| **Shopify** | tier1 | 🔧 Managed REST | 无（商家侧） | OAuth2 | — | [shopify.dev](https://shopify.dev/docs/apps/build/storefront-mcp) | **这条最容易误判，务必读完。** Shopify 确实有官方托管的 Remote MCP，**但全是买家侧的，没有商家 Admin API 的Remote MCP**。官方「MCP servers」一节只列两个：<br>(1) **Storefront MCP** `https://{shop}.myshopify.com/api/mcp` —— 官方原文「Storefront MCP servers **don't require authentication**」，工具只有 `search_catalog` / `lookup_catalog` / `get_product` / `get_cart` / `update_cart` / `search_shop_policies_and_faqs`；<br>(2) **Customer accounts MCP** `https://{shopDomain}/customer/api/mcp` —— OAuth2 + PKCE，**但主体是终端消费者**（查订单、退货），且文档示例 header 写的是 `'Authorization': 'YOUR_ACCESS_TOKEN'`（**无 Bearer 前缀**）。<br>**两者都覆盖不了 connector 真正需要的 Admin GraphQL**（商品/库存/履约/折扣/Webhook）。`Shopify/dev-mcp` 仓库已 404，且 Dev MCP 本来也是本地 npx + 只搜文档。<br>➡️ Admin GraphQL 走 Managed REST。💡 **若日后只做「购物导购」场景，Storefront MCP 可作为独立 provider 复用**（无鉴权，per-shop endpoint 需从配置读） |
| **Google Ads** | tier1 | 🔧 Managed REST | stdio only | OAuth2 | — | [developers.google.com](https://developers.google.com/google-ads/api/docs/developer-toolkit/mcp-server) | **本仓库已有 `packages/connectors/googleads`，该选择是对的。** Google 官方确实发了 MCP server（2026-04-28），但官方文档 Key specifications 一栏白纸黑字：**`Transport: Standard input/output (stdio)`、`Mode: Read-only (current release)`**。且**没有任何 Google 托管的 endpoint** —— 文档只教你本地 `pipx run ...`，或自己 build Docker image 部署到**你自己的** Cloud Run（要自己配 `GOOGLE_ADS_MCP_BASE_URL`）。**开 OAuth proxy 后可切 streamable-http，但那是你自己运营的服务器，不满足条件 2。** 另需 Developer Token（22 位）。**只读** —— 建广告/改预算官方明说仍走 REST/gRPC<br>💡 **本仓库采用了第三条路：`ProvenanceSelfHosted` + `EndpointConfigField`，让用户自带自托管 endpoint，380 行搞定。见 [5.2 节](#52-一条本表二分法没覆盖的第三条路)** |
| **Amazon Seller Central**（SP-API） | tier1 | 🔧 Managed REST | stdio only | OAuth2（LWA） | — | [amzn/selling-partner-api-samples](https://github.com/amzn/selling-partner-api-samples/tree/main/use-cases/sp-api-dev-mcp/sp-api-dev-assistant-mcp-server) | **确认没有 Amazon 托管的 SP-API Remote MCP。⚠️ 注意别和 Amazon Ads MCP 混淆 —— 那是两套东西，Ads 有、Seller 没有。** Amazon 唯一的官方产物是 samples 仓库里的 `sp-api-dev-assistant-mcp-server`，配置是 `"command": "npx"`，本地 stdio，**而且定位是开发助手**（搜 API 目录、生成代码、本地向量搜文档），不是给 Agent 跑业务的。探测：`sellingpartnerapi-na.amazon.com/mcp`（SP-API 网关通用 403，非 MCP 响应）、`mcp.sellercentral.amazon.com`、`sellercentral-ai.amazon.com`（均不解析）。市面上所有「hosted Amazon Seller MCP」都是第三方 SaaS。<br>➡️ SP-API。⚠️ **LWA OAuth + RDT 受限数据令牌，工作量偏上限** |
| **WooCommerce** | tier1 | 🔧 Managed REST | ⚠️ local proxy | ⚠️ `X-MCP-API-Key` | — | [developer.woocommerce.com](https://developer.woocommerce.com/docs/features/mcp/) | WooCommerce 官方**有** MCP，但**三条硬性条件全挂**：(1) **不是厂商托管** —— Woo 是自托管 WordPress 插件，endpoint 在商家自己的站上 `https://yourstore.com/wp-json/woocommerce/mcp`（**且该路径文档标注为 deprecated**）；(2) 传输是「local proxy approach」—— 客户端走 stdio/JSON-RPC 到一个本地代理，再由代理翻译成对 WP REST API 的 HTTP 调用，**不是可直连的 Streamable HTTP**；(3) 认证是自定义 header `X-MCP-API-Key: consumer_key:consumer_secret`，**不是 `Authorization: Bearer`**。官方还明确标注 developer preview。<br>➡️ WooCommerce REST API v3，**base URL 必须从配置读**（自托管，同 GitLab 情形） |
| **BigCommerce** | tier2 | 🔧 Managed REST | Streamable HTTP（买家侧） | ⚠️ 基本无鉴权 | — | [docs.bigcommerce.com](https://docs.bigcommerce.com/developer/api-reference/mcp/overview) | **和 Shopify 同一个坑**：官方 MCP 存在但是**买家侧 Storefront MCP，不是商家 Admin**。官方说它提供 "purpose-built commerce tools" —— search products / manage cart / hand off to checkout；**B2C Storefront 处于 Beta 且当前只支持 guest shopping**（logged-in "coming soon"，B2B "coming soon"），因此**基本无鉴权可言，谈不上用 bearer 绑定承载商家凭证**。endpoint 不是固定厂商域名，而是商家在控制台开启 beta 后拿到的 **store-specific URL**（需从配置读，开启后约 10 分钟生效）。🪤 `docs.bigcommerce.com/developer/docs/ai-agent-setup` 那个 MCP 只是把开发者文档喂给 AI。<br>➡️ BigCommerce V3 REST API，⚠️ **`X-Auth-Token` header，也不是 Bearer** |
| **Mailchimp**（Marketing） | tier2 | 🔧 Managed REST | 无 | OAuth2 | — | [mailchimp.com/developer/mcp](https://mailchimp.com/developer/mcp) | **与 3.9 是同一条。** Intuit/Mailchimp 官方**只**为 Transactional（Mandrill）做了 Remote MCP；**Marketing 平台（audiences / lists / campaigns / automations / segments）没有官方 MCP，无论远程还是本地**。市面上覆盖 Marketing API 的都是社区实现（如 227 tools 的 `damientilman/mailchimp-mcp-server`）。`mcp.mailchimp.com` 不解析。<br>➡️ Marketing API v3，**datacenter 前缀 `usX.api.mailchimp.com`，base URL 要从 token metadata 动态取** |
| **TikTok Ads**（TikTok for Business） | tier2 | ❓ 待验证 | 未知 | 未知 | 未知 | [business-api.tiktok.com](https://business-api.tiktok.com/portal/docs?id=how-to-connect-to-tiktok-for-business-mcp-server) | **标 ❓ 是因为确实拿不到厂商一手证据，不是因为没查。**<br>**已知事实**：TikTok 在 2026-05-12/13 的 TikTok World 上发布了 TikTok for Business MCP + Agentic Hub，且官方文档站**存在**一个标题为「How to connect to TikTok for Business MCP server」的页面（另有 `/portal/docs/tiktok-ads-mcp-server/v1.3` 和 `ads.tiktok.com/help/article/about-tiktok-for-business-agentic-hub-and-mcp-server`）—— **有这个页面标题就说明托管版大概率真实存在**。<br>**但无法核实 endpoint / transport / 认证方式**：本机到 `*.tiktok.com` 的 TLS 连接被环境阻断（curl exit 35），无头浏览器 `net::ERR_CONNECTION_CLOSED`，WebFetch 与渲染器只拿到 JS 外壳。探测 `mcp.tiktok.com` 等均无响应（**同样被阻断，不能作为不存在的证据**）。第三方报道口径也不一致。<br>➡️ **下一步**：换一个能访问 tiktok.com 的网络重新打开上述官方页面，**重点确认 endpoint URL、是否 Streamable HTTP、以及是否只需 `Authorization: Bearer`** —— ⚠️ **TikTok Ads API 传统上用 `Access-Token` 自定义 header，若 MCP 沿用则不满足条件 4**。在此之前不要按 400 行排期 |

---

## 4. 重点提示：看起来该手写、其实有官方 MCP

**这是本文档最大的价值所在。** 下面这些 provider，凭印象或凭一次粗略搜索很容易判成「没有 MCP，只能手写」，而它们实际上有厂商托管的 Streamable HTTP + Bearer 端点。

### 4.1 头号案例：Notion（已改造完成）

> **本仓库 `packages/connectors/notion` 曾是 3,296 行 Managed REST。**
> **而 Notion 有官方托管的 Remote MCP：`https://mcp.notion.com/mcp`。**
> **2026-07-25 已改写完成：414 行 / 10 个 tool。**

- 两轮**独立**调研（「文档/知识库」与「项目管理」两个类别各一次）**都**确认了它，且都通过了对抗性复核。
- 官方开发者文档明确 **Streamable HTTP 为推荐传输**，`/sse` 只是 legacy 客户端兜底。
- 实测 `401` + `WWW-Authenticate: Bearer realm="OAuth", resource_metadata=https://mcp.notion.com/.well-known/oauth-protected-resource/mcp`。
- 支持 **Dynamic Client Registration (RFC 7591)**。

**为什么当初会判错？** 因为官方文档里有一句话极具误导性：

> *"Notion MCP requires user-based OAuth authentication and does not support bearer token authentication."*

看到 "does not support bearer token authentication" 很容易直接否掉。**但这句话说的是「不接受 Notion REST API 的 internal integration token / PAT（`ntn_...`）」**，而不是「不能用 `Authorization: Bearer` 这个 header」。走完 OAuth 后拿到的 access token **仍然是以 `Authorization: Bearer` 提交的** —— **完全符合 `MCPAuthBinding{Scheme: "bearer"}`**。

**教训：判定第 4 条要看的是「凭据能不能用 `Authorization: Bearer` 这个 header 承载」，不是「厂商有没有说 bearer token 这四个字」。**

改造收益（**实测，非估算**）：**3,296 → 414 行，省 2,882 行，tool 数 6 → 10**（代价见 [5.4 节](#54-改造的真实代价outputschema-要自己补一遍本节已订正)）。

### 4.2 复核推翻过的每一条

> 列出来是为了让读者知道**哪些结论曾经是错的**，以及**错在哪一类推理上**。
> 这些不是「调研员不认真」——**每一条都是有具体理由才被推翻的，而那些理由本身就是最有用的判定经验。**

#### 判定方向被推翻（4 条）

| Provider | 原判 → 改判 | 推翻理由 |
|---|---|---|
| **OneDrive / SharePoint** | 🔧 Managed REST → ✅ **Remote MCP**（**但仓库仍走 Managed，见下**） | **Notion 错误的重演。** 原论断「微软自营托管 MCP 只有三类，没有面向文件的第一方 MCP」是**事实错误** —— 有第四类 **Work IQ MCP servers**，明确覆盖 OneDrive/SharePoint 文件，约 35 个工具含完整增删改移复制。原报告用微软官方 Learn MCP 检索却漏掉了它；**复核用同一个检索工具检索到了全系文档 —— 不是资料不存在，是检索没做到位**。<br>⚠️ **本表的 ✅ 只推翻「协议层可不可用」，没有推翻「我们该走哪条」**：`one_drive` 命中 [blueprint §8.1](blueprint.md#81-降级到-managed-rest-的条件) 前三条降级条件（preview / 5MB 天花板 / M365 Copilot 许可证），**2026-07-25 决定继续走 Managed REST**，详见 [5.1](#51-现状对照2026-07-25-复核四条改造已完成)。这一条**不是**待办 |
| **Vercel** | ✅ Remote MCP → 🔧 **Managed REST** | 协议层四条全过，**但 client 审核是代码级强制的**。复核实际跑了 DCR 测试：localhost 回调 200、claude.ai 回调 200、**第三方托管回调 400 `invalid_redirect_uri`**。我们正是被拒的那一类 |
| **Braze** | ✅ Remote MCP → 🔧 **Managed REST** | 技术事实全对，**但原报告把两条决定性事实列为「风险」却没让它们改变结论**：Early Access 需联系客户经理开通（非自助）+ 只给非 PII 聚合数据（per-user 场景结构性够不到，最终还得写两套） |
| **Google Analytics 4** | 🔧 维持，但**清空 endpoint** | 判定对，但在一条 Managed 判定上填了一个**厂商零文档、连 PRM 都没有（404）** 的端点。写进记录会误导实现方去依赖一个随时可能消失、无 SLA 的 URL |

#### 降级为 ❓ 待验证（9 条）

| Provider | 降级理由（一句话） |
|---|---|
| **Bitbucket** | OAuth 路径已证实够不到 Bitbucket；仅剩的 API token 路径被厂商自己警告「部分工具不可用」且未公布幸存清单 |
| **Azure DevOps** | 文档明写「For now, only Visual Studio and Visual Studio Code are supported」，且 Entra 的 OIDC 配置里根本没有 `registration_endpoint` |
| **Salesloft** | 引用的 newsroom URL 是**软 404**（任意伪造 slug 也返回 200），citation 什么都证明不了；且 manifest 自报 `mcp_version: 2024-11-05`，是 Streamable HTTP 之前的 spec 版本 |
| **Dynamics 365 Sales** | 文档说的是「HTTP **(HTTP or Server-Sent Events)**」—— **构造性歧义而非确认**；且租户路径校验发生在认证挑战之前，探测拿不到任何握手信息 |
| **Clay** | 技术上其实比原报告更好（DCR 可用、POST-only），**但厂商自己的文档只描述本地 stdio 产品**，远程 URL 只有第三方目录佐证 |
| **Square** | 官方文档 12 次给出 `/sse`、8 次用 `npx mcp-remote`，**`/mcp` 与「Streamable HTTP」全页零出现**；且认证网关在传输层之前，`/mcp` 与 `/sse` 行为**逐字节相同**，401 无法区分 |
| **Xero** | 引用的「官方依据」是服务自己的落地页而非文档；端点是真的但**没有 AS metadata、没有 DCR**，AS 也没有 `registration_endpoint` —— 我们能不能用是未证实的 |
| **AWS S3** | 标准 1/2/3 成立，**但 AWS 官方文档全篇只讲 IAM/SigV4，不描述 bearer 路径**；且 AS 只支持 public client，与我们的 confidential-client 模型冲突 |
| **Replicate** | 厂商托管页上**每一条**客户端指引都指向 `/sse`；且 `/mcp` 缺席了所有在其他条目上确认 Streamable HTTP 的正面信号（PRM 是裸根路径、`www-authenticate` 无 `resource_metadata`、CORS 不暴露 `mcp-session-id`） |

#### 判定维持但关键字段被修正（6 条）

| Provider | 修正内容 |
|---|---|
| **Cisco Webex** | **原判「消息侧没有官方 MCP、需 Managed REST」是错的** —— Messaging MCP 存在，24 个工具。endpoint 应改为 `webex-messaging`。**但同时降置信度**：Cisco 全套文档通篇没出现 Streamable HTTP / SSE 字样，传输是反推的 |
| **Alibaba Cloud** | **原判「endpoint 必须每账号生成、无法硬编码」的前提被实测推翻** —— 固定路径 `/mcp` 本身就是活的公共端点。置信度 low → medium-high |
| **Box** | endpoint 钉死为**根域名**（PRM 自报 `"resource": "https://mcp.box.com/"`）；纠正「OAuth 2.1 + PKCE」→ 文档写的是 OAuth 2.0 且未提 PKCE；Enterprise Advanced 只针对 `docgen.readwrite` |
| **Coda** | 认证从 `bearer_pat` → **`oauth2`（含 DCR）**。原判基于搜索引擎摘要（help 页 403），**服务本体推翻了它** |
| **PayPal** | **官方文档写的 `/http` 路径实测四组组合全部 404** —— 用 `/mcp`。置信度 high → medium |
| **Amazon Ads** | 原称「OAuth 模式不需要 `Amazon-Ads-ClientId` header」**与官方 Required Headers 一节的无条件表述直接冲突**，且实测无法证伪。置信度 high → medium |

### 4.3 飞书 / Lark：结论冲突未决

**全表唯一的内部矛盾，且它是中文市场的 tier1 provider，必须单独处理。**

四个类别的调研员看的是同一台 server（`https://mcp.feishu.cn/mcp`），给出了**相反的结论**：

| 类别 | 判定 | 依据 |
|---|---|---|
| 通讯 / 协作 | 🔧 Managed REST | **读文档**：凭证走私有头 `X-Lark-MCP-UAT`/`TAT`，官方明说不是 Authorization header；且「完全不含 IM 消息/群聊」 |
| 文档 / 知识库 | 🔧 Managed REST | 同上 |
| **存储 / 文件** | ✅ Remote MCP | **实测**：`401` + `WWW-Authenticate: Bearer realm="mcp"`；PRM `authorization_servers=["https://accounts.feishu.cn/mcp"]`、`bearer_methods_supported:["header"]`，**且 `resource_documentation` 字段由服务器自己指回 open.feishu.cn 官方文档**。明确指出**存在两条鉴权路径**，自定义头那条只是**给开发者的另一种模式** |
| **项目管理** | ✅ Remote MCP (medium) | 同样实测到标准 Bearer；**且发现 IM read 工具确实存在**（搜群/搜消息/已读状态）—— 与「完全不含 IM」的说法矛盾 |

**冲突的成因很清楚**：判 Managed 的两位只读了文档，判 Remote 的两位做了实测。**实测证据更强**（尤其 `resource_documentation` 自指官方文档这一条，是官方性的一手证据）。

**但即使 Remote 方是对的，还有一个未解决的阻塞**：项目管理类别的调研员指出，**每个请求必须声明 `X-Lark-MCP-Allowed-Tools` 头** —— 这是 `MCPAuthBinding` 表达不了的（见 [5.3 节](#53-两个框架改动能翻盘一批条目)）。

➡️ **定档动作**：拿一个真实飞书租户，走完整 OAuth，对 `https://mcp.feishu.cn/mcp` 打 `initialize` + `tools/list`，确认 (a) 纯 `Authorization: Bearer` 是否被接受、(b) 不带 `X-Lark-MCP-Allowed-Tools` 是否可用、(c) IM 工具的实际覆盖面。**在此之前保持 ❓，不要排期。**

### 4.4 反向陷阱：看起来有 MCP、其实不能用

**这一类的数量比 4.1 那一类还多。** 记住这几种形态，能省掉大量白跑。

| 陷阱形态 | 典型 provider | 识别方法 |
|---|---|---|
| **只查文档、不执行 API** | Twilio、Coinbase CDP Docs、OpenAI、Perplexity docs、Amplitude docs、SendGrid（并入 Twilio docs server） | 官方文档里找 "documentation-only" / "does not execute API calls on your behalf" |
| **方向反了 —— 厂商做的是 MCP Client** | Zendesk、ElevenLabs Agents、Mistral Connectors、Groq tool-use、Perplexity/Comet | 看标题是不是「connect **to** MCP servers」 |
| **买家侧而非商家侧** | Shopify Storefront、BigCommerce Storefront | 看工具是 `search_catalog`/`update_cart` 还是 Admin CRUD |
| **本地 stdio 伪装成官方 MCP** | Google Ads、Adyen、Coinbase CLI、ElevenLabs、Groq、Perplexity、CircleCI、Docker Hub、QuickBooks、Clay、钉钉、腾讯云 COS、阿里云 OSS | 配置里出现 `"command"`、`npx`、`uvx`、`pipx` |
| **本地包与远程托管同名并存** | Netlify、monday、Klaviyo、Todoist、Supabase、W&B、Linear、Notion、Box、Wix、飞书 | **仓库 README 只写本地用法不代表没有远程版 —— 一定要去看厂商 docs 站** |
| **自建 remote ≠ 厂商托管** | Azure MCP Server、Terraform、Google Ads、CircleCI、WooCommerce、Jenkins | 文档在教你 `azd deploy` / 自己起 Docker |
| **商务/审核门槛挡住第三方** | Vercel、Azure DevOps、Braze、ClickUp、Slack、Ramp、Square、Gong、Salesforce、Asana | 找 "reviewed and approved"、"allowlist"、"Early Access"、"contact your account manager" |
| **能力面严重受限** | Plaid（只有运维诊断）、Gong（3 个只读工具）、Braze（非 PII 聚合）、Mercury（只读）、ClickHouse（强制 readonly）、Xero（只读 scope）、GA4（无 Data API） | **先跑 `tools/list` 再排期** |
| **认证不是 Bearer** | 飞书、Freshdesk、Bitbucket、Zoho、WooCommerce、Jenkins、Wix API key 模式、Adyen、Anthropic | 见 [5.3 节](#53-两个框架改动能翻盘一批条目) |

---

## 5. 已实现 provider 的复查结论

### 5.1 现状对照（2026-07-25 复核，四条改造已完成）

> 🔧 **订正声明：本节此前的版本已过时。** 它把 `notion` / `stripe` / `linear` / `gitlab`
> 列为 Managed REST 并标「可能选错了，合计可省 ≈8,050 行」。
> **这四个已于 2026-07-25 全部改写成 Remote MCP**，那一列结论不再成立 ——
> 下表是改写后的实测现状。**待办也随之变了**：不再是「要不要改造」，
> 而是「用 `mise run mcp-probe` 拉真实 `tools/list`，把近似 InputSchema 校准掉」——
> 而且范围是 **7 个 Remote Provider**，不止这四个（见 [§5.5](#55-remote-mcp-契约校准状态跟踪)）。

以下均为 `packages/connectors/` 实测数据（`<dir>/*.go`，排除 `_test.go`）：

| Connector | 当前实现 | 行数 | Tool 数 | OutputSchema 数 | 本表判定 | 结论 |
|---|---|---:|---:|---:|---|---|
| `stripe` | Remote MCP（official，固定 endpoint） | 361 | 5 | 0 | ✅ Remote MCP | ✅ **已改造**（2,287 → 361） |
| `googleads` | Remote MCP（**self-hosted**，配置 endpoint） | 380 | 2 | 0 | 🔧 Managed REST | ✅ **选得更好** —— 见 [5.2](#52-一条本表二分法没覆盖的第三条路) |
| `github` | Remote MCP（official，固定 endpoint） | 388 | 5 | 0 | ✅ Remote MCP | ✅ **选对了，是标杆** |
| `notion` | Remote MCP（official，固定 endpoint） | 414 | 10 | 0 | ✅ Remote MCP | ✅ **已改造**（3,296 → 414） |
| `gitlab` | Remote MCP（**self-hosted**，配置 endpoint） | 641 | 14 | 0 | ✅ Remote MCP | ✅ **已改造**（1,601 → 641） |
| `linear` | Remote MCP（official，固定 endpoint） | 707 | 12 | 0 | ✅ Remote MCP | ✅ **已改造**（2,463 → 707） |
| `gmail` | 混合（2 Remote + 2 Managed） | 491 | 4 | 2 | ✅ Remote MCP（Google Workspace MCP） | ✅ **形态合理**，已在用 `gmailmcp.googleapis.com/mcp/v1` |
| `onedrive` | Managed REST | 450 | 2 | 2 | ✅ Remote MCP（复核翻案） | ⚠️ **协议层可用，但按 blueprint §8.1 保持 Managed** —— 见下 |
| `datadog` | Managed REST | 1,469 | 5 | 5 | **本表未覆盖** | ⚠️ **需单独核实** —— 见下 |

**四条改造实测合计 9,596 → 2,123 行，减少 7,473 行（−78%），tool 数 27 → 41。**
Remote MCP tool 现共 50 个，Managed tool 9 个。

> ℹ️ 表里的 `OutputSchema 数` 是**整个 connector** 的声明数，因此 `gmail` 的 2 来自它的
> Managed 一半；**7 个 Provider 的 50 个 remote tool 声明数一律为 0**。这是**现状**，不是能力上限。
> Remote backend 同样可以声明 `OutputSchema`（引擎侧早已支持），详见
> [5.4 节的订正](#54-改造的真实代价outputschema-要自己补一遍本节已订正)。

#### 逐条说明

**四个改造后的共同待办：InputSchema 还是近似值。** tool 名来自厂商官方文档（可信），
参数由文档 / 开源 server 源码推导，**尚未对真实 endpoint 采样校准**。Remote backend 是
参数直通，**参数名或类型写错，上游会直接报错**。校准工具是 `mise run mcp-probe`
（`packages/api/cmd/connect-it-mcp-probe`，与生产走同一个 mcpclient，只读，只问 `tools/list`）。
详见 [blueprint §8.2](blueprint.md#82-tool-定义从哪来三条路成本递增)。
> ⚠️ **这个待办不止这四个。** 逐条核实 `definition.go` 后发现 `github` / `googleads` /
> `gmail` 的 remote schema 同样是近似值 —— **7 个 Remote Provider 全部未经 probe 校准**，
> 逐项状态见 [§5.5](#55-remote-mcp-契约校准状态跟踪)。

**`notion`（3,296 → 414，10 tool）** —— 见 [4.1 节](#41-头号案例notion已改造完成)。当初误判的成因是官方文档一句「does not support bearer token authentication」被字面理解。⚠️ 改造时踩到的真坑：Notion 有**三代 MCP，tool namespace 零重叠**，远程版是 `notion-search` / `notion-fetch` 这套带前缀的名字，不要拿开源本地版的 tool 名来改。

**`linear`（2,463 → 707，12 tool）** —— 认证最灵活的一个：OAuth 2.1（含 DCR）**或**直接 `Bearer <Linear API key>`，Definition 里两种 auth method 都声明了。还有 `/mcp/readonly` 只读变体可做 safe-mode。无套餐门槛。

**`stripe`（2,287 → 361，5 tool）** —— 走 `Authorization: Bearer <restricted key>`。⚠️ Connect 场景需要 `Stripe-Account` header，**框架目前无法注入附加 header，因此这个能力尚未覆盖**（见 [5.3](#53-两个框架改动能翻盘一批条目)）。

**`gitlab`（1,601 → 641，14 tool）** —— MCP 内置在 GitLab 本体的 REST 命名空间下，endpoint 走 `EndpointConfigField`（`mcp_url`），Provenance 是 `ProvenanceSelfHosted`。⚠️ **两条开通门槛必须做进错误提示**：管理员需把 GitLab Duo availability 设为 Always on / On by default；凭据必须是**带 `mcp` scope 的 OAuth access token**（`api` scope **不**蕴含 `mcp`，personal / project / group access token 一律不被 `/api/v4/mcp` 接受）。

**`onedrive`（450 行，判定翻案但保持 Managed）** —— 复核证实微软有 Work IQ 的 `mcp_OneDriveRemoteServer`，[3.10 节](#310-存储--文件) 有完整实测证据。**但按 [blueprint §8.1](blueprint.md#81-降级到-managed-rest-的条件) 前三条降级**：(a) preview，微软明写「aren't meant for production use」且工具名/参数可能变；(b) **所有文件读写硬性限制 ≤5MB —— 对文件连接器是实打实的功能天花板**；(c) 需 M365 Copilot 许可证 + Entra 应用注册 + 管理员逐 server 授权。且现有 Managed 实现只有 450 行，**改造收益本来就接近零**。
> ✅ 已完成的动作：onedrive 选择 Managed 的**理由**已从「微软没有第一方 MCP」（假前提）换成上面三条真理由。**假前提会让人永远不去复查；真理由会在门槛消失时自动触发复查。**

**`datadog`（1,469 行，本表未覆盖）** —— **本轮调研的 12 个类别里没有 Datadog 条目，至今仍未按四条标准核实过。**
> ⚠️ **不能因为表里没有就断定它没有官方 MCP。** 这正是 [第 6 节](#6-维护机制这份表会过时) 要防的错误。
> ➡️ **动作（未执行）**：单独按四条标准核实一次 Datadog（重点查 `docs.datadoghq.com` 有无托管 MCP endpoint，以及是不是又一个 docs-only server），然后把结果补进本表。

**`googleads` / `github` / `gmail`** —— 选择正确，无需改动。

### 5.2 一条本表二分法没覆盖的「第三条路」

`googleads` 值得单独说：**本表判它 🔧 Managed REST，但仓库里它是 380 行的 Remote MCP。这不矛盾，而是仓库找到了第三条路。**

Google Ads 官方 MCP 是 stdio + 只读，**没有 Google 托管的 endpoint** —— 按判定条件 2 出局。但官方文档提到：开 OAuth proxy 后可切 streamable-http，部署到**你自己的** Cloud Run。仓库的做法是：

```go
RemoteMCPServers: []connector.RemoteMCPServer{{
    Key: "self_hosted",
    Endpoint: connector.Endpoint{
        Source:         connector.EndpointConfigField,
        ConfigFieldKey: "mcp_url",
    },
    AuthBinding: connector.MCPAuthBinding{Scheme: "bearer"},
    Provenance:  connector.Provenance{Kind: connector.ProvenanceSelfHosted},
}}
```

即 **`ProvenanceSelfHosted` + `EndpointConfigField`：让用户自带自托管 endpoint**。380 行搞定了一个本该 1,500+ 行的 provider。`gitlab` 后来复用了同一条路（instance 级 endpoint + `ProvenanceSelfHosted`）。

**这条路适用于哪些条目？** 凡是「官方有 Streamable HTTP 实现、但厂商不托管」的：**Jenkins**（`/mcp-server/mcp`，还需 `scheme=basic`）、**Terraform**（官方 self-hosted remote）、**Azure MCP Server**、**CircleCI**（官方支持自建 docker remote）、**Kubernetes**、**WooCommerce**（若日后去掉 local proxy）。

⚠️ **代价要说清楚**：自托管路径把运维负担转嫁给了用户，开箱即用性远不如托管端点，**只适合本来就自建的技术型用户**。别拿它当托管 MCP 的通用替代。

### 5.3 两个框架改动能翻盘一批条目

已核实 `MCPAuthBinding` 只有 `Scheme` 与 `CredentialFieldByAuthMethod` 两个字段。两个小改动的 ROI 很高：

**(a) 支持 `scheme=basic`**

| 条目 | 收益 |
|---|---|
| **Jenkins** | 配合自托管 endpoint，从 ~2,000 行降到 ~400 行（调研员原话） |
| **Bitbucket** | 个人 API token（`Basic base64(email:token)`）路径打开，不必强制组织级 service account |
| Twilio / Freshdesk（Basic 侧） | 打开 Managed 之外的可能 |

**(b) 支持静态附加 header（名值对）**

| 条目 | 当前状态 | 收益 |
|---|---|---|
| **dbt** | ✅ 但被阻塞 | `x-dbt-prod-environment-id` 是**必填**，否则这条会退化成需要改框架 |
| **Close** | ✅ 但只读 | `Close-Scope` 决定读/写/破坏性权限，没有它 **Close 只能是只读的** |
| **Amazon Ads** | ✅ 但存疑 | `Amazon-Ads-ClientId` 可能必填（官方 Required Headers 一节的无条件表述） |
| **Stripe** | ✅ | `Stripe-Account` 打开 Connect 子账号场景 |
| **飞书 / Lark** | ❓ | `X-Lark-MCP-Allowed-Tools` 是 per-request 必需；且若支持可配置 header 名，**飞书可从 ~3,000 行降到 ~400 行**（调研员称「ROI 极高」），**钉钉/企业微信很可能同形态** |
| **Freshdesk** | 🔧 | 裸 API key 无 `Bearer ` 前缀 —— **若 binding 支持空 scheme，此条可直接翻案** |
| **GitHub / Azure DevOps** | ✅ | `X-MCP-Toolsets` / `X-MCP-Readonly` 等工具集裁剪头（当前非必需，但能收窄工具面） |

> 💡 **建议把这两个能力做成通用特性，而不是为单个 provider 硬编码。**
> 保守估计能直接影响 **8–10 条**记录的判定或能力上限。

### 5.4 改造的真实代价：OutputSchema 要自己补一遍（本节已订正）

> 🔧 **订正声明（2026-07-24）：本节此前的结论有误，已重写。若你基于旧版本做过判断，请重新读。**
>
> **旧结论（错的）**：改造 `notion` / `stripe` / `linear` / `gitlab` 是「省 8,050 行」
> 与「失去 100 个 `OutputSchema` 输出契约」的**二选一**；并把「Remote 型没有输出契约」
> 归因于框架的 `OutputMapperKey` 字段。
>
> **为什么错**：已读代码核实 —— `packages/service/exec/engine.go` 里
> `callManaged` 与 `callRemote` **收敛到同一个 `finish()`**，而 `finish()` 无条件调用
> `validateBackendResult(result, r.schemas.Output)`（`packages/service/exec/schema_pipeline.go`）。
> 也就是说：**只要 Definition 声明了 `OutputSchema`，Remote backend 的输出本来就会过校验，零引擎改动。**
> 现有 50 个 Remote tool 之所以没有输出契约保护，**不是架构不支持，是没人写。**
> 这条能力现在有测试钉住：`packages/service/exec/schema_pipeline_test.go` 的
> `TestOutputSchemaBindsRemoteBackendSameAsManaged`。
>
> **两个概念此前被混为一谈，务必分清**：
>
> | | 作用 | 现状 |
> |---|---|---|
> | `OutputSchema` | **校验**输出形状（不符即坍缩为 `invalid_response`） | ✅ **已支持，与 backend 类型无关**；只是 Remote 型没人声明 |
> | `OutputMapperKey` | **重塑**输出形状（做投影、改字段） | ❌ 未实现；`engine.go` 在 backend 触达前显式拒绝（「第一期仅支持参数直通」） |
>
> **结论修正为：行数节省和输出契约可以兼得。** 代价不是「失去契约」，而是
> **要为迁移后的每个 Remote tool 重新写一份 `OutputSchema`，且这份 schema 必须来自真实响应采样**
> —— 见本节末尾与 [模板指引](templates/provider/README.md#给-remote-mcp-tool-补-outputschema-的安全做法)。

实测数据（2026-07-25 现状，非应然；口径 = `definition.go` 里 `OutputSchema:` 的声明数）：

| 类型 | Connector | OutputSchema 声明数 |
|---|---|---:|
| Managed | `datadog` | 5 |
| 混合（Managed 侧） | `gmail` / `onedrive` | 各 2 |
| **Remote MCP** | `github` / `googleads` / `notion` / `stripe` / `linear` / `gitlab`，以及 `gmail` 的 remote 一半 | **全部 0**（可以声明，只是没声明） |

> ⚠️ 本表旧版给 `linear` 32 / `notion` 31 / `stripe` 19 / `gitlab` 18 / `datadog` 14 这组数
> —— 前四个描述的是**已删除的 Managed REST 实现**；`datadog` 的 14 是把常量名里的
> `OutputSchema` 字样一起数进去的**计数口径错误**，真实声明数是 5（每个 tool 一份）。

**两种形态的差别在「谁定形状」，不在「有没有契约」：**

- **Managed 型做了输出投影。** 我们自己解析上游响应、映射成固定形状，`OutputSchema` 钉死的是**我们造出来的**形状。上游 API 改字段时映射层会报错或要改代码，但**呈现给 Agent 的契约是稳定的**。
- **Remote MCP 型的形状由厂商决定，我们只能校验、不能重塑**（`OutputMapperKey` 未实现）。**没声明 `OutputSchema` 时**，厂商返回什么 Agent 就看到什么，**厂商改了输出形状，Agent 侧契约会静默漂移，而我们不会立刻知道**。声明了 `OutputSchema` 之后，同一个漂移会立刻变成一个显式的 `invalid_response` 失败 —— 从静默变成可观测。

**这个漂移风险不是理论的。** 本表里多家厂商明确警告过：

- 微软 Work IQ / Teams / SharePoint / Power BI：*"Microsoft might change preview MCP tool names and parameters. **Avoid hard-coded dependencies**"*
- Slite / Coda / Chargebee / Guru：官方标 beta，"tools 可能变"
- Trello：工具集「仍很早期」
- Egnyte：*"Available tools and capabilities vary based on your Egnyte plan"* —— **同一份 connector 在不同客户那里可用工具集不同**

**`notion` / `stripe` / `linear` / `gitlab` 的改造已于 2026-07-25 完成，实际的收益与代价：**

| | 收益 | 代价 |
|---|---|---|
| 行数 | **−7,473 行**（9,596 → 2,123，−78%） | — |
| 维护 | 厂商替我们维护 tool 实现，上游 API 变更不再需要改代码 | 厂商改工具名/参数/输出形状时**我们被动接受** |
| Agent 契约 | 可以保住：Remote tool **同样能声明 `OutputSchema`** | 原先的 100 个 Managed schema **随 Managed REST 实现一并删除，没有搬过来**（它们描述的是我们投影出来的形状，不是厂商 MCP 返回的形状）；要恢复输出契约必须**采样真实响应后重写** |
| 能力覆盖 | tool 数 27 → 41 | 工具面由厂商决定，**可能缺我们原先实现的某个具体能力** |
| 开通体验 | — | 多出厂商侧门槛（GitLab Duo 开关 + `mcp` scope、Notion 独立 OAuth app 等） |
| 参数准确度 | — | 四个的 InputSchema **目前是近似值**，未经 `mise run mcp-probe` 校准 |

> **「省行数」和「保输出契约」不冲突** —— 这就是本节订正的要点。
>
> 真正的成本项是**采样**——写 `OutputSchema` 需要先拿真实账号把该 tool 调用若干次、
> 看清厂商实际返回的形状。这一步无法靠读文档替代（多数厂商根本没文档化 MCP 输出形状）。
> 单个 tool 的 schema 只多几十行，**总量仍远低于 Managed 的 1,500–3,300 行**。

> 🚨 **安全红线：不要给现有的 50 个 Remote tool（`github` 5、`googleads` 2、`gmail` 2、
> `stripe` 5、`notion` 10、`linear` 12、`gitlab` 14）凭猜测补 `OutputSchema`。**
> 我们不知道这些厂商 MCP 实际返回什么形状。schema 写错一个字段，那个 tool 会在运行时
> 直接返回 `FailureInvalidResponse` —— **把一个本来能用的 tool 写死**。
> 补 schema 的前置条件是真实账号采样，流程见
> [模板指引：给 Remote MCP tool 补 OutputSchema 的安全做法](templates/provider/README.md#给-remote-mcp-tool-补-outputschema-的安全做法)。
> **宁可暂时不声明，也不要声明一个猜的。**

**剩余待办**（改造本身已完成，四个 Definition 现在缺的是**准确性**，不是行数）：

1. 对 endpoint 跑 `mise run mcp-probe` 拉 `tools/list`，**校准 InputSchema**
   （tool 名可信、参数是推导的；参数写错会在运行时被上游拒绝）。**范围是 7 个
   Remote Provider 而非这四个**，优先级与逐项状态见 [§5.5](#55-remote-mcp-契约校准状态跟踪)；
   同一份 probe 输出直接充当 [§6.3](#63-复查节奏) 漂移检测的基线快照。
2. 拿真实账号采样响应，**按需补 `OutputSchema`** —— 先在 `linear` 上走通
   「真实账号采样 → 归纳最小封闭 schema → real smoke 验证 → 写进 Definition」这条链，
   把工作量量化一次，再决定其余三个。**不采样就不要写。**

### 5.5 Remote MCP 契约校准状态跟踪

本节跟踪的是**仓库里已实现的 Remote MCP Provider**，与 §3 那 139 个调研条目不是一回事。
它回答一个 §5.1 的散段落回答不了的问题：**哪几个 Provider 的 Tool 契约还没对真实
`tools/list` 核过，因此随时可能在运行时被上游拒绝。**

下表每一格都逐条核实自 `packages/connectors/<p>/definition.go` 的注释与 schema 实体。

| Provider | 形态 | remote tool 数 | tool 名来源 | InputSchema 来源 | 已 probe 校准？ | OutputSchema |
|---|---|---:|---|---|:---:|---:|
| `gitlab` | Remote（self-hosted） | 14 | **双重印证**：上游源码 `app/services/mcp/tools/manager.rb` × 官方文档 `doc/user/model_context_protocol/mcp_server_tools.md`（`definition.go:94`） | 由上游 **Grape params 白名单反推**（`definition.go:162`），注释自称「推导结果」 | ❌ | 0 |
| `linear` | Remote（official） | 12 | **两个第三方 client 对同一 endpoint 的 live `tools/list` 交叉印证**（`fprochazka/linear-mcp-cli` 由 live 列表生成命令、`openclaw/mcporter` 发布过 `mcporter list linear` 快照） | **重建的近似，非 verbatim** —— 包注释原文：property 名可信，**类型与 required 集合是推断的** | ❌（例外：`create_comment` 的 `issueId`/`body`/`parentId` 采自 live server） | 0 |
| `notion` | Remote（official） | 10 | 官方托管远程 server 文档（带 `notion-` 前缀那一代） | **零公开参数契约** —— 官方只给描述和示例 prompt；properties 只写文档里逐字出现过的参数 | ❌ | 0 |
| `stripe` | Remote（official） | 5 | `docs.stripe.com/mcp` 官方 tool 表格（上游 13 个，暴露 5 个） | 官方**只公布 tool 名、不公布 input schema**；properties 只列已确认项 | ❌ | 0 |
| `github` | Remote（official） | 5 | **`definition.go` 未标注出处** —— 只写「5 个固定映射的官方 remote tool」 | 同上未标注，只声明「schema 宽松、参数直通、以上游为准」 | ❌ | 0 |
| `googleads` | Remote（self-hosted） | 2 | 官方开源实现 `googleads/google-ads-mcp`（`definition.go:123`） | 宽松近似（同上） | ❌ | 0 |
| `gmail`（remote 部分） | 混合 | 2 | **`definition.go` 未标注出处** | 宽松近似（`definition.go:92`） | ❌ | 0（另 2 个 Managed tool 各有 1 份） |

> **一眼结论：`已 probe 校准` 一列全是 ❌ —— 7 个 Remote Provider、50 个 remote tool，
> 没有任何一个的 InputSchema 对真实 `tools/list` 核对过。**

**这修正了 §5.1 的一个表述偏差。** §5.1 把「InputSchema 还是近似值」写成
`notion`/`stripe`/`linear`/`gitlab` **四个改造后的共同待办**；实际核实下来，
`github`/`googleads`/`gmail` 这三个原生 Remote Provider 的注释同样写着「宽松近似，
以 tools/list 为准」。**欠校准的是 7 个，不是 4 个**，只是前四个的 tool 数占了大头。

**校准缺口最严重的地方（按 schema 实体清点，不是按注释语气）：**

| 现象 | 实测 | 后果 |
|---|---|---|
| `"properties": {}` 的 tool | `notion` **7 / 10**、`stripe` 4 / 5、`github` 1 / 5、`googleads` 1 / 2 | 其中 `github.get_me`、`googleads.list_accessible_customers`、`stripe.get_stripe_account_info`、`stripe.get_balance_summary` 确实无参数，**是正常的**；但 `notion.update_page` / `create_database` / `create_comment` / `query_data_sources` 与 `stripe.search_stripe_resources` / `fetch_stripe_resources` **上游明确收参数**，Agent 却拿不到任何参数提示，只能盲试 |
| 声明了 `required` 的 tool | `gitlab` 最多，`notion` **0 个** | `required` 是猜的比没有更危险（[blueprint §8.2](blueprint.md#82-tool-定义从哪来三条路成本递增)：「猜出来的 schema 比没有 schema 更糟」）；`notion` 干脆一个都不声明，是**诚实的**处理 |

⚠️ **两个工具名字像、能力完全不同，不要混为一谈（已读代码核实）：**

| | 是什么 | 实际做什么 | 能否校准 schema |
|---|---|---|---|
| `mcp:verify` | **生产 admin 端点** `POST /admin/connectors/{type}/mcp:verify`（`packages/api/handlers_verify.go` → `packages/service/catalogsvc/verify.go`） | 配置期用**空 bearer** 握手，把上游 `tools/list` 的**名字集合**与 Definition 里每个 `RemoteMCPBackend.RemoteToolName` 比对，缺一个就写 health 失败并拒绝 | ❌ **只看名字，完全不看 schema** |
| `mise run mcp-probe` | 开发期 CLI（`packages/api/cmd/connect-it-mcp-probe`），与生产同一个 `mcpclient`，只读 | 打印上游每个 tool 的**完整声明**：`name` / `description` / `inputSchema` | ✅ **唯一能校准 schema 的路径** |

因此 `definition.go` 里多处写的「以 `mcp:verify` 对照 `tools/list` 为准」**高估了那个端点** ——
它挡得住 tool 改名/下线，挡不住参数改名。**校准必须跑 `mise run mcp-probe`。**

**建议顺序**（先拿信息量最大的，不是先拿行数最多的）：

1. `notion`（7 个空 properties，且**没有任何公开文档可替代 probe**）
2. `stripe`（`search_stripe_resources` / `fetch_stripe_resources` 两个核心读工具零参数提示）
3. `gitlab` → `linear`（schema 是推导/重建的，但至少 property 名有依据）
4. `github` / `googleads` / `gmail`（tool 数少，且注释未标出处，顺带把来源补进注释）

probe 拿到真实 `inputSchema` 之后，**同一份输出就是 [§6.3](#63-复查节奏) 漂移检测的基线快照** ——
两件事跑同一条命令，不要分两次拉。

---

## 6. 维护机制：这份表**会**过时

> **MCP 生态变化极快。这份表的保质期以季度计，不是以年计。**

### 6.1 活生生的例子

- **Notion** —— 我们的印象是「没有 MCP，得手写」，于是写了 3,296 行。**印象过时了**，2026-07-25 改写成 414 行 Remote MCP。
- **OneDrive** —— 第一轮调研结论是「微软没有第一方文件 MCP」，**复核当场推翻**（判定翻案，但因 preview + 5MB 上限仍走 Managed）。
- **Height** —— 出现在候选名单里，但产品**2025-09-24 就已经关停了**，公司清算。纯粹是训练数据过时。
- **Wrike** —— 多数第三方文章只提 SSE URL，照着看会**错误否决**；厂商文档里其实两个都有。
- **Square / Xero / Terraform / Clay** —— 端点已经上线跑起来了，**厂商文档还没跟上**。
- **Vercel / Azure DevOps** —— 协议全部就绪，**商务门槛把第三方挡在外面**（这类门槛可能随时打开，也可能长期不开）。

**这六种过时方式各不相同，但结果一样：按旧印象排期，代价是几千行代码。**

### 6.2 记录规范

**每条记录必须带 `verifiedOn` 日期。** 本轮全部为 **2026-07-24**（部分复核延续至 2026-07-25）。

新增或更新记录时，至少记录：

| 字段 | 要求 |
|---|---|
| `verdict` | ✅ / 🔧 / ⛔ / ❓ |
| `transport` | 必须能指出**厂商文档的原文**或**实测证据**，不能写「推断」而不标注 |
| `auth` | 明确凭据以什么 header + 什么 scheme 承载 |
| `endpoint` | **只填厂商官方文档记载的 URL**。未文档化的活端点写进 notes，**不要填进 endpoint 字段**（见 GA4 与 Clay 的处理） |
| `officialDoc` | **必须是厂商文档页，不能是服务自己的落地页**（见 Xero），**且要验证它不是软 404**（见 Salesloft） |
| `verifiedOn` | 日期 |
| `confidence` | high / medium / low —— **low 或证据不足一律标 ❓，不要为了填满表格而猜** |

### 6.3 复查节奏

| 对象 | 频率 | 重点 |
|---|---|---|
| **tier1 全部条目** | **每季度一次** | 尤其是 🔧 和 ❓ 的，看是否已上线合规的托管端点 |
| tier2 / tier3 | 每半年，或有客户需求时 | — |
| **标了「值得设复查提醒」的条目** | 每季度 | Twilio、SendGrid、Zoho CRM（约 2 季度）、CircleCI、Braze、Freshdesk、Vercel、Azure DevOps、Cloudflare R2 |
| **preview / beta 条目** | 每季度 | Work IQ 全系、Power BI、Looker、Google Workspace MCP、Dropbox、ClickUp、Front、Square、Amazon Ads、AWS MCP、Trello |
| ❓ 待验证条目 | **动手前必查** | 见 [第 7 节](#7-待人工跟进的条目) |
| **🆕 已实现的 Remote MCP Provider 的 `tools/list` 漂移** | **每月一次**，另在上游 GA / 改版公告后立即补一次 | 7 个 Provider（`github` / `gitlab` / `linear` / `notion` / `stripe` / `googleads` / `gmail` 的 remote 部分），见下方说明 |

#### 为什么单独加这一行

上面五行盯的都是**表里的调研条目**（尚未实现的候选），失败模式是「排期估错」；
这一行盯的是**已经跑在生产里的 7 个 Provider**，失败模式是「Agent 调用当场失败」。
两者是不同的轴 —— 这正是它此前从复查节奏里漏掉的原因。
要求来自 [blueprint §8.4](blueprint.md#84-配套漂移检测)：走 Remote MCP 的代价是厂商改
tool 形状我们不会知道，所以要**把变化转成可评审的 CI 信号，而不是等运行时失败**。

- **检测对象**：7 个 Remote Provider 共 50 个 remote tool（清单与现状见 [§5.5](#55-remote-mcp-契约校准状态跟踪)）。
- **手段**：`mise run mcp-probe -- -endpoint <url> -token <token>` 拉 `tools/list`，
  与提交入库的快照做 diff。**只读**（只问 `tools/list`，不调用任何 tool），
  出网走与生产完全相同的 `providerkit` 受控管道。
- **基线现状**：⚠️ **仓库里目前没有任何已提交的 `tools/list` 快照，基线是空的。**
  所以第一次跑不是 diff、而是**建基线**，且它与 [§5.5](#55-remote-mcp-契约校准状态跟踪)
  的 schema 校准是同一条命令的同一份输出 —— **一次跑完，两件事都办了。**
- **频率取月而非季**：这几个上游多为 beta / preview（Notion 远程版、Google Workspace MCP、
  GitLab MCP 18.6 起 beta），改动节奏比调研条目快；而单次成本是每个 endpoint 一次只读调用。
- **`mcp:verify` 不能替代它**：那个端点只比对 tool **名字**是否还在，
  参数改名它一无所知（见 [§5.5](#55-remote-mcp-契约校准状态跟踪) 的对照表）。
  它是漂移检测的**下限**，不是全部。

> ✅ **与 [blueprint §1「明确不做：动态拉上游 `tools/list` 生成工具」](blueprint.md#1-定位它是什么不是什么) 不冲突。**
> 那条禁的是**运行时动态生成工具** —— 上游改什么，Agent 侧契约就跟着静默改，
> 没人评审、也没人知道。这里做的是**构建期拉取 → 快照提交入库 → 走 review → 才进 Definition**：
> 上游的每一次改动都变成一个人看得见、可拒绝的 diff。**方向恰好相反**，
> blueprint §8.4 末尾已逐字写明这一点。

### 6.4 硬性规则

> ## 🚨 新增 provider 前，**必须重新核实该条目**，而不是直接信表格。
>
> 表格的作用是**告诉你从哪里开始查、以及历史上踩过哪些坑**，
> **不是**免除你核实的责任。

核实的最小动作（约 10 分钟）：

1. 打开表里的 `官方依据` URL，确认它**还活着**、**还是那个说法**、**不是软 404**（用一个伪造 slug 试一下）。
2. 对 endpoint 发一个 `initialize` 的 JSON-RPC POST，带 `Accept: application/json, text/event-stream`。
   - `401` + `WWW-Authenticate: Bearer` + `resource_metadata` → 良性信号
   - `200` + `mcp-session-id` 头 → Streamable HTTP 强证据
   - `404` / `405` → 路由不存在
3. 拉 `/.well-known/oauth-protected-resource[/path]`，确认 `bearer_methods_supported` 含 `header`，且 `resource` 精确指向你要用的 URL（**不是裸根路径** —— 见 Replicate）。
4. 检查厂商文档里有没有 `/sse`、`npx mcp-remote`、`"command"` 这些字样 —— **它们出现得越多，越可能不是 Streamable HTTP**（见 Square 的 12 次 `/sse`）。
5. **能拿到真 token 就跑一次 `tools/list`** —— 很多条目的致命问题（Plaid 只有诊断工具、Gong 只有 3 个工具、Braze 只有聚合数据）**只有这一步能发现**。

---

## 7. 待人工跟进的条目

**调研日期：2026-07-24**（对抗性复核延续至 2026-07-25）

下列 **13 条**标记为 ❓ **待验证**，**禁止直接排期**。每条都附了具体的下一步动作：

| # | Provider | 类别 | 卡在哪一条 | 下一步动作 | 成本 |
|---|---|---|---|---|---|
| 1 | **飞书 / Lark** | 通讯 / 文档 / 存储 / PM | 条件 4（内部结论冲突） | 真实租户走完整 OAuth，`initialize` + `tools/list`，确认纯 Bearer 是否被接受、`X-Lark-MCP-Allowed-Tools` 是否必需、IM 覆盖面 | 中（需租户）|
| 2 | **QuickBooks Online** | 支付 / 财务 | 条件 1+2（托管端点存否未定） | 从非数据中心 IP / 真实浏览器访问 `ai-inc.quickbooks.intuit.com/v1/mcp`；查 Claude/ChatGPT connector 目录；提 Intuit ticket | 低 |
| 3 | **Square** | 支付 / 财务 | 条件 3（文档 SSE-only）+ redirect 白名单 | 拿真实 Square token 对 `/mcp` 打 `initialize`，确认是否协商 Streamable HTTP；同时向 Square 申请文档化 | 中 |
| 4 | **Xero** | 支付 / 财务 | 条件 1（无文档）+ 授权链未证实 | 确认手工注册的 Xero app 能否签发 `mcp.xero.com` 接受的 audience token；**注意 scope 只读，写入场景直接走 REST** | 中 |
| 5 | **AWS S3** | 存储 | 条件 4（bearer 路径未验证） | PoC：证明纯 bearer 能拿到某客户账号的 S3 权限。**⚠️ 分支是 ✅ 或 ⛔，没有 Managed 中间选项**（S3 REST 强制 SigV4） | 高 |
| 6 | **Bitbucket** | 开发工具 | 条件 4（仅 Basic / 组织级凭据） | 30 分钟 spike：建 service account API key，对 `/v1/mcp` 打 `tools/list`，看 Bitbucket 工具是否出现 | **低（30 min）** |
| 7 | **Azure DevOps** | 开发工具 | 条件 4（DCR 未开放） | Entra spike：验证预注册多租户应用能否取得可用 token。**失败则按 Managed REST 排期** | 中 |
| 8 | **Replicate** | AI / ML | 条件 1+3 | **5 分钟**：拿真实 `r8_` token 对 `/mcp` 打 `initialize` | **极低（5 min）** |
| 9 | **Terraform** | 云 / 基础设施 | 条件 1（端点活但无文档） | 拿真实 HCP Terraform token 打 `initialize` + `tools/list`，**并向 HashiCorp 确认该端点是否受支持** | 低 |
| 10 | **Clay** | CRM | 条件 1（厂商只文档化 stdio） | 走一次真实 OAuth + `tools/list`；**并复查 Clay 是否已发布远程文档** | 低 |
| 11 | **Salesloft** | CRM | 条件 1（无有效文档）+ 3（spec 版本存疑） | 找到真实厂商技术文档；确认传输。**已知：只读、支持 DCR** | 中 |
| 12 | **Dynamics 365 Sales** | CRM | 条件 3（文档歧义，无法探测） | 真实租户 spike，**聚焦 Dataverse endpoint**（做 CRUD 的是它）。⚠️ 注意 Copilot Credits 计费 | 高 |
| 13 | **TikTok Ads** | 电商 / 广告 | 全部未知（网络受阻） | **换一个能访问 tiktok.com 的网络**打开官方页面，确认 endpoint / transport / 是否纯 Bearer（⚠️ TikTok Ads API 传统上用 `Access-Token` 自定义头） | 低 |

### 建议优先处理（成本极低、影响明确）

> **Replicate（5 分钟）、Bitbucket（30 分钟）、QuickBooks、Terraform、Clay** —— 这五条都能在一小时内定档。
> 其中 **Replicate 和 Bitbucket 是纯粹的「跑一次命令」问题**，没有租户或商务前置条件。

### 另需单独核实（不在本轮调研范围内）

| Provider | 原因 |
|---|---|
| **Datadog** | 本仓库已有 1,469 行 Managed 实现，**但本轮 12 个类别都没有 Datadog 条目** —— 表里没有 ≠ 没有官方 MCP。**必须按四条标准单独核实一次，重点查是不是又一个 docs-only server。** |

---

## 附：本表统计

**决策表共 157 条记录，去重后 139 个 provider**（跨类别重复的如 Notion、Stripe、Atlassian、飞书等只算一个）。

| 判定 | 表内记录数 | 去重后 provider 数 | 占比 |
|---|---:|---:|---:|
| ✅ **Remote MCP** | 98 | **85** | **61%** |
| 🔧 **Managed REST** | 41 | **39** | 28% |
| ❓ **待验证** | 16 | **13** | 9% |
| ⛔ **Blocked** | 2 | **2** | 1% |
| **合计** | **157** | **139** | 100% |

**按常见度分布**（表内记录数）：tier1 = 62，tier2 = 73，tier3 = 22。

**按类别分布**（表内记录数）：

| 类别 | 条数 | 类别 | 条数 |
|---|---:|---|---:|
| 3.1 开发工具 / 代码托管 | 12 | 3.7 数据 / 分析 | 13 |
| 3.2 通讯 / 协作 | 12 | 3.8 项目管理 / 生产力 | 15 |
| 3.3 文档 / 知识库 | 16 | 3.9 客服 / 营销 / 邮件 | 14 |
| 3.4 CRM / 销售 | 13 | 3.10 存储 / 文件 | 12 |
| 3.5 支付 / 财务 | 14 | 3.11 AI / ML 平台 | 12 |
| 3.6 云 / 基础设施 | 10 | 3.12 电商 / 广告 | 14 |

> **一个值得注意的结论：61% 的 provider 有可直接使用的官方托管 Remote MCP。**
> 也就是说，**默认假设应该是「先去查有没有官方 MCP」，而不是「先开始写 Managed REST」** ——
> 这条已经写进 [blueprint §8](blueprint.md#8-provider-形态选型remote-mcp-优先) 成为默认路线。
> 本仓库 9 个已实现 provider 中曾有 4 个（notion / stripe / linear / gitlab）落在这 61% 里
> 却走了 Managed REST 路径，**2026-07-25 已全部改写为 Remote MCP，实测省下 7,473 行**。
> 现在 9 个里 6 个纯 Remote、1 个混合、2 个 Managed（`onedrive` 因 preview + 5MB 上限，
> `datadog` 因本表未覆盖、尚未核实）。

---

**调研日期：2026-07-24** ｜ **对抗性复核：2026-07-24 / 25** ｜ **仓库现状复核：2026-07-25** ｜ **下次 tier1 复查建议：2026-10**
