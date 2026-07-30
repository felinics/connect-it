// Package authsvc is the service gate: the admin password (argon2id) and the
// static API tokens used by internal applications. Tokens are stored as
// sha256 and the plaintext is returned only once, at creation time.
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

// EnvAdminPassword seeds the admin account on first start. The user name is
// always admin.
const EnvAdminPassword = "CONNECT_IT_ADMIN_PASSWORD"

// EnvBootstrapAPIToken seeds a static API token on start so an internal
// application can call the machine API without first minting a token through
// the admin UI. The value is the plaintext token itself.
const EnvBootstrapAPIToken = "CONNECT_IT_BOOTSTRAP_API_TOKEN"

const tokenPrefix = "cit_"

// bootstrapTokenMinLen keeps operators from seeding guessable tokens: the
// cit_ prefix plus at least 32 characters (16 bytes of entropy in hex).
const bootstrapTokenMinLen = len(tokenPrefix) + 32

// ErrNotFound means the target row does not exist, for example when revoking
	// a token that is already gone.
var ErrNotFound = errors.New("authsvc: not found")

type Service struct {
	q *store.Queries
}

func New(q *store.Queries) *Service {
	return &Service{q: q}
}

// EnsureAdminFromEnv creates the admin account from the environment password
// when it is missing. An existing account is left untouched.
func (s *Service) EnsureAdminFromEnv(ctx context.Context) error {
	if _, err := s.q.GetAdminAccount(ctx); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	password := os.Getenv(EnvAdminPassword)
	if password == "" {
		return fmt.Errorf("authsvc: admin account does not exist and %s is not set", EnvAdminPassword)
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
		return errors.New("authsvc: password must be at least 8 characters")
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

// EnsureBootstrapTokenFromEnv inserts the environment-provided API token when
// its hash is not in the database yet. Restarts are no-ops, and a bootstrap
// token revoked through the admin API stays revoked: the hash lookup includes
// revoked rows, so the token is never silently resurrected.
func (s *Service) EnsureBootstrapTokenFromEnv(ctx context.Context) error {
	token := strings.TrimSpace(os.Getenv(EnvBootstrapAPIToken))
	if token == "" {
		return nil
	}
	if !strings.HasPrefix(token, tokenPrefix) {
		return fmt.Errorf("authsvc: %s must start with %q", EnvBootstrapAPIToken, tokenPrefix)
	}
	if len(token) < bootstrapTokenMinLen {
		return fmt.Errorf(
			"authsvc: %s must carry at least %d characters after the %q prefix",
			EnvBootstrapAPIToken, bootstrapTokenMinLen-len(tokenPrefix), tokenPrefix,
		)
	}
	exists, err := s.q.APITokenHashExists(ctx, hashToken(token))
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return s.q.InsertAPIToken(ctx, store.InsertAPITokenParams{
		ID: uuid.New(), Name: "bootstrap", TokenHash: hashToken(token),
	})
}

type APITokenView struct {
	ID        uuid.UUID  `json:"id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at"`
}

// CreateAPIToken generates a token. The plaintext is returned only here; the
// database stores its sha256 only.
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
