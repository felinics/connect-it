package connsvc_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func testDef() connector.Definition {
	return connector.Definition{
		Type: "example_app", Name: "Example", ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{
			{Key: "pat", Type: connector.AuthAPIKey, Label: "PAT",
				CredentialFields: []connector.ConfigField{
					{Key: "token", Label: "Token", InputType: connector.InputText, Required: true,
						Validation: connector.FieldValidation{Pattern: `^tok_`}},
				}},
			{Key: "oauth", Type: connector.AuthOAuth2, Label: "OAuth", OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
			}},
		},
	}
}

func newReg(t *testing.T) *registry.Registry {
	t.Helper()
	r := registry.New()
	r.MustRegister(testDef())
	return r
}

// 校验类错误在触库前返回，用 nil store 做纯单测。
func TestCreateAPIKeyValidation(t *testing.T) {
	s := connsvc.New(nil, newReg(t), nil)
	ctx := context.Background()

	cases := []struct {
		name    string
		typ     connector.Type
		method  string
		alias   string
		fields  map[string]string
		wantErr error
	}{
		{"alias 非法", "example_app", "pat", "Bad_Alias", nil, connsvc.ErrInvalidAlias},
		{"未知 connector", "nope", "pat", "a1", nil, connsvc.ErrUnknownConnector},
		{"未知 auth method", "example_app", "nope", "a1", nil, connsvc.ErrUnknownAuthMethod},
		{"oauth method 不能走 api-key", "example_app", "oauth", "a1", nil, connsvc.ErrWrongAuthType},
		{"缺必填字段", "example_app", "pat", "a1", map[string]string{}, connsvc.ErrInvalidFields},
		{"未声明字段", "example_app", "pat", "a1",
			map[string]string{"token": "tok_1", "extra": "x"}, connsvc.ErrInvalidFields},
		{"pattern 不符", "example_app", "pat", "a1",
			map[string]string{"token": "bad"}, connsvc.ErrInvalidFields},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.CreateAPIKey(ctx, tc.typ, tc.method, tc.alias, tc.fields)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("want %v, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestConnectionLifecycle(t *testing.T) {
	pool := testutil.NewDB(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("ef", 32))
	if err != nil {
		t.Fatal(err)
	}
	s := connsvc.New(store.New(pool), newReg(t), kr)
	ctx := context.Background()

	id, err := s.CreateAPIKey(ctx, "example_app", "pat", "acct-1", map[string]string{"token": "tok_abc"})
	if err != nil {
		t.Fatal(err)
	}

	// alias 冲突
	if _, err := s.CreateAPIKey(ctx, "example_app", "pat", "acct-1",
		map[string]string{"token": "tok_xyz"}); !errors.Is(err, connsvc.ErrAliasTaken) {
		t.Fatalf("重复 alias 应 ErrAliasTaken, got %v", err)
	}

	list, err := s.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	if list[0].ID != id || list[0].Alias != "acct-1" || list[0].Status != "active" {
		t.Fatalf("视图不符: %+v", list[0])
	}

	// credential 已加密：直接读库不应出现明文
	var raw []byte
	if err := pool.QueryRow(ctx, "select credential from connections where id = $1", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tok_abc") {
		t.Fatal("credential 应为密文")
	}

	view, err := s.Get(ctx, id)
	if err != nil || view.ID != id {
		t.Fatalf("get: %+v err=%v", view, err)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, id); !errors.Is(err, connsvc.ErrNotFound) {
		t.Fatalf("重复删除应 ErrNotFound, got %v", err)
	}
	if _, err := s.Get(ctx, uuid.New()); !errors.Is(err, connsvc.ErrNotFound) {
		t.Fatalf("不存在应 ErrNotFound, got %v", err)
	}
}
