package sessions

import (
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

func TestDecodeStoredToolAllowlistStrict(t *testing.T) {
	valid := `{"version":1,"tools":[{"name":"gh__read","risk":"read"},{"name":"gh__write","risk":"write"}]}`
	envelope, err := decodeStoredToolAllowlist([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	if len(envelope.Tools) != 2 {
		t.Fatalf("tools = %+v", envelope.Tools)
	}

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"legacy array", `["gh__read"]`},
		{"unknown version", `{"version":2,"tools":[]}`},
		{"missing version", `{"tools":[]}`},
		{"missing tools", `{"version":1}`},
		{"null tools", `{"version":1,"tools":null}`},
		{"unknown envelope field", `{"version":1,"tools":[],"extra":true}`},
		{"unknown grant field", `{"version":1,"tools":[{"name":"gh__read","risk":"read","source":"explicit"}]}`},
		{"unknown risk", `{"version":1,"tools":[{"name":"gh__read","risk":"safe"}]}`},
		{"invalid name", `{"version":1,"tools":[{"name":"bad","risk":"read"}]}`},
		{"duplicate", `{"version":1,"tools":[{"name":"gh__read","risk":"read"},{"name":"gh__read","risk":"read"}]}`},
		{"trailing value", `{"version":1,"tools":[]} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeStoredToolAllowlist([]byte(tc.raw)); err == nil {
				t.Fatalf("decode %s succeeded", tc.raw)
			}
		})
	}
}

func TestEncodeStoredToolAllowlistStableAndNeverNull(t *testing.T) {
	raw, err := encodeStoredToolAllowlist(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"version":1,"tools":[]}` {
		t.Fatalf("nil encoding = %s", raw)
	}

	raw, err = encodeStoredToolAllowlist([]StoredToolGrant{
		{Name: "z__read", Risk: connector.RiskRead},
		{Name: "a__read", Risk: connector.RiskRead},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(raw), "a__read") > strings.Index(string(raw), "z__read") {
		t.Fatalf("grants not sorted: %s", raw)
	}
}
