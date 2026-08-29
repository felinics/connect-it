package connsvc_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/felinics/connect-it/packages/core/crypto"
	"github.com/felinics/connect-it/packages/core/registry"
	"github.com/felinics/connect-it/packages/service/configsvc"
	"github.com/felinics/connect-it/packages/service/connsvc"
	"github.com/felinics/connect-it/packages/service/store"
	"github.com/felinics/connect-it/packages/service/testutil"
)

func testDef() connector.Definition {
	return connector.Definition{
		Type: "example_app", Name: "Example", ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{
			{Key: "pat", Type: connector.AuthAPIKey, Label: "PAT",
				CredentialFields: []connector.ConfigField{
					{Key: "token", Label: "Token", InputType: connector.InputText, Required: true,
						Validation: connector.FieldValidation{Pattern: `^tok_`}},
					{Key: "region", Label: "Region", InputType: connector.InputSelect,
						Validation: connector.FieldValidation{Options: []string{"us", "eu"}}},
				}},
			{Key: "oauth", Type: connector.AuthOAuth2, Label: "OAuth", OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
			}},
		},
		Implementation: connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
	}
}

func newReg(t *testing.T) *registry.Registry {
	t.Helper()
	r := registry.New()
	r.MustRegister(testDef())
	return r
}

// Validation errors are returned before touching the database, so a nil
// store is enough for a pure unit test.
func TestCreateAPIKeyValidation(t *testing.T) {
	reg := newReg(t)
	s := connsvc.New(nil, reg, configsvc.New(nil, reg, nil), nil)
	ctx := context.Background()

	cases := []struct {
		name    string
		typ     connector.Type
		method  string
		alias   string
		fields  map[string]string
		wantErr error
	}{
		{"unknown connector", "nope", "pat", "a1", nil, connsvc.ErrUnknownConnector},
		{"unknown auth method", "example_app", "nope", "a1", nil, connsvc.ErrUnknownAuthMethod},
		{"oauth method cannot go through api-key", "example_app", "oauth", "a1", nil, connsvc.ErrWrongAuthType},
		{"missing required field", "example_app", "pat", "a1", map[string]string{}, connsvc.ErrInvalidFields},
		{"undeclared field", "example_app", "pat", "a1",
			map[string]string{"token": "tok_1", "extra": "x"}, connsvc.ErrInvalidFields},
		{"pattern mismatch", "example_app", "pat", "a1",
			map[string]string{"token": "bad"}, connsvc.ErrInvalidFields},
		{"option mismatch", "example_app", "pat", "a1",
			map[string]string{"token": "tok_1", "region": "other"}, connsvc.ErrInvalidFields},
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
	q := store.New(pool)
	reg := newReg(t)
	s := connsvc.New(q, reg, configsvc.New(q, reg, kr), kr)
	ctx := context.Background()

	id, err := s.CreateAPIKey(ctx, "example_app", "pat", "acct-1", map[string]string{"token": "tok_abc"})
	if err != nil {
		t.Fatal(err)
	}
	pendingID := uuid.New()
	if _, err := pool.Exec(ctx, `insert into connections
		(id, connector_type, alias, auth_method, credential, secret_key_version,
		 profile, scopes, status, created_at, updated_at)
		values ($1, 'example_app', null, 'oauth', '\x', 1, '{}', '{}', 'pending', now(), now())`,
		pendingID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into oauth_authorizations
		(id, connector_type, state_hash, pkce_verifier, secret_key_version,
		 auth_method, connection_id, status, expires_at, created_at)
		values ($1, 'example_app', $2, '\x', 1, 'oauth', $3, 'pending',
		        now() - interval '1 minute', now())`,
		uuid.New(), uuid.NewString(), pendingID); err != nil {
		t.Fatal(err)
	}
	expired, err := s.Get(ctx, pendingID)
	if err != nil || expired.Status != "authorization_failed" {
		t.Fatalf("the sweep should settle expired authorizations: %+v err=%v", expired, err)
	}
	if err := s.Delete(ctx, pendingID); err != nil {
		t.Fatal(err)
	}

	// Aliases are not unique: duplicates are allowed, and so is an empty alias.
	dupID, err := s.CreateAPIKey(ctx, "example_app", "pat", "acct-1",
		map[string]string{"token": "tok_xyz"})
	if err != nil {
		t.Fatalf("a duplicate alias should be allowed: %v", err)
	}
	noAliasID, err := s.CreateAPIKey(ctx, "example_app", "pat", "",
		map[string]string{"token": "tok_zzz"})
	if err != nil {
		t.Fatalf("an empty alias should be allowed: %v", err)
	}
	if err := s.Delete(ctx, dupID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, noAliasID); err != nil {
		t.Fatal(err)
	}

	list, err := s.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v err=%v", list, err)
	}
	if list[0].ID != id || list[0].Alias != "acct-1" || list[0].Status != "active" {
		t.Fatalf("unexpected view: %+v", list[0])
	}

	// The credential is encrypted: reading the row directly must not reveal plaintext.
	var raw []byte
	if err := pool.QueryRow(ctx, "select credential from connections where id = $1", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tok_abc") {
		t.Fatal("credential should be ciphertext")
	}

	view, err := s.Get(ctx, id)
	if err != nil || view.ID != id {
		t.Fatalf("get: %+v err=%v", view, err)
	}
	apiTokenID, sessionID := uuid.New(), uuid.New()
	if _, err := pool.Exec(ctx, `insert into api_tokens (id, name, token_hash, created_at)
		values ($1, 'test', $2, now())`, apiTokenID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into mcp_sessions
		(id, token_hash, api_token_id, tool_snapshot, expires_at, created_at)
		values ($1, $2, $3, '{"tools":[],"routes":{}}', now() + interval '1 hour', now())`,
		sessionID, uuid.NewString(), apiTokenID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into mcp_session_connections
		(session_id, alias, connection_id) values ($1, 'example', $2)`,
		sessionID, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	var bindingsLeft int
	if err := pool.QueryRow(ctx,
		`select count(*) from mcp_session_connections where session_id = $1`,
		sessionID).Scan(&bindingsLeft); err != nil {
		t.Fatal(err)
	}
	if bindingsLeft != 0 {
		t.Fatal("deleting a connection should also delete its session bindings")
	}
	if err := s.Delete(ctx, id); !errors.Is(err, connsvc.ErrNotFound) {
		t.Fatalf("deleting twice should yield ErrNotFound, got %v", err)
	}
	if _, err := s.Get(ctx, uuid.New()); !errors.Is(err, connsvc.ErrNotFound) {
		t.Fatalf("a missing connection should yield ErrNotFound, got %v", err)
	}
}

func TestCreateAPIKeyRejectsDisabledConnector(t *testing.T) {
	pool := testutil.NewDB(t)
	q := store.New(pool)
	reg := newReg(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("ef", 32))
	if err != nil {
		t.Fatal(err)
	}
	cfg := configsvc.New(q, reg, kr)
	if err := cfg.SetEnabled(t.Context(), "example_app", false); err != nil {
		t.Fatal(err)
	}
	s := connsvc.New(q, reg, cfg, kr)
	_, err = s.CreateAPIKey(t.Context(), "example_app", "pat", "acct-1",
		map[string]string{"token": "tok_abc"})
	if !errors.Is(err, configsvc.ErrConnectorDisabled) {
		t.Fatalf("a disabled connector should reject new connections, got %v", err)
	}
}
