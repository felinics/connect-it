package mcpclient

import (
	"fmt"

	"github.com/memohai/connect-it/packages/core/connector"
)

// PreflightDefinitions constructs every fixed Remote MCP policy without
// performing network I/O. Production calls it after registry registration so
// a reviewed static endpoint that cannot satisfy the runtime egress boundary
// fails startup instead of waiting for the first tool call.
func (client *Client) PreflightDefinitions(
	definitions []connector.Definition,
) error {
	for _, definition := range definitions {
		for _, server := range definition.RemoteMCPServers {
			if server.Endpoint.Source != connector.EndpointFixed {
				continue
			}
			policyClient, err := client.policyClient(
				definition.Type,
				server,
				server.Endpoint.URL,
				"false",
			)
			if err != nil {
				return fmt.Errorf(
					"connector %q remote MCP server %q policy: %w",
					definition.Type,
					server.Key,
					err,
				)
			}
			policyClient.CloseIdleConnections()
		}
	}
	return nil
}
