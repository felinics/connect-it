package configsvc_test

import (
	"errors"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/service/configsvc"
)

// Validate 会合并库中已存的 secret，因此需要真实 store。
func newValidateService(t *testing.T) *configsvc.Service {
	t.Helper()
	svc, _ := newRWService(t, configsvc.Fixture("example_app", ""))
	return svc
}

func TestValidateOK(t *testing.T) {
	s := newValidateService(t)
	err := s.Validate(t.Context(), "example_app",
		map[string]any{"client_id": "abc", "region": "eu", "project_id": "123"},
		map[string]string{"client_secret": "shh", "api_key": "k"})
	if err != nil {
		t.Fatalf("合法配置不应报错: %v", err)
	}
}

func TestValidateUnknownConnector(t *testing.T) {
	s := newValidateService(t)
	err := s.Validate(t.Context(), "nope", nil, nil)
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
			err := s.Validate(t.Context(), "example_app", tc.public, tc.secrets)
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
	err := s.Validate(t.Context(), "example_app",
		map[string]any{"client_id": "a"},
		map[string]string{"client_secret": "s", "api_key": ""})
	if err != nil {
		t.Fatalf("可选 Secret 空串应通过: %v", err)
	}
}

// Validate 是 Put 的试算，两者对同一请求体必须给出同样的结论。secrets 是补丁：
// 管理员只改 public、不重填已存的必填 secret 时，Put 会合并库中原值并成功，
// 试算若不合并就会误报“必填字段缺失”。
func TestValidateMergesStoredSecretsLikePut(t *testing.T) {
	s := newValidateService(t)
	mustPut(t, s, "example_app",
		map[string]any{"client_id": "abc"},
		map[string]string{"client_secret": "stored"})

	public := map[string]any{"client_id": "changed"}
	if err := s.Validate(t.Context(), "example_app", public, map[string]string{}); err != nil {
		t.Fatalf("已存必填 secret 未重填时试算不应报错: %v", err)
	}
	// 零值 ifMatch 表示不做乐观并发检查。
	if _, err := s.Put(t.Context(), "example_app", public, map[string]string{}, time.Time{}); err != nil {
		t.Fatalf("同样的请求体 Put 应成功: %v", err)
	}
}

// 已存 secret 被显式删除时，试算必须和 Put 一样报必填缺失。
func TestValidateHonoursSecretDeletionAgainstStored(t *testing.T) {
	s := newValidateService(t)
	mustPut(t, s, "example_app",
		map[string]any{"client_id": "abc"},
		map[string]string{"client_secret": "stored"})

	err := s.Validate(t.Context(), "example_app",
		map[string]any{"client_id": "abc"},
		map[string]string{"client_secret": ""})
	var ve *configsvc.ValidationError
	if !errors.As(err, &ve) || ve.Field != "client_secret" {
		t.Fatalf("删除已存必填 secret 应报 client_secret 缺失, got %v", err)
	}
}
