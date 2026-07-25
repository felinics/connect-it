# Provider 出站网络安全

> 最后核实：2026-07-25（本文的数值边界逐条对过 `packages/core/providerkit`：
> `url.go` 的 `maxProviderURLBytes` 16 KiB、`maxProviderPathBytes` 8 KiB、
> `maxPathUnescapeIterations` 32，以及 `transport.go` 的 `Proxy: nil`）。
> 入站边界与整体安全模型见 [blueprint §6](blueprint.md#6-安全模型纵深)。

`packages/core/providerkit`的受控 HTTP transport 是进程内 Provider 出网的唯一边界。
现存 Managed Provider、credential validator、OAuth token/profile/refresh 和
Remote MCP 全部走这条边界。进入该边界的流量默认只访问经过精确 canonical
origin 校验的公网 HTTPS endpoint。新增或修改任何 Provider 出站路径也必须由
composition root 注入的`providerkit.Factory`构造，不能重新引入旁路 client。

两条子路径的入口不同、守卫相同：一次性请求走`Client.Do`；Remote MCP 需要
`*http.Client` 才能流式收 SSE，因此`packages/service/mcpclient`用同一个 Client 的
`HTTPClient(authorizer, labels)`。后者的 redirect 由受控 RoundTripper 自己处理而
不交给`net/http.Client`，避免原始 Location 落进`*url.Error`；总 timeout 同时覆盖
DNS、dial、redirect、response header 与流式读取。

## 默认边界

- 不使用 `http.DefaultClient`、`http.DefaultTransport`或环境代理；
- 忽略 `HTTP_PROXY`、`HTTPS_PROXY`和 `NO_PROXY`；
- DNS 的全部解析结果都必须通过 IP 分类，并把已验证地址直接用于连接，避免二次解析；
- 规范化前的 absolute URL 最多 16 KiB、path 最多 8 KiB；path 最多递归解码
  32 层，超过上限按非法输入拒绝，避免深层 percent-encoding 绕过路径检查；
- 拒绝 loopback、link-local、metadata、multicast、unspecified、reserved、
  documentation、benchmark、CGNAT 和默认私网地址；IPv6 公网地址还必须命中
  IANA global-unicast registry 当前明确列出的 allocated prefix，`2000::/3`内未列出
  的空间同样按 reserved 拒绝，不能只依赖语言运行时的宽泛分类；
- redirect 每跳重新校验 origin、DNS 和 IP，永远拒绝 HTTPS 降级；
- 跨 origin redirect 必须单独列入 allowlist，只允许无 body 的 GET/HEAD；
- authorizer 每个 request attempt 只执行一次；首次跨 origin 前的 same-origin redirect
  从初始请求的受声明 credential footprint 快照恢复 header/query credential，不重新
  执行 authorizer；
- credential footprint 在首次跨 origin 后永久移除，且任何 redirect 都不发送
  `Referer`；首次跨 origin 后 header 只保留 `Accept`、`Accept-Language`、
  `Cache-Control`和`User-Agent`，包括`Mcp-Session-Id`在内的未知或协议专用 header
  一律删除；
- response 同时检查声明长度和实际解压后读取字节数。

默认不支持通过环境代理访问 Provider。若部署必须使用可信 egress proxy，需要先为
代理本身增加独立的固定 endpoint、证书、DNS/IP 和审计设计，不能只设置
`HTTP_PROXY`。

## Self-hosted opt-in

部署级开关默认均为 `false`：

```text
CONNECT_IT_ALLOW_PRIVATE_PROVIDER_NETWORK=false
CONNECT_IT_ALLOW_INSECURE_PROVIDER_HTTP=false
```

值只接受精确小写 `true` 或 `false`。环境变量只是部署端的外层批准，不能单独放宽
任意 Provider：

1. Provider Definition 必须明确支持 self-hosted；
2. 规范化 Policy 必须是 `SelfHostedOptIn`；
3. Connection／Connector 配置中的 endpoint 必须形成精确 canonical origin；
4. 访问私网还必须设置 `CONNECT_IT_ALLOW_PRIVATE_PROVIDER_NETWORK=true`；
5. 使用明文 HTTP 还必须同时由 Connection／Connector 配置显式允许，并设置
   `CONNECT_IT_ALLOW_INSECURE_PROVIDER_HTTP=true`。

固定 SaaS、`PublicOnly` Policy、Agent Tool arguments 都不能使用这些开关。即使开放
私网，localhost、link-local、云 metadata、multicast、unspecified 和 reserved 地址
仍永久禁止；HTTPS → HTTP redirect 也始终禁止。

## 发布检查

- 生产环境保持两个开关为 `false`，除非已有受审 self-hosted Provider；
- 不把 token、API key、完整 URL query 或请求／响应 body 写入日志；
- `docker/nginx.conf` 对 authorization/install 的 begin、reauth 和 callback
  路径关闭 access log，避免 state、code、HMAC 等 query proof 落盘；生产环境若在
  Nginx 前还有 LB／Ingress，也必须对同一组路径禁记 query string；
- 新 endpoint 必须在 Definition 与 Provider 文档中记录官方来源和 reviewed date；
- 任何尚未迁入`providerkit`的例外出站路径都必须在发布前明确列出并阻止发布；
- 回归验收覆盖 Managed Provider、validator、OAuth 和 Remote MCP 的实际调用链，
  持续证明 Provider 出站 HTTP 统一受控；
- Provider runtime 变更执行 `go test -race ./...`（`packages/core`）；
- Docker Compose 中显式保留两个 `false`，避免部署工具继承宿主机意外值。
