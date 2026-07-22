package connector_test

import (
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

// 类型包无行为，测试锁定两件事：tagged union 可被 type switch 区分；零值可用。
func TestToolBackendTaggedUnion(t *testing.T) {
	tools := []connector.Tool{
		{ID: "a", Backend: connector.RemoteMCPBackend{ServerKey: "s", RemoteToolName: "r"}},
		{ID: "b", Backend: connector.ManagedBackend{HandlerKey: "h"}},
	}
	var kinds []string
	for _, tool := range tools {
		switch tool.Backend.(type) {
		case connector.RemoteMCPBackend:
			kinds = append(kinds, "remote")
		case connector.ManagedBackend:
			kinds = append(kinds, "managed")
		default:
			t.Fatalf("tool %s: 未知 backend", tool.ID)
		}
	}
	if kinds[0] != "remote" || kinds[1] != "managed" {
		t.Fatalf("got %v", kinds)
	}
}

func TestDefinitionZeroValue(t *testing.T) {
	var def connector.Definition
	if def.Deprecated || len(def.Tools) != 0 {
		t.Fatal("零值 Definition 应为空且未弃用")
	}
}
