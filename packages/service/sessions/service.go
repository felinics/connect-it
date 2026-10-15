// Package sessions manages short-lived aggregate MCP capability snapshots.
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
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felinics/connect-it/packages/service/store"
)

const (
	DefaultTTL           = time.Hour
	MaxTTL               = 24 * time.Hour
	toolDiscoveryTimeout = 30 * time.Second
	maxSnapshotTools     = 1024
	maxSnapshotBytes     = 2 << 20
	discoveryWorkers     = 4
)

var (
	aliasPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
)

var (
	// ErrInvalidSession means that a token is missing, expired, revoked, or
	// references a Connection that no longer belongs to the snapshot.
	ErrInvalidSession = errors.New("sessions: invalid session token")
	ErrToolDiscovery  = errors.New("sessions: tool discovery failed")
)

type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }

// ToolRoute maps one exposed, namespaced tool back to its owning Connection
// and the original tool name understood by that Connector implementation.
type ToolRoute struct {
	Alias        string    `json:"alias"`
	ConnectionID uuid.UUID `json:"connection_id"`
	ToolName     string    `json:"tool_name"`
}

type toolSnapshot struct {
	Tools  []*mcp.Tool          `json:"tools"`
	Routes map[string]ToolRoute `json:"routes"`
}

type SessionView struct {
	ID         uuid.UUID
	APITokenID uuid.UUID
	Tools      []*mcp.Tool
	Routes     map[string]ToolRoute
	ExpiresAt  time.Time
}

type ToolLister interface {
	ListTools(ctx context.Context, connectionID uuid.UUID) ([]*mcp.Tool, error)
}

type Service struct {
	q      *store.Queries
	lister ToolLister
}

// DiscoveryFailure reports one alias whose Connection could not list its
// tools when the session was created. The session still carries every other
// alias; the caller decides whether to surface the failure or retry later.
type DiscoveryFailure struct {
	Alias        string
	ConnectionID uuid.UUID
	Error        string
}

type CreateResult struct {
	Token     string
	ExpiresAt time.Time
	// FailedConnections lists aliases excluded from the snapshot because
	// tool discovery failed. Empty when every alias was discovered.
	FailedConnections []DiscoveryFailure
}

func New(q *store.Queries, lister ToolLister) *Service {
	return &Service{q: q, lister: lister}
}

