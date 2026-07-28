// Package authsvc 是服务门禁：admin 密码（argon2id）与内部应用的静态
// API token（sha256 存储，明文只在创建时返回一次）。
package authsvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/service/store"
)

// EnvAdminPassword 用于首次启动 seed admin 账号（用户名固定 admin）。
const EnvAdminPassword = "CONNECT_IT_ADMIN_PASSWORD"

const tokenPrefix = "cit_"

// ErrNotFound：目标行不存在（如撤销不存在的 token）。
var ErrNotFound = errors.New("authsvc: not found")

type Service struct {
	q *store.Queries
}

func New(q *store.Queries) *Service {
	return &Service{q: q}
}

// EnsureAdminFromEnv 在 admin 账号缺失时用环境变量密码创建；已存在则不动。
func (s *Service) EnsureAdminFromEnv(ctx context.Context) error {
	if _, err := s.q.GetAdminAccount(ctx); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	password := os.Getenv(EnvAdminPassword)
	if password == "" {
		return fmt.Errorf("authsvc: admin 账号不存在且未设置 %s", EnvAdminPassword)
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.q.InsertAdminAccountIfAbsent(ctx, store.InsertAdminAccountIfAbsentParams{
		Username: "admin", PasswordHash: hash,
	})
	return err
}

func (s *Service) VerifyAdminPassword(ctx context.Context, username, password string) (bool, error) {
	acct, err := s.q.GetAdminAccount(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if acct.Username != username {
		return false, nil
	}
	return verifyPassword(acct.PasswordHash, password)
}

func (s *Service) ChangeAdminPassword(ctx context.Context, newPassword string) error {
	if len(newPassword) < 8 {
		return errors.New("authsvc: 密码至少 8 个字符")
	}
	hash, err := hashPassword(newPassword)
	if err != nil {
		return err
	}
	n, err := s.q.UpdateAdminPassword(ctx, hash)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

type APITokenView struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

// CreateAPIToken 生成 token；明文仅此一次返回，库中只存 sha256。
func (s *Service) CreateAPIToken(ctx context.Context, name string) (string, uuid.UUID, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", uuid.Nil, err
	}
	plaintext := tokenPrefix + hex.EncodeToString(raw)
	id := uuid.New()
	if err := s.q.InsertAPIToken(ctx, store.InsertAPITokenParams{
		ID: id, Name: name, TokenHash: hashToken(plaintext),
	}); err != nil {
		return "", uuid.Nil, err
	}
	return plaintext, id, nil
}

func (s *Service) VerifyAPIToken(ctx context.Context, token string) (uuid.UUID, bool, error) {
	if !strings.HasPrefix(token, tokenPrefix) {
		return uuid.Nil, false, nil
	}
	row, err := s.q.GetAPITokenByHash(ctx, hashToken(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return row.ID, true, nil
}

func (s *Service) ListAPITokens(ctx context.Context) ([]APITokenView, error) {
	rows, err := s.q.ListAPITokens(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]APITokenView, 0, len(rows))
	for _, r := range rows {
		out = append(out, APITokenView{ID: r.ID, Name: r.Name, CreatedAt: r.CreatedAt, RevokedAt: r.RevokedAt})
	}
	return out, nil
}

func (s *Service) RevokeAPIToken(ctx context.Context, id uuid.UUID) error {
	n, err := s.q.RevokeAPIToken(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
