package configsvc_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/store"
)

// creationFenceDefinition 复用 policy 集成 fixture，只额外挂一个 api-key
// 方法，因为创建栅栏要同时覆盖 api-key 与 OAuth 两条插入语句。
func creationFenceDefinition() connector.Definition {
	def := configsvc.Fixture("google_ads", "https://accounts.example/token")
	def.AuthMethods = append(def.AuthMethods, connector.AuthMethod{
		Key: "token", Type: connector.AuthAPIKey, Label: "Token",
		CredentialFields: []connector.ConfigField{
			{Key: "token", Label: "Token", InputType: connector.InputText, Required: true, Secret: true},
		},
	})
	def.RemoteMCPServers[0].AuthBinding.CredentialFieldByAuthMethod =
		map[string]string{"token": "token"}
	return def
}

// fencePublic 返回一份完整 public 配置，mcp_url 决定 policy identity。
func fencePublic(mcpURL string) map[string]any {
	public := initialGoogleAdsPublic()
	public["mcp_url"] = mcpURL
	return public
}

func TestCreationStatementsFencePolicySnapshot(t *testing.T) {
	def := creationFenceDefinition()
	configs, pool := newRWService(t, def)
	q := store.New(pool)

	first, err := configs.Put(
		t.Context(),
		def.Type,
		fencePublic("https://mcp-a.example/mcp"),
		map[string]string{"client_secret": "secret-a"},
		time.Time{},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, stale, err := configs.ResolvedWithPolicy(t.Context(), def.Type)
	if err != nil {
		t.Fatal(err)
	}
	second, err := configs.Put(
		t.Context(),
		def.Type,
		fencePublic("https://mcp-b.example/mcp"),
		nil,
		first.UpdatedAt,
	)
	if err != nil {
		t.Fatal(err)
	}

	staleConnectionID := uuid.New()
	_, err = q.CreateConnectionAtPolicyIdentity(
		t.Context(),
		store.CreateConnectionAtPolicyIdentityParams{
			ID:                       staleConnectionID,
			ConnectorType:            string(def.Type),
			AuthMethod:               "token",
			Credential:               []byte("ciphertext"),
			SecretKeyVersion:         1,
			Profile:                  []byte(`{}`),
			Scopes:                   []string{},
			Status:                   "active",
			ExpectedIdentityVersion:  stale.IdentityVersion,
			ExpectedIdentityDigest:   stale.IdentityDigest,
			ExpectedDefinitionDigest: stale.DefinitionDigest,
		},
	)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale API-key insert error = %v, want no rows", err)
	}
	_, err = q.BeginInitialOAuthAuthorizationAtPolicyIdentity(
		t.Context(),
		store.BeginInitialOAuthAuthorizationAtPolicyIdentityParams{
			ConnectorType:                 string(def.Type),
			ExpectedIdentityVersion:       stale.IdentityVersion,
			ExpectedIdentityDigest:        stale.IdentityDigest,
			ExpectedDefinitionDigest:      stale.DefinitionDigest,
			ConnectionID:                  uuid.New(),
			AuthMethod:                    "oauth",
			EmptyCredential:               []byte("ciphertext"),
			ConnectionSecretKeyVersion:    1,
			RequestedScopes:               []string{},
			AuthorizationID:               uuid.New(),
			StateHash:                     "stale-state",
			ContextCiphertext:             []byte("context"),
			ExpiresAt:                     time.Now().Add(time.Minute),
			AuthorizationSecretKeyVersion: 1,
			RedirectUrl:                   "",
		},
	)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale initial authorization error = %v, want no rows", err)
	}

	_, current, err := configs.ResolvedWithPolicy(t.Context(), def.Type)
	if err != nil {
		t.Fatal(err)
	}
	apiConnectionID := uuid.New()
	if _, err := q.CreateConnectionAtPolicyIdentity(
		t.Context(),
		store.CreateConnectionAtPolicyIdentityParams{
			ID:                       apiConnectionID,
			ConnectorType:            string(def.Type),
			AuthMethod:               "token",
			Credential:               []byte("ciphertext"),
			SecretKeyVersion:         1,
			Profile:                  []byte(`{}`),
			Scopes:                   []string{},
			Status:                   "active",
			ExpectedIdentityVersion:  current.IdentityVersion,
			ExpectedIdentityDigest:   current.IdentityDigest,
			ExpectedDefinitionDigest: current.DefinitionDigest,
		},
	); err != nil {
		t.Fatal(err)
	}
	oauthConnectionID := uuid.New()
	begin, err := q.BeginInitialOAuthAuthorizationAtPolicyIdentity(
		t.Context(),
		store.BeginInitialOAuthAuthorizationAtPolicyIdentityParams{
			ConnectorType:                 string(def.Type),
			ExpectedIdentityVersion:       current.IdentityVersion,
			ExpectedIdentityDigest:        current.IdentityDigest,
			ExpectedDefinitionDigest:      current.DefinitionDigest,
			ConnectionID:                  oauthConnectionID,
			AuthMethod:                    "oauth",
			EmptyCredential:               []byte("ciphertext"),
			ConnectionSecretKeyVersion:    1,
			RequestedScopes:               []string{},
			AuthorizationID:               uuid.New(),
			StateHash:                     "current-state",
			ContextCiphertext:             []byte("context"),
			ExpiresAt:                     time.Now().Add(time.Minute),
			AuthorizationSecretKeyVersion: 1,
			RedirectUrl:                   "",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if begin.ExpectedAuthorizationGeneration != 1 {
		t.Fatalf(
			"initial expected generation = %d, want 1",
			begin.ExpectedAuthorizationGeneration,
		)
	}

	// Once the insert wins the shared-lock ordering, a later writer sees both
	// new Connections and advances them with the policy change.
	if _, err := configs.Put(
		t.Context(),
		def.Type,
		fencePublic("https://mcp-c.example/mcp"),
		nil,
		second.UpdatedAt,
	); err != nil {
		t.Fatal(err)
	}
	for _, connectionID := range []uuid.UUID{
		apiConnectionID,
		oauthConnectionID,
	} {
		row, err := q.GetConnection(t.Context(), connectionID)
		if err != nil {
			t.Fatal(err)
		}
		if row.AuthorizationGeneration != 2 {
			t.Fatalf(
				"Connection %s generation = %d, want 2",
				connectionID,
				row.AuthorizationGeneration,
			)
		}
	}
	var staleCount int
	if err := pool.QueryRow(
		t.Context(),
		`select count(*) from connections where id = $1`,
		staleConnectionID,
	).Scan(&staleCount); err != nil {
		t.Fatal(err)
	}
	if staleCount != 0 {
		t.Fatalf("stale snapshot left %d Connection(s)", staleCount)
	}
}