// Create discovers tools for every alias→Connection binding and signs one
// immutable aggregate snapshot. An empty allowlist includes every discovered
// tool; a non-empty allowlist uses exposed alias__tool names.
//
// Discovery is per alias: an upstream that fails to list its tools is left
// out of the snapshot and reported in CreateResult.FailedConnections, so one
// broken Connection does not take the other Connections down with it. Only
// when every alias fails (or the allowlist names a tool of a failed alias)
// does Create return ErrToolDiscovery.
func (s *Service) Create(
	ctx context.Context,
	apiTokenID uuid.UUID,
	bindings map[string]uuid.UUID,
	toolAllowlist []string,
	ttl time.Duration,
) (CreateResult, error) {
	if apiTokenID == uuid.Nil {
		return CreateResult{}, &ValidationError{Code: "invalid_api_token", Message: "api token identity is required"}
	}
	if len(bindings) == 0 {
		return CreateResult{}, &ValidationError{Code: "empty_bindings", Message: "at least one connection binding is required"}
	}

	connectionAliases := make(map[uuid.UUID]string, len(bindings))
	for alias, connectionID := range bindings {
		if !aliasPattern.MatchString(alias) {
			return CreateResult{}, &ValidationError{Code: "invalid_alias",
				Message: fmt.Sprintf("alias %q must match %s", alias, aliasPattern)}
		}
		if connectionID == uuid.Nil {
			return CreateResult{}, &ValidationError{Code: "invalid_connection_id",
				Message: fmt.Sprintf("alias %q has an empty connection id", alias)}
		}
		if previous, duplicate := connectionAliases[connectionID]; duplicate {
			return CreateResult{}, &ValidationError{Code: "duplicate_connection",
				Message: fmt.Sprintf("connection %s is bound by both %q and %q", connectionID, previous, alias)}
		}
		connectionAliases[connectionID] = alias
		row, err := s.q.GetConnection(ctx, connectionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return CreateResult{}, &ValidationError{Code: "unknown_connection",
				Message: fmt.Sprintf("connection %s (alias %q) does not exist", connectionID, alias)}
		}
		if err != nil {
			return CreateResult{}, err
		}
		if row.Status != "active" {
			return CreateResult{}, &ValidationError{Code: "connection_not_active",
				Message: fmt.Sprintf("connection %s (alias %q) is %s", connectionID, alias, row.Status)}
		}
	}

	requestedTools := make(map[string]bool, len(toolAllowlist))
	for _, name := range toolAllowlist {
		if !toolNamePattern.MatchString(name) {
			return CreateResult{}, &ValidationError{Code: "invalid_allowlist",
				Message: fmt.Sprintf("entry %q is not a valid exposed tool name", name)}
		}
		if requestedTools[name] {
			return CreateResult{}, &ValidationError{Code: "invalid_allowlist",
				Message: fmt.Sprintf("entry %q is duplicated", name)}
		}
		requestedTools[name] = true
	}
	if len(requestedTools) > maxSnapshotTools {
		return CreateResult{}, &ValidationError{Code: "too_many_tools",
			Message: fmt.Sprintf("at most %d allowlist entries are allowed", maxSnapshotTools)}
	}

	switch {
	case ttl < 0:
		return CreateResult{}, &ValidationError{Code: "invalid_ttl", Message: "ttl must not be negative"}
	case ttl == 0:
		ttl = DefaultTTL
	case ttl > MaxTTL:
		return CreateResult{}, &ValidationError{Code: "invalid_ttl", Message: "ttl must not exceed 24h"}
	}

	snapshotJSON, failed, err := s.discoverTools(ctx, bindings, requestedTools)
	if err != nil {
		return CreateResult{}, err
	}
	failedAliases := make(map[string]bool, len(failed))
	for _, failure := range failed {
		failedAliases[failure.Alias] = true
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return CreateResult{}, err
	}
	token := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	sessionID := uuid.New()
	expiresAt := time.Now().UTC().Add(ttl)

	if err := s.q.DeleteExpiredMCPSessions(ctx); err != nil {
		return CreateResult{}, err
	}
	tx, qtx, err := s.q.BeginTx(ctx)
	if err != nil {
		return CreateResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := qtx.CreateMCPSession(ctx, store.CreateMCPSessionParams{
		ID:           sessionID,
		TokenHash:    hex.EncodeToString(sum[:]),
		ApiTokenID:   apiTokenID,
		ToolSnapshot: snapshotJSON,
		ExpiresAt:    expiresAt,
	}); err != nil {
		return CreateResult{}, err
	}
	// Only aliases that contributed to the snapshot are bound to the session:
	// a failed Connection has no routes, so binding it would only let a token
	// outlive the retry that is supposed to pick the Connection up again.
	aliases := sortedAliases(bindings)
	for _, alias := range aliases {
		if failedAliases[alias] {
			continue
		}
		if err := qtx.AddMCPSessionConnection(ctx, store.AddMCPSessionConnectionParams{
			SessionID:    sessionID,
			Alias:        alias,
			ConnectionID: bindings[alias],
		}); err != nil {
			return CreateResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return CreateResult{}, err
	}
	return CreateResult{Token: token, ExpiresAt: expiresAt, FailedConnections: failed}, nil
}

type discoveryJob struct {
	alias        string
	connectionID uuid.UUID
}

type discoveryResult struct {
	alias        string
	connectionID uuid.UUID
	tools        []*mcp.Tool
	err          error
}

// discoverTools lists every alias in parallel and encodes the snapshot of
// the aliases that succeeded. The returned failures are sorted by alias; the
// error is non-nil only when nothing could be discovered at all or when the
// allowlist depends on a failed alias.
func (s *Service) discoverTools(
	ctx context.Context,
	bindings map[string]uuid.UUID,
	requested map[string]bool,
) ([]byte, []DiscoveryFailure, error) {
	if s.lister == nil {
		return nil, nil, fmt.Errorf("%w: tool lister is not configured", ErrToolDiscovery)
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, toolDiscoveryTimeout)
	defer cancel()

	aliases := sortedAliases(bindings)
	jobs := make(chan discoveryJob, len(aliases))
	results := make(chan discoveryResult, len(aliases))
	for _, alias := range aliases {
		jobs <- discoveryJob{alias: alias, connectionID: bindings[alias]}
	}
	close(jobs)

	workers := discoveryWorkers
	if len(aliases) < workers {
		workers = len(aliases)
	}
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for job := range jobs {
				tools, err := s.lister.ListTools(discoveryCtx, job.connectionID)
				results <- discoveryResult{
					alias: job.alias, connectionID: job.connectionID, tools: tools, err: err,
				}
			}
		}()
	}
	wg.Wait()
	close(results)

	byAlias := make(map[string]discoveryResult, len(aliases))
	var failed []DiscoveryFailure
	for result := range results {
		if result.err != nil {
			// Keep the alias in the message: operators pair this line with the
			// downstream's own discovery warning by alias and timestamp.
			slog.WarnContext(ctx, fmt.Sprintf("sessions: tool discovery failed for alias %q: %v", result.alias, result.err),
				slog.String("connection_id", result.connectionID.String()),
				slog.String("alias", result.alias))
			failed = append(failed, DiscoveryFailure{
				Alias:        result.alias,
				ConnectionID: result.connectionID,
				Error:        result.err.Error(),
			})
			continue
		}
		byAlias[result.alias] = result
	}
	sort.Slice(failed, func(i, j int) bool { return failed[i].Alias < failed[j].Alias })
	if len(failed) == len(aliases) {
		return nil, nil, fmt.Errorf("%w for alias %q: %s", ErrToolDiscovery, failed[0].Alias, failed[0].Error)
	}
	for _, failure := range failed {
		for name := range requested {
			if requestedAlias(name) == failure.Alias {
				return nil, nil, fmt.Errorf("%w for alias %q: %s", ErrToolDiscovery, failure.Alias, failure.Error)
			}
		}
	}

	snapshot := toolSnapshot{
		Tools:  make([]*mcp.Tool, 0),
		Routes: make(map[string]ToolRoute),
	}
	for _, alias := range aliases {
		result, discovered := byAlias[alias]
		if !discovered {
			continue
		}
		seenOriginal := make(map[string]bool)
		for _, tool := range result.tools {
			if tool == nil {
				slog.WarnContext(ctx, "skipping upstream MCP tool",
					slog.String("connection_id", result.connectionID.String()),
					slog.String("alias", alias),
					slog.String("reason", "nil_tool"))
				continue
			}
			if !toolNamePattern.MatchString(tool.Name) {
				slog.WarnContext(ctx, "skipping upstream MCP tool",
					slog.String("connection_id", result.connectionID.String()),
					slog.String("alias", alias),
					slog.String("tool_name", tool.Name),
					slog.String("reason", "invalid_name"))
				continue
			}
			if seenOriginal[tool.Name] {
				slog.WarnContext(ctx, "skipping upstream MCP tool",
					slog.String("connection_id", result.connectionID.String()),
					slog.String("alias", alias),
					slog.String("tool_name", tool.Name),
					slog.String("reason", "duplicate_name"))
				continue
			}
			seenOriginal[tool.Name] = true
			exposedName := alias + "__" + tool.Name
			if !toolNamePattern.MatchString(exposedName) {
				slog.WarnContext(ctx, "skipping upstream MCP tool",
					slog.String("connection_id", result.connectionID.String()),
					slog.String("alias", alias),
					slog.String("tool_name", tool.Name),
					slog.String("reason", "invalid_exposed_name"))
				continue
			}
			copy := *tool
			copy.Name = exposedName
			snapshot.Tools = append(snapshot.Tools, &copy)
			snapshot.Routes[exposedName] = ToolRoute{
				Alias: alias, ConnectionID: result.connectionID, ToolName: tool.Name,
			}
		}
	}

	if len(requested) > 0 {
		for name := range requested {
			if _, found := snapshot.Routes[name]; !found {
				return nil, nil, &ValidationError{Code: "invalid_allowlist",
					Message: fmt.Sprintf("tool %q was not discovered", name)}
			}
		}
		filtered := make([]*mcp.Tool, 0, len(requested))
		for _, tool := range snapshot.Tools {
			if requested[tool.Name] {
				filtered = append(filtered, tool)
			} else {
				delete(snapshot.Routes, tool.Name)
			}
		}
		snapshot.Tools = filtered
	}

	sort.Slice(snapshot.Tools, func(i, j int) bool {
		return snapshot.Tools[i].Name < snapshot.Tools[j].Name
	})
	if len(snapshot.Tools) > maxSnapshotTools {
		return nil, nil, fmt.Errorf("%w: snapshot contains more than %d tools",
			ErrToolDiscovery, maxSnapshotTools)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encode snapshot: %v", ErrToolDiscovery, err)
	}
	if len(encoded) > maxSnapshotBytes {
		return nil, nil, fmt.Errorf("%w: snapshot exceeds %d bytes", ErrToolDiscovery, maxSnapshotBytes)
	}
	return encoded, failed, nil
}

