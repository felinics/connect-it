package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

// tool_allowlist 的三态直接决定授权范围：省略＝当前 read Tool，[]＝零 Tool，
// null＝拒绝。这里按线上请求体形状钉住解码结果。
func TestToolAllowlistPreservesWireTriState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		wantNull  bool
		wantTools []string
	}{
		{"omitted", `{"connections":{}}`, false, nil},
		{"null", `{"connections":{},"tool_allowlist":null}`, true, nil},
		{"empty", `{"connections":{},"tool_allowlist":[]}`, false, []string{}},
		{"values", `{"connections":{},"tool_allowlist":["gh__read"]}`, false, []string{"gh__read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request createMCPSessionRequest
			if err := json.Unmarshal([]byte(tc.body), &request); err != nil {
				t.Fatal(err)
			}
			if request.ToolAllowlist.Null != tc.wantNull ||
				!reflect.DeepEqual(request.ToolAllowlist.Tools, tc.wantTools) {
				t.Fatalf("tool_allowlist = %+v", request.ToolAllowlist)
			}
		})
	}
}

func TestToolAllowlistFieldRejectsNonStringArray(t *testing.T) {
	var request createMCPSessionRequest
	if err := json.Unmarshal(
		[]byte(`{"connections":{},"tool_allowlist":["ok",1]}`),
		&request,
	); err == nil {
		t.Fatal("non-string allowlist element was accepted")
	}
}
