package configsvc_test

import (
	"errors"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
)

// newValidateService 只装 registry，store 与 keyring 传 nil（Validate 不用它们）。
func newValidateService(t *testing.T) *configsvc.Service {
	t.Helper()
	r := registry.New()
	r.MustRegister(connector.Definition{
		Type:                "example_app",
		Name:                "Example",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Label: "Client ID", InputType: connector.InputText, Required: true},
			{Key: "client_secret", Label: "Client Secret", InputType: connector.InputText, Required: true, Secret: true},
			{Key: "region", Label: "Region", InputType: connector.InputSelect,
				Validation: connector.FieldValidation{Options: []string{"us", "eu"}}},
			{Key: "project_id", Label: "Project ID", InputType: connector.InputText,
				Validation: connector.FieldValidation{Pattern: `^[0-9]+$`}},
			{Key: "api_key", Label: "API Key", InputType: connector.InputText, Secret: true},
		},
	})
	return configsvc.New(nil, r, nil)
}

func TestValidateOK(t *testing.T) {
	s := newValidateService(t)
	err := s.Validate("example_app",
		map[string]any{"client_id": "abc", "region": "eu", "project_id": "123"},
		map[string]string{"client_secret": "shh", "api_key": "k"})
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
}

func TestValidateUnknownConnector(t *testing.T) {
	s := newValidateService(t)
	err := s.Validate("nope", nil, nil)
	if !errors.Is(err, configsvc.ErrUnknownConnector) {
		t.Fatalf("want ErrUnknownConnector, got %v", err)
	}
}

func TestValidateFailures(t *testing.T) {
	cases := []struct {
		name      string
		public    map[string]any
		secrets   map[string]string
		wantField string
	}{
		{"未知公开字段", map[string]any{"client_id": "a", "bogus": "x"},
			map[string]string{"client_secret": "s"}, "bogus"},
		{"Secret 字段放进 public", map[string]any{"client_id": "a", "client_secret": "leak"},
			map[string]string{"client_secret": "s"}, "client_secret"},
		{"未知 Secret 字段", map[string]any{"client_id": "a"},
			map[string]string{"client_secret": "s", "bogus": "x"}, "bogus"},
		{"公开字段值不是字符串", map[string]any{"client_id": 42},
			map[string]string{"client_secret": "s"}, "client_id"},
		{"缺必填公开字段", map[string]any{},
			map[string]string{"client_secret": "s"}, "client_id"},
		{"缺必填 Secret 字段", map[string]any{"client_id": "a"},
			map[string]string{}, "client_secret"},
		{"必填 Secret 传空串视为缺失", map[string]any{"client_id": "a"},
			map[string]string{"client_secret": ""}, "client_secret"},
		{"Pattern 不匹配", map[string]any{"client_id": "a", "project_id": "abc"},
			map[string]string{"client_secret": "s"}, "project_id"},
		{"不在 Options 内", map[string]any{"client_id": "a", "region": "cn"},
			map[string]string{"client_secret": "s"}, "region"},
	}
	s := newValidateService(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Validate("example_app", tc.public, tc.secrets)
			var ve *configsvc.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want ValidationError, got %v", err)
			}
			if ve.Field != tc.wantField {
				t.Fatalf("错误字段 %q, want %q（reason=%s）", ve.Field, tc.wantField, ve.Reason)
			}
		})
	}
}

func TestValidateOptionalSecretMayBeEmpty(t *testing.T) {
	// 可选 Secret 传空串表示删除，应通过校验。
	s := newValidateService(t)
	err := s.Validate("example_app",
		map[string]any{"client_id": "a"},
		map[string]string{"client_secret": "s", "api_key": ""})
	if err != nil {
		t.Fatalf("可选 Secret 空串应通过: %v", err)
	}
}
