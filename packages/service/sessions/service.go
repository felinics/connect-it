// Package sessions 管理短期 MCP Session：token 签发（256bit 随机 hex，
// 库中只存 sha256）、解析与绑定查询（spec §11）。
package sessions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/service/store"
)

const (
	DefaultTTL = time.Hour
	MaxTTL     = 24 * time.Hour
)

var (
	aliasPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	toolIDPattern = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// ErrInvalidSession：token 不存在、过期或已吊销。
var ErrInvalidSession = errors.New("sessions: invalid session token")

// ValidationError 的 Code 直接作为 API 错误码返回。
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }

// SessionView 是一个已解析 session 的只读视图。
// Allowlist 的 key 是暴露名 alias__tool_id；空 map 表示允许全部绑定连接的全部 tool。
type SessionView struct {
	ID        uuid.UUID
	Bindings  map[string]uuid.UUID
	Allowlist map[string]bool
	ExpiresAt time.Time
}

type Service struct {
	q *store.Queries
}

func New(q *store.Queries) *Service { return &Service{q: q} }

// SplitExposedName 把暴露名 {alias}__{tool_id} 按第一个 "__" 切开并校验两段字符集。
func SplitExposedName(name string) (alias, toolID string, ok bool) {
	i := strings.Index(name, "__")
	if i < 0 {
		return "", "", false
	}
	alias, toolID = name[:i], name[i+2:]
	if !aliasPattern.MatchString(alias) || !toolIDPattern.MatchString(toolID) {
		return "", "", false
	}
	return alias, toolID, true
}

// Create 签发一个新的 MCP Session token。
// ttl == 0 时取 DefaultTTL；ttl < 0 或 > MaxTTL 报 invalid_ttl。
func (s *Service) Create(ctx context.Context, bindings map[string]uuid.UUID, toolAllowlist []string, ttl time.Duration) (token string, err error) {
	if len(bindings) == 0 {
		return "", &ValidationError{Code: "empty_bindings", Message: "at least one alias binding is required"}
	}
	for alias := range bindings {
		if !aliasPattern.MatchString(alias) {
			return "", &ValidationError{Code: "invalid_alias",
				Message: fmt.Sprintf("alias %q must match %s", alias, aliasPattern)}
		}
	}
	for _, name := range toolAllowlist {
		alias, _, ok := SplitExposedName(name)
		if !ok {
			return "", &ValidationError{Code: "invalid_allowlist",
				Message: fmt.Sprintf("entry %q is not a valid {alias}__{tool_id} name", name)}
		}
		if _, bound := bindings[alias]; !bound {
			return "", &ValidationError{Code: "invalid_allowlist",
				Message: fmt.Sprintf("entry %q references unbound alias %q", name, alias)}
		}
	}
	switch {
	case ttl < 0:
		return "", &ValidationError{Code: "invalid_ttl", Message: "ttl must not be negative"}
	case ttl == 0:
		ttl = DefaultTTL
	case ttl > MaxTTL:
		return "", &ValidationError{Code: "invalid_ttl", Message: "ttl must not exceed 24h"}
	}
	for alias, connID := range bindings {
		found, err := s.q.ConnectionExistsByID(ctx, connID)
		if err != nil {
			return "", err
		}
		if !found {
			return "", &ValidationError{Code: "unknown_connection",
				Message: fmt.Sprintf("connection %s (alias %q) does not exist", connID, alias)}
		}
	}

	raw := make([]byte, 32) // 256bit
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token = hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))

	allowlist := toolAllowlist
	if allowlist == nil {
		allowlist = []string{}
	}
	allowlistJSON, err := json.Marshal(allowlist)
	if err != nil {
		return "", err
	}

	// 无事务：session 行先落库，连接绑定行随后插入。
	// 中途失败时 token 不会返回给调用方，孤儿 session 行不可达、无害。
	sessionID := uuid.New()
	if err := s.q.CreateMCPSession(ctx, store.CreateMCPSessionParams{
		ID:            sessionID,
		TokenHash:     hex.EncodeToString(sum[:]),
		ToolAllowlist: allowlistJSON,
		ExpiresAt:     time.Now().UTC().Add(ttl),
	}); err != nil {
		return "", err
	}
	for alias, connID := range bindings {
		if err := s.q.AddMCPSessionConnection(ctx, store.AddMCPSessionConnectionParams{
			SessionID:    sessionID,
			Alias:        alias,
			ConnectionID: connID,
		}); err != nil {
			return "", err
		}
	}
	return token, nil
}

// Resolve 把 session token 解析为 SessionView；token 不存在、过期或吊销返回 ErrInvalidSession。
func (s *Service) Resolve(ctx context.Context, token string) (SessionView, error) {
	sum := sha256.Sum256([]byte(token))
	row, err := s.q.GetMCPSessionByTokenHash(ctx, hex.EncodeToString(sum[:]))
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionView{}, ErrInvalidSession
	}
	if err != nil {
		return SessionView{}, err
	}
	if row.Status != "active" {
		return SessionView{}, ErrInvalidSession
	}
	if !row.ExpiresAt.After(time.Now()) {
		return SessionView{}, ErrInvalidSession
	}

	var list []string
	if err := json.Unmarshal(row.ToolAllowlist, &list); err != nil {
		return SessionView{}, fmt.Errorf("sessions: corrupt tool_allowlist for session %s: %w", row.ID, err)
	}
	allowlist := make(map[string]bool, len(list))
	for _, name := range list {
		allowlist[name] = true
	}

	conns, err := s.q.ListMCPSessionConnections(ctx, row.ID)
	if err != nil {
		return SessionView{}, err
	}
	bindings := make(map[string]uuid.UUID, len(conns))
	for _, c := range conns {
		bindings[c.Alias] = c.ConnectionID
	}

	return SessionView{
		ID:        row.ID,
		Bindings:  bindings,
		Allowlist: allowlist,
		ExpiresAt: row.ExpiresAt,
	}, nil
}

// ConnectionConnectorTypes 批量查询连接的 connector_type，供 /mcp 构建工具列表。
// 不存在的连接（已被删除）不会出现在结果里，由调用方决定如何降级。
func (s *Service) ConnectionConnectorTypes(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	if len(ids) == 0 {
		return map[uuid.UUID]string{}, nil
	}
	rows, err := s.q.GetConnectionConnectorTypes(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(rows))
	for _, r := range rows {
		out[r.ID] = r.ConnectorType
	}
	return out, nil
}
