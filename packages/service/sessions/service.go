// Package sessions 管理绑定单个 Connection 的短期 MCP Session。
package sessions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/service/store"
)

const (
	DefaultTTL           = time.Hour
	MaxTTL               = 24 * time.Hour
	toolDiscoveryTimeout = 30 * time.Second
	maxSnapshotTools     = 1024
	maxSnapshotBytes     = 2 << 20
)

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

var (
	// ErrInvalidSession：token 不存在、过期，或签发它的 API token 已撤销。
	ErrInvalidSession = errors.New("sessions: invalid session token")
	ErrToolDiscovery  = errors.New("sessions: tool discovery failed")
)

// ValidationError 的 Code 直接作为 API 错误码返回。
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }

// SessionView 是一个已解析 session 的只读能力快照。
type SessionView struct {
	ID           uuid.UUID
	APITokenID   uuid.UUID
	ConnectionID uuid.UUID
	Tools        []*mcp.Tool
	AllowedTools map[string]bool
	ExpiresAt    time.Time
}

type ToolLister interface {
	ListTools(ctx context.Context, connectionID uuid.UUID) ([]*mcp.Tool, error)
}

type Service struct {
	q      *store.Queries
	lister ToolLister
}

func New(q *store.Queries, lister ToolLister) *Service {
	return &Service{q: q, lister: lister}
}

// Create 签发一个新的 MCP Session token。
// ttl == 0 时取 DefaultTTL；ttl < 0 或 > MaxTTL 报 invalid_ttl。
func (s *Service) Create(
	ctx context.Context,
	apiTokenID uuid.UUID,
	connectionID uuid.UUID,
	toolAllowlist []string,
	ttl time.Duration,
) (token string, err error) {
	if len(toolAllowlist) > maxSnapshotTools {
		return "", &ValidationError{Code: "too_many_tools",
			Message: fmt.Sprintf("at most %d allowlist entries are allowed", maxSnapshotTools)}
	}
	requestedTools := make(map[string]bool, len(toolAllowlist))
	for _, name := range toolAllowlist {
		if !toolNamePattern.MatchString(name) {
			return "", &ValidationError{Code: "invalid_allowlist",
				Message: fmt.Sprintf("entry %q is not a valid tool name", name)}
		}
		if requestedTools[name] {
			return "", &ValidationError{Code: "invalid_allowlist",
				Message: fmt.Sprintf("entry %q is duplicated", name)}
		}
		requestedTools[name] = true
	}
	switch {
	case ttl < 0:
		return "", &ValidationError{Code: "invalid_ttl", Message: "ttl must not be negative"}
	case ttl == 0:
		ttl = DefaultTTL
	case ttl > MaxTTL:
		return "", &ValidationError{Code: "invalid_ttl", Message: "ttl must not exceed 24h"}
	}
	connection, err := s.q.GetConnection(ctx, connectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", &ValidationError{Code: "unknown_connection",
			Message: fmt.Sprintf("connection %s does not exist", connectionID)}
	}
	if err != nil {
		return "", err
	}
	if connection.Status != "active" {
		return "", &ValidationError{Code: "connection_not_active",
			Message: fmt.Sprintf("connection %s is %s", connectionID, connection.Status)}
	}

	toolSnapshot, err := s.discoverTools(ctx, connectionID, requestedTools)
	if err != nil {
		return "", err
	}

	raw := make([]byte, 32) // 256bit
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token = hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))

	if err := s.q.DeleteExpiredMCPSessions(ctx); err != nil {
		return "", err
	}
	if err := s.q.CreateMCPSession(ctx, store.CreateMCPSessionParams{
		ID:           uuid.New(),
		TokenHash:    hex.EncodeToString(sum[:]),
		ApiTokenID:   apiTokenID,
		ConnectionID: connectionID,
		ToolSnapshot: toolSnapshot,
		ExpiresAt:    time.Now().UTC().Add(ttl),
	}); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) discoverTools(
	ctx context.Context,
	connectionID uuid.UUID,
	requested map[string]bool,
) ([]byte, error) {
	if s.lister == nil {
		return nil, fmt.Errorf("%w: tool lister is not configured", ErrToolDiscovery)
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, toolDiscoveryTimeout)
	defer cancel()

	tools, err := s.lister.ListTools(discoveryCtx, connectionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrToolDiscovery, err)
	}

	discovered := make([]*mcp.Tool, 0)
	seen := make(map[string]bool)
	for _, tool := range tools {
		if tool == nil {
			slog.WarnContext(ctx, "skipping upstream MCP tool",
				slog.String("connection_id", connectionID.String()),
				slog.String("reason", "nil_tool"))
			continue
		}
		if !toolNamePattern.MatchString(tool.Name) {
			slog.WarnContext(ctx, "skipping upstream MCP tool",
				slog.String("connection_id", connectionID.String()),
				slog.String("tool_name", tool.Name),
				slog.String("reason", "invalid_name"))
			continue
		}
		if seen[tool.Name] {
			slog.WarnContext(ctx, "skipping upstream MCP tool",
				slog.String("connection_id", connectionID.String()),
				slog.String("tool_name", tool.Name),
				slog.String("reason", "duplicate_name"))
			continue
		}
		seen[tool.Name] = true
		copy := *tool
		discovered = append(discovered, &copy)
	}

	if len(requested) > 0 {
		for name := range requested {
			if !seen[name] {
				return nil, &ValidationError{Code: "invalid_allowlist",
					Message: fmt.Sprintf("tool %q was not discovered", name)}
			}
		}
		filtered := discovered[:0]
		for _, tool := range discovered {
			if requested[tool.Name] {
				filtered = append(filtered, tool)
			}
		}
		discovered = filtered
	}
	sort.Slice(discovered, func(i, j int) bool { return discovered[i].Name < discovered[j].Name })
	if len(discovered) > maxSnapshotTools {
		return nil, fmt.Errorf("%w: snapshot contains more than %d tools",
			ErrToolDiscovery, maxSnapshotTools)
	}
	snapshot, err := json.Marshal(discovered)
	if err != nil {
		return nil, fmt.Errorf("%w: encode snapshot: %v", ErrToolDiscovery, err)
	}
	if len(snapshot) > maxSnapshotBytes {
		return nil, fmt.Errorf("%w: snapshot exceeds %d bytes", ErrToolDiscovery, maxSnapshotBytes)
	}
	return snapshot, nil
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
	if !row.ExpiresAt.After(time.Now()) {
		return SessionView{}, ErrInvalidSession
	}

	var tools []*mcp.Tool
	if err := json.Unmarshal(row.ToolSnapshot, &tools); err != nil {
		return SessionView{}, fmt.Errorf("sessions: corrupt tool_snapshot for session %s: %w", row.ID, err)
	}
	allowedTools := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if tool == nil || tool.Name == "" {
			return SessionView{}, fmt.Errorf("sessions: corrupt tool_snapshot for session %s", row.ID)
		}
		allowedTools[tool.Name] = true
	}

	return SessionView{
		ID:           row.ID,
		APITokenID:   row.ApiTokenID,
		ConnectionID: row.ConnectionID,
		Tools:        tools,
		AllowedTools: allowedTools,
		ExpiresAt:    row.ExpiresAt,
	}, nil
}
