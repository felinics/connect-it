// connect-it-mcp-probe 对一个 Remote MCP endpoint 调一次 tools/list，把上游每个
// tool 的完整声明（name / description / inputSchema）打印成 JSON。
//
// 它存在的唯一理由：Remote backend 是参数直通，我们 Definition 里的 InputSchema
// 必须逐字复现上游契约，不一致就会在运行时失败。而厂商文档普遍只描述能力、不列
// tool 名与参数（Linear 的 MCP 文档就是如此），所以唯一可靠的来源是对真实
// endpoint 问一次。新增或迁移 Remote MCP Provider 前先跑它，把输出抄进
// definition.go，而不是照着文档猜。
//
// 这是开发期工具，不进生产镜像，也不写数据库。出网走的是与生产完全相同的
// providerkit 受控管道，因此同样受 origin 策略、DNS-pin、重定向剥离等约束。
//
// 用法：
//
//	connect-it-mcp-probe -endpoint https://mcp.linear.app/mcp -token "$LINEAR_API_KEY"
//	connect-it-mcp-probe -endpoint https://mcp.notion.com/mcp -token "$NOTION_TOKEN" -names-only
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/service/mcpclient"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("connect-it-mcp-probe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("endpoint", "", "Remote MCP endpoint（Streamable HTTP）")
	token := flags.String("token", "", "Bearer token：OAuth access token 或 PAT/API key")
	namesOnly := flags.Bool("names-only", false, "只打印 tool 名字，便于先看清单")
	timeout := flags.Duration("timeout", 30*time.Second, "单次握手加列举的总超时")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *endpoint == "" || *token == "" {
		fmt.Fprintln(stderr, "必须提供 -endpoint 与 -token")
		flags.Usage()
		return 2
	}

	factory, err := providerkit.NewFactoryFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "解析 Provider 网络策略: %v\n", err)
		return 1
	}
	client, err := mcpclient.New(factory)
	if err != nil {
		fmt.Fprintf(stderr, "装配 Remote MCP runtime: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// probe 面对的是一个尚未写进任何 Definition 的 endpoint，所以这里现造一份
	// 最小 server 描述。AllowedHostnames 留空表示只允许 endpoint 自身的 host，
	// 与生产同样不接受任意重定向目标。
	definitions, err := client.ListToolDefinitions(ctx, mcpclient.ListRequest{
		ConnectorType:   connector.Type("probe"),
		Operation:       mcpclient.OperationVerify,
		AuthorizationID: "probe",
		Server: connector.RemoteMCPServer{
			Key: "probe",
			Endpoint: connector.Endpoint{
				Source: connector.EndpointFixed,
				URL:    *endpoint,
			},
			AuthBinding:    connector.MCPAuthBinding{Scheme: "bearer"},
			RequestTimeout: *timeout,
		},
		Endpoint:    *endpoint,
		BearerToken: *token,
	})
	if err != nil {
		// 这里刻意不打印 token，也不回显上游 body：错误已被 mcpclient 收敛成安全信息。
		fmt.Fprintf(stderr, "tools/list 失败: %v\n", err)
		return 1
	}

	if *namesOnly {
		for _, definition := range definitions {
			fmt.Fprintln(stdout, definition.Name)
		}
		fmt.Fprintf(stderr, "\n共 %d 个 tool\n", len(definitions))
		return 0
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(toolsView(definitions)); err != nil {
		fmt.Fprintf(stderr, "编码输出: %v\n", err)
		return 1
	}
	fmt.Fprintf(stderr, "\n共 %d 个 tool\n", len(definitions))
	return 0
}

// toolView 是给人读、也给人抄的形状：字段名与 connector.Tool 对齐，方便直接
// 搬进 definition.go。
type toolView struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema,omitempty"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

func toolsView(definitions []mcpclient.ToolDefinition) []toolView {
	views := make([]toolView, 0, len(definitions))
	for _, definition := range definitions {
		views = append(views, toolView{
			Name:         definition.Name,
			Description:  definition.Description,
			InputSchema:  definition.InputSchema,
			OutputSchema: definition.OutputSchema,
		})
	}
	return views
}
