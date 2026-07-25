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
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/connsvc"
	execsvc "github.com/memohai/connect-it/packages/service/exec"
	"github.com/memohai/connect-it/packages/service/store"
)

const (
	DefaultTTL = time.Hour
	MaxTTL     = 24 * time.Hour
)

// aliasPattern 复用 connsvc 的唯一定义：session alias 必须与 connection alias
// 同形，否则 {alias}__{tool_id} 会出现无法绑定的名字。
var (
	aliasPattern  = connsvc.AliasPattern
	toolIDPattern = regexp.MustCompile(`^[a-z0-9_]+$`)
)

var (
	// ErrInvalidSession covers an unknown, expired, revoked, or corrupt Session.
	ErrInvalidSession = errors.New("sessions: invalid session token")
	// ErrExecutionUnauthorized deliberately collapses every persisted policy
	// mismatch to one fail-closed result.
	ErrExecutionUnauthorized = execsvc.ErrExecutionUnauthorized
)

// ValidationError 的 Code 直接作为 API 错误码返回。
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }

type SessionBinding struct {
	ConnectionID            uuid.UUID
	AuthorizationGeneration int64
	ActiveConnectorType     string
}

// SessionView is a read-only snapshot. An empty Grants map authorizes zero
// tools; it never means "all tools".
type SessionView struct {
	ID        uuid.UUID
	Bindings  map[string]SessionBinding
	Grants    map[string]StoredToolGrant
	ExpiresAt time.Time
}

// CreatedSession is the result of an atomic Session issuance. ExpiresAt is the
// database timestamp returned by the same statement that inserted the Session
// and every binding; it is never derived from the application clock.
type CreatedSession struct {
	Token     string
	ExpiresAt time.Time
}

type sessionBindingWrite struct {
	Alias                   string    `json:"alias"`
	ConnectionID            uuid.UUID `json:"connection_id"`
	AuthorizationGeneration int64     `json:"authorization_generation"`
}

type Service struct {
	q        *store.Queries
	registry *registry.Registry
}

func New(q *store.Queries, reg *registry.Registry) *Service {
	return &Service{q: q, registry: reg}
}

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