// requestedAlias returns the alias part of an exposed alias__tool name, or ""
// when the name has no namespace.
func requestedAlias(exposedName string) string {
	alias, _, ok := strings.Cut(exposedName, "__")
	if !ok {
		return ""
	}
	return alias
}

func sortedAliases(bindings map[string]uuid.UUID) []string {
	aliases := make([]string, 0, len(bindings))
	for alias := range bindings {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	return aliases
}

// Resolve validates the token and reconstructs its immutable aggregate view.
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

	var snapshot toolSnapshot
	if err := json.Unmarshal(row.ToolSnapshot, &snapshot); err != nil {
		return SessionView{}, fmt.Errorf("sessions: corrupt tool_snapshot for session %s: %w", row.ID, err)
	}
	if len(snapshot.Tools) > maxSnapshotTools || snapshot.Routes == nil {
		return SessionView{}, fmt.Errorf("sessions: corrupt tool_snapshot for session %s", row.ID)
	}
	connections, err := s.q.ListMCPSessionConnections(ctx, row.ID)
	if err != nil {
		return SessionView{}, err
	}
	bindings := make(map[string]uuid.UUID, len(connections))
	for _, connection := range connections {
		bindings[connection.Alias] = connection.ConnectionID
	}
	if len(bindings) == 0 {
		return SessionView{}, ErrInvalidSession
	}

	seenTools := make(map[string]bool, len(snapshot.Tools))
	for _, tool := range snapshot.Tools {
		if tool == nil || tool.Name == "" || seenTools[tool.Name] {
			return SessionView{}, fmt.Errorf("sessions: corrupt tool_snapshot for session %s", row.ID)
		}
		route, ok := snapshot.Routes[tool.Name]
		if !ok || route.ToolName == "" || bindings[route.Alias] != route.ConnectionID {
			return SessionView{}, ErrInvalidSession
		}
		seenTools[tool.Name] = true
	}
	if len(seenTools) != len(snapshot.Routes) {
		return SessionView{}, fmt.Errorf("sessions: corrupt tool_snapshot for session %s", row.ID)
	}

	return SessionView{
		ID:         row.ID,
		APITokenID: row.ApiTokenID,
		Tools:      snapshot.Tools,
		Routes:     snapshot.Routes,
		ExpiresAt:  row.ExpiresAt,
	}, nil
}
