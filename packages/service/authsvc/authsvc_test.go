package authsvc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/felinics/connect-it/packages/service/store"
	"github.com/felinics/connect-it/packages/service/testutil"
)

func TestPasswordHashRoundtrip(t *testing.T) {
	hash, err := hashPassword("s3cret-pass")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	if ok, err := verifyPassword(hash, "s3cret-pass"); err != nil || !ok {
		t.Fatalf("the correct password should verify: ok=%v err=%v", ok, err)
	}
	if ok, _ := verifyPassword(hash, "wrong"); ok {
		t.Fatal("a wrong password must not verify")
	}
	if _, err := verifyPassword("$bcrypt$whatever", "x"); err == nil {
		t.Fatal("an unknown format should return an error")
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
		t.Fatal("no account and no environment variable should return an error")
	}

	t.Setenv(EnvAdminPassword, "first-pass")
	if err := s.EnsureAdminFromEnv(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.VerifyAdminPassword(ctx, "admin", "first-pass"); err != nil || !ok {
		t.Fatalf("the seeded password should log in: ok=%v err=%v", ok, err)
	}

	// Idempotent: rerunning with a different environment variable must not
	// overwrite the existing password.
	t.Setenv(EnvAdminPassword, "second-pass")
	if err := s.EnsureAdminFromEnv(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.VerifyAdminPassword(ctx, "admin", "second-pass"); ok {
		t.Fatal("an existing account must not be overwritten by a new environment variable")
	}
	if ok, _ := s.VerifyAdminPassword(ctx, "admin", "first-pass"); !ok {
		t.Fatal("the original password should still be valid")
	}
	if ok, _ := s.VerifyAdminPassword(ctx, "root", "first-pass"); ok {
		t.Fatal("a mismatched user name must not verify")
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
		t.Fatal("a too-short password should be rejected")
	}
	if err := s.ChangeAdminPassword(ctx, "brand-new-pass"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.VerifyAdminPassword(ctx, "admin", "brand-new-pass"); !ok {
		t.Fatal("the new password should take effect")
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
		t.Fatalf("unexpected token prefix: %s", plaintext)
	}
	if gotID, ok, err := s.VerifyAPIToken(ctx, plaintext); err != nil || !ok || gotID != id {
		t.Fatalf("a valid token should verify: id=%s ok=%v err=%v", gotID, ok, err)
	}
	if _, ok, _ := s.VerifyAPIToken(ctx, "cit_deadbeef"); ok {
		t.Fatal("a forged token must not verify")
	}
	if _, ok, _ := s.VerifyAPIToken(ctx, "Bearer-something"); ok {
		t.Fatal("a token without the prefix must not verify")
	}

	list, err := s.ListAPITokens(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "ci" || list[0].RevokedAt != nil {
		t.Fatalf("unexpected list: %+v err=%v", list, err)
	}

	if err := s.RevokeAPIToken(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.VerifyAPIToken(ctx, plaintext); ok {
		t.Fatal("a revoked token must not verify")
	}
	if err := s.RevokeAPIToken(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking twice should yield ErrNotFound, got %v", err)
	}
	if err := s.RevokeAPIToken(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown id should yield ErrNotFound, got %v", err)
	}
}

func TestEnsureBootstrapTokenFromEnv(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	valid := "cit_" + strings.Repeat("ab", 16)

	t.Setenv(EnvBootstrapAPIToken, "")
	if err := s.EnsureBootstrapTokenFromEnv(ctx); err != nil {
		t.Fatalf("an unset environment variable should be a no-op: %v", err)
	}

	t.Setenv(EnvBootstrapAPIToken, "no-prefix-token-of-decent-length-here")
	if err := s.EnsureBootstrapTokenFromEnv(ctx); err == nil {
		t.Fatal("a token without the cit_ prefix should be rejected")
	}

	t.Setenv(EnvBootstrapAPIToken, "cit_tooshort")
	if err := s.EnsureBootstrapTokenFromEnv(ctx); err == nil {
		t.Fatal("a too-short token should be rejected")
	}

	t.Setenv(EnvBootstrapAPIToken, valid)
	if err := s.EnsureBootstrapTokenFromEnv(ctx); err != nil {
		t.Fatal(err)
	}
	id, ok, err := s.VerifyAPIToken(ctx, valid)
	if err != nil || !ok {
		t.Fatalf("the seeded token should verify: ok=%v err=%v", ok, err)
	}
	list, err := s.ListAPITokens(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "bootstrap" {
		t.Fatalf("unexpected list: %+v err=%v", list, err)
	}

	// Idempotent: a restart must not insert a second row.
	if err := s.EnsureBootstrapTokenFromEnv(ctx); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListAPITokens(ctx); len(list) != 1 {
		t.Fatalf("a rerun must not duplicate the token, got %d rows", len(list))
	}

	// A revoked bootstrap token stays revoked across restarts.
	if err := s.RevokeAPIToken(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureBootstrapTokenFromEnv(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.VerifyAPIToken(ctx, valid); ok {
		t.Fatal("a revoked bootstrap token must not be resurrected on restart")
	}

	// A rotated value seeds a new token alongside the revoked one.
	rotated := "cit_" + strings.Repeat("cd", 16)
	t.Setenv(EnvBootstrapAPIToken, rotated)
	if err := s.EnsureBootstrapTokenFromEnv(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.VerifyAPIToken(ctx, rotated); !ok {
		t.Fatal("a rotated bootstrap token should verify")
	}
}
