package authsvc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func TestPasswordHashRoundtrip(t *testing.T) {
	hash, err := hashPassword("s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("哈希格式不符: %s", hash)
	}
	if ok, err := verifyPassword(hash, "s3cret-pass"); err != nil || !ok {
		t.Fatalf("正确密码应通过: ok=%v err=%v", ok, err)
	}
	if ok, _ := verifyPassword(hash, "wrong"); ok {
		t.Fatal("错误密码不应通过")
	}
	if _, err := verifyPassword("$bcrypt$whatever", "x"); err == nil {
		t.Fatal("未知格式应报错")
	}
}

func newService(t *testing.T) *Service {
	t.Helper()
	pool := testutil.NewDB(t)
	return New(store.New(pool))
}

func TestEnsureAdminFromEnv(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	t.Setenv(EnvAdminPassword, "")
	if err := s.EnsureAdminFromEnv(ctx); err == nil {
		t.Fatal("无账号且无环境变量应报错")
	}

	t.Setenv(EnvAdminPassword, "first-pass")
	if err := s.EnsureAdminFromEnv(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.VerifyAdminPassword(ctx, "admin", "first-pass"); err != nil || !ok {
		t.Fatalf("seed 密码应可登录: ok=%v err=%v", ok, err)
	}

	// 幂等：换环境变量再跑不会覆盖既有密码
	t.Setenv(EnvAdminPassword, "second-pass")
	if err := s.EnsureAdminFromEnv(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.VerifyAdminPassword(ctx, "admin", "second-pass"); ok {
		t.Fatal("已有账号不应被新环境变量覆盖")
	}
	if ok, _ := s.VerifyAdminPassword(ctx, "admin", "first-pass"); !ok {
		t.Fatal("原密码应仍然有效")
	}
	if ok, _ := s.VerifyAdminPassword(ctx, "root", "first-pass"); ok {
		t.Fatal("用户名不匹配不应通过")
	}
}

func TestChangeAdminPassword(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	t.Setenv(EnvAdminPassword, "first-pass")
	if err := s.EnsureAdminFromEnv(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangeAdminPassword(ctx, "short"); err == nil {
		t.Fatal("过短密码应被拒绝")
	}
	if err := s.ChangeAdminPassword(ctx, "brand-new-pass"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.VerifyAdminPassword(ctx, "admin", "brand-new-pass"); !ok {
		t.Fatal("新密码应生效")
	}
}

func TestAPITokenLifecycle(t *testing.T) {
	s := newService(t)
	ctx := context.Background()

	plaintext, id, err := s.CreateAPIToken(ctx, "ci")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(plaintext, "cit_") {
		t.Fatalf("token 前缀不符: %s", plaintext)
	}
	if gotID, ok, err := s.VerifyAPIToken(ctx, plaintext); err != nil || !ok || gotID != id {
		t.Fatalf("有效 token 应通过: id=%s ok=%v err=%v", gotID, ok, err)
	}
	if _, ok, _ := s.VerifyAPIToken(ctx, "cit_deadbeef"); ok {
		t.Fatal("伪造 token 不应通过")
	}
	if _, ok, _ := s.VerifyAPIToken(ctx, "Bearer-something"); ok {
		t.Fatal("无前缀 token 不应通过")
	}

	list, err := s.ListAPITokens(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "ci" || list[0].RevokedAt != nil {
		t.Fatalf("list 不符: %+v err=%v", list, err)
	}

	if err := s.RevokeAPIToken(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.VerifyAPIToken(ctx, plaintext); ok {
		t.Fatal("已撤销 token 不应通过")
	}
	if err := s.RevokeAPIToken(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复撤销应 ErrNotFound, got %v", err)
	}
	if err := s.RevokeAPIToken(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的 id 应 ErrNotFound, got %v", err)
	}
}