// CreateWithExpiry atomically inserts the Session and all bindings and returns
// the expiration timestamp calculated by PostgreSQL. ttl == 0 selects
// DefaultTTL; ttl < 0 or ttl > MaxTTL returns invalid_ttl.
func (s *Service) CreateWithExpiry(
	ctx context.Context,
	bindings map[string]uuid.UUID,
	allowlistInput AllowlistInput,
	ttl time.Duration,
) (CreatedSession, error) {
	if len(bindings) == 0 {
		return CreatedSession{}, &ValidationError{
			Code:    "empty_bindings",
			Message: "at least one alias binding is required",
		}
	}
	for alias := range bindings {
		if !aliasPattern.MatchString(alias) {
			return CreatedSession{}, &ValidationError{Code: "invalid_alias",
				Message: fmt.Sprintf("alias %q must match %s", alias, aliasPattern)}
		}
	}
	if allowlistInput.Null {
		return CreatedSession{}, &ValidationError{
			Code:    "invalid_allowlist",
			Message: "tool_allowlist must be omitted or an array",
		}
	}
	switch {
	case ttl < 0:
		return CreatedSession{}, &ValidationError{
			Code: "invalid_ttl", Message: "ttl must not be negative",
		}
	case ttl == 0:
		ttl = DefaultTTL
	case ttl > MaxTTL:
		return CreatedSession{}, &ValidationError{
			Code: "invalid_ttl", Message: "ttl must not exceed 24h",
		}
	case ttl%time.Second != 0:
		return CreatedSession{}, &ValidationError{
			Code: "invalid_ttl", Message: "ttl must use whole seconds",
		}
	}
	if s.q == nil {
		return CreatedSession{}, errors.New("sessions: store is not configured")
	}
	if s.registry == nil {
		return CreatedSession{}, errors.New("sessions: registry is not configured")
	}

	raw := make([]byte, 32) // 256bit
	if _, err := rand.Read(raw); err != nil {
		return CreatedSession{}, err
	}
	token := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))

	tx, qtx, err := s.q.BeginTx(ctx)
	if err != nil {
		return CreatedSession{}, err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	connectionIDs := uniqueSortedConnectionIDs(bindings)
	connectionRows, err := qtx.GetConnectionsForMCPSession(ctx, connectionIDs)
	if err != nil {
		return CreatedSession{}, err
	}
	connectionByID := make(map[uuid.UUID]store.GetConnectionsForMCPSessionRow, len(connectionRows))
	for _, row := range connectionRows {
		connectionByID[row.ID] = row
	}
	for alias, connectionID := range bindings {
		row, found := connectionByID[connectionID]
		if !found {
			return CreatedSession{}, &ValidationError{
				Code:    "unknown_connection",
				Message: fmt.Sprintf("connection %s (alias %q) does not exist", connectionID, alias),
			}
		}
		if row.Status != "active" {
			return CreatedSession{}, &ValidationError{
				Code:    "inactive_connection",
				Message: fmt.Sprintf("connection %s (alias %q) is not active", connectionID, alias),
			}
		}
		if row.AuthorizationGeneration < 1 {
			return CreatedSession{}, fmt.Errorf(
				"sessions: connection %s has invalid authorization generation",
				connectionID,
			)
		}
	}

	grants, err := s.buildGrants(bindings, connectionByID, allowlistInput)
	if err != nil {
		return CreatedSession{}, err
	}
	allowlistJSON, err := encodeStoredToolAllowlist(grants)
	if err != nil {
		return CreatedSession{}, err
	}

	sessionID := uuid.New()
	aliases := sortedAliases(bindings)
	bindingRows := make([]sessionBindingWrite, 0, len(aliases))
	for _, alias := range aliases {
		connectionID := bindings[alias]
		bindingRows = append(bindingRows, sessionBindingWrite{
			Alias:                   alias,
			ConnectionID:            connectionID,
			AuthorizationGeneration: connectionByID[connectionID].AuthorizationGeneration,
		})
	}
	bindingsJSON, err := json.Marshal(bindingRows)
	if err != nil {
		return CreatedSession{}, fmt.Errorf("sessions: encode bindings: %w", err)
	}
	expiresAt, err := qtx.CreateMCPSession(ctx, store.CreateMCPSessionParams{
		ID:            sessionID,
		TokenHash:     hex.EncodeToString(sum[:]),
		ToolAllowlist: allowlistJSON,
		TtlSeconds:    int64(ttl / time.Second),
		Bindings:      bindingsJSON,
	})
	if err != nil {
		return CreatedSession{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CreatedSession{}, err
	}
	return CreatedSession{Token: token, ExpiresAt: expiresAt}, nil
}

func (s *Service) buildGrants(
	bindings map[string]uuid.UUID,
	connections map[uuid.UUID]store.GetConnectionsForMCPSessionRow,
	input AllowlistInput,
) ([]StoredToolGrant, error) {
	toolsByAlias := make(map[string]map[string]connector.Tool, len(bindings))
	for alias, connectionID := range bindings {
		connection := connections[connectionID]
		def, ok := s.registry.Get(connector.Type(connection.ConnectorType))
		if !ok {
			return nil, &ValidationError{
				Code:    "unknown_connector",
				Message: fmt.Sprintf("connection %s references an unavailable connector", connectionID),
			}
		}
		tools := make(map[string]connector.Tool, len(def.Tools))
		for _, tool := range def.Tools {
			tools[tool.ID] = tool
		}
		toolsByAlias[alias] = tools
	}

	if input.Tools == nil {
		var grants []StoredToolGrant
		for alias, tools := range toolsByAlias {
			for _, tool := range tools {
				if tool.Risk != connector.RiskRead {
					continue
				}
				grants = append(grants, StoredToolGrant{
					Name: alias + "__" + tool.ID,
					Risk: connector.RiskRead,
				})
			}
		}
		return grants, nil
	}

	grants := make([]StoredToolGrant, 0, len(input.Tools))
	seen := make(map[string]struct{}, len(input.Tools))
	for _, name := range input.Tools {
		alias, toolID, ok := SplitExposedName(name)
		if !ok {
			return nil, &ValidationError{
				Code:    "invalid_allowlist",
				Message: fmt.Sprintf("entry %q is not a valid {alias}__{tool_id} name", name),
			}
		}
		tools, bound := toolsByAlias[alias]
		if !bound {
			return nil, &ValidationError{
				Code:    "invalid_allowlist",
				Message: fmt.Sprintf("entry %q references unbound alias %q", name, alias),
			}
		}
		tool, exists := tools[toolID]
		if !exists {
			return nil, &ValidationError{
				Code:    "invalid_allowlist",
				Message: fmt.Sprintf("entry %q references an unavailable tool", name),
			}
		}
		if !validToolRisk(tool.Risk) {
			return nil, &ValidationError{
				Code:    "invalid_allowlist",
				Message: fmt.Sprintf("entry %q references a tool with invalid risk metadata", name),
			}
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, &ValidationError{
				Code:    "invalid_allowlist",
				Message: fmt.Sprintf("entry %q is duplicated", name),
			}
		}
		seen[name] = struct{}{}
		grants = append(grants, StoredToolGrant{
			Name: name,
			Risk: tool.Risk,
		})
	}
	return grants, nil
}

// Resolve 把 session token 解析为 SessionView；token 不存在、过期或吊销返回 ErrInvalidSession。
func (s *Service) Resolve(ctx context.Context, token string) (SessionView, error) {
	if s == nil || s.q == nil {
		return SessionView{}, errors.New("sessions: store is not configured")
	}
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
	if !row.Unexpired {
		return SessionView{}, ErrInvalidSession
	}

	envelope, err := decodeStoredToolAllowlist(row.ToolAllowlist)
	if err != nil {
		return SessionView{}, ErrInvalidSession
	}
	grants := make(map[string]StoredToolGrant, len(envelope.Tools))
	for _, grant := range envelope.Tools {
		grants[grant.Name] = grant
	}

	conns, err := s.q.ListMCPSessionConnections(ctx, row.ID)
	if err != nil {
		return SessionView{}, err
	}
	bindings := make(map[string]SessionBinding, len(conns))
	for _, c := range conns {
		if c.AuthorizationGeneration < 1 {
			return SessionView{}, ErrInvalidSession
		}
		bindings[c.Alias] = SessionBinding{
			ConnectionID:            c.ConnectionID,
			AuthorizationGeneration: c.AuthorizationGeneration,
			ActiveConnectorType:     c.ActiveConnectorType,
		}
	}

	return SessionView{
		ID:        row.ID,
		Bindings:  bindings,
		Grants:    grants,
		ExpiresAt: row.ExpiresAt,
	}, nil
}

// AuthorizeExecution re-reads the complete persisted grant and binding state.
// All policy mismatches collapse to ErrExecutionUnauthorized.
func (s *Service) AuthorizeExecution(
	ctx context.Context,
	sessionID uuid.UUID,
	exposedToolName string,
	connectionID uuid.UUID,
	toolID string,
	currentRisk connector.ToolRisk,
	currentAuthorizationGeneration int64,
) error {
	if s.q == nil ||
		sessionID == uuid.Nil ||
		connectionID == uuid.Nil ||
		toolID == "" ||
		currentAuthorizationGeneration < 1 {
		return ErrExecutionUnauthorized
	}
	alias, exposedToolID, ok := SplitExposedName(exposedToolName)
	if !ok || exposedToolID != toolID {
		return ErrExecutionUnauthorized
	}
	row, err := s.q.GetMCPExecutionAuthorization(ctx, store.GetMCPExecutionAuthorizationParams{
		SessionID: sessionID,
		Alias:     alias,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExecutionUnauthorized
	}
	if err != nil {
		return err
	}
	if row.SessionStatus != "active" ||
		!row.SessionUnexpired ||
		row.ConnectionStatus != "active" ||
		row.ConnectionID != connectionID ||
		row.BindingAuthorizationGeneration != currentAuthorizationGeneration ||
		row.CurrentAuthorizationGeneration != currentAuthorizationGeneration {
		return ErrExecutionUnauthorized
	}
	envelope, err := decodeStoredToolAllowlist(row.ToolAllowlist)
	if err != nil {
		return ErrExecutionUnauthorized
	}
	for _, grant := range envelope.Tools {
		if grant.Name == exposedToolName &&
			grant.Risk == currentRisk {
			return nil
		}
	}
	return ErrExecutionUnauthorized
}

func uniqueSortedConnectionIDs(bindings map[string]uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(bindings))
	ids := make([]uuid.UUID, 0, len(bindings))
	for _, id := range bindings {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return ids[i].String() < ids[j].String()
	})
	return ids
}

func sortedAliases(bindings map[string]uuid.UUID) []string {
	aliases := make([]string, 0, len(bindings))
	for alias := range bindings {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	return aliases
}

func validToolRisk(risk connector.ToolRisk) bool {
	switch risk {
	case connector.RiskRead, connector.RiskWrite, connector.RiskDestructive:
		return true
	default:
		return false
	}
}
