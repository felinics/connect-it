package connsvc_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func testDef() connector.Definition {
	defaultRegion := "us"
	return connector.Definition{
		Type: "example_app", Name: "Example", ConfigSchemaVersion: 1,
		AuthMethods: []connector.AuthMethod{
			{Key: "pat", Type: connector.AuthAPIKey, Label: "PAT",
				CredentialFields: []connector.ConfigField{
					{Key: "token", Label: "Token", InputType: connector.InputText, Required: true,
						Validation: connector.FieldValidation{Pattern: `^tok_`}},
					{
						Key:          "region",
						Label:        "Region",
						InputType:    connector.InputSelect,
						DefaultValue: &defaultRegion,
						Validation: connector.FieldValidation{
							Options: []string{"us", "eu"},
						},
					},
				}},
			{Key: "oauth", Type: connector.AuthOAuth2, Label: "OAuth", OAuth: &connector.OAuthConfig{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				Egress: connector.OAuthEgressConfig{
					AuthorizationOrigins: []string{"https://example.com:443"},
					TokenOrigins:         []string{"https://example.com:443"},
				},
			}},
		},
	}
}

func newReg(t *testing.T) *registry.Registry {
	t.Helper()
	r := registry.New()
	r.MustRegister(testDef())
	return r
}

// newTestDB 是所有触库用例的公共前置：一次性的库、Queries 与 keyring。
func newTestDB(t *testing.T) (*pgxpool.Pool, *store.Queries, *crypto.Keyring) {
	t.Helper()
	pool := testutil.NewDB(t)
	kr, err := crypto.ParseKeyring("1:" + strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return pool, store.New(pool), kr
}

type staticConfigResolver struct{}

func (staticConfigResolver) ResolvedWithPolicy(
	context.Context,
	connector.Type,
) (map[string]any, configsvc.PolicySnapshot, error) {
	return map[string]any{"endpoint": "https://example.com"},
		configsvc.PolicySnapshot{}, nil
}

func validatorsForTest(
	fn connector.CredentialValidator,
) connector.CredentialValidatorMap {
	if fn == nil {
		fn = func(
			_ context.Context,
			input connector.CredentialValidationInput,
		) (connector.CredentialValidationResult, error) {
			return connector.CredentialValidationResult{
				Profile: connector.CredentialProfile{
					AccountID:   "acct_" + input.Fields["token"],
					DisplayName: "Example account",
				},
				GrantedScopes: []string{"read"},
				ScopesKnown:   true,
			}, nil
		}
	}
	return connector.CredentialValidatorMap{
		"example_app": {"pat": fn},
	}
}

func newService(
	t *testing.T,
	q *store.Queries,
	reg *registry.Registry,
	kr *crypto.Keyring,
	validator connector.CredentialValidator,
) *connsvc.Service {
	t.Helper()
	if q != nil {
		configs := configsvc.New(q, reg, kr)
		if _, err := configs.ReconcilePolicyIdentities(t.Context()); err != nil {
			t.Fatalf("startup policy reconcile: %v", err)
		}
		return connsvc.New(
			q,
			reg,
			kr,
			configs,
			validatorsForTest(validator),
			nil,
		)
	}
	return connsvc.New(
		q,
		reg,
		kr,
		staticConfigResolver{},
		validatorsForTest(validator),
		nil,
	)
}

// 校验类错误在触库前返回，用 nil store 做纯单测。
func TestCreateAPIKeyValidation(t *testing.T) {
	s := newService(t, nil, newReg(t), nil, nil)
	ctx := context.Background()

	cases := []struct {
		name    string
		typ     connector.Type
		method  string
		alias   string
		fields  map[string]string
		wantErr error
	}{
		{"alias 非法", "example_app", "pat", "Bad_Alias", nil, connsvc.ErrInvalidAlias},
		{"未知 connector", "nope", "pat", "a1", nil, connsvc.ErrUnknownConnector},
		{"未知 auth method", "example_app", "nope", "a1", nil, connsvc.ErrUnknownAuthMethod},
		{"oauth method 不能走 api-key", "example_app", "oauth", "a1", nil, connsvc.ErrWrongAuthType},
		{"缺必填字段", "example_app", "pat", "a1", map[string]string{}, connsvc.ErrInvalidFields},
		{"未声明字段", "example_app", "pat", "a1",
			map[string]string{"token": "tok_1", "extra": "x"}, connsvc.ErrInvalidFields},
		{"pattern 不符", "example_app", "pat", "a1",
			map[string]string{"token": "bad"}, connsvc.ErrInvalidFields},
		{"options 不符", "example_app", "pat", "a1",
			map[string]string{"token": "tok_1", "region": "ap"}, connsvc.ErrInvalidFields},
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
	pool, q, kr := newTestDB(t)
	var authorizationIDs sync.Map
	s := newService(
		t,
		q,
		newReg(t),
		kr,
		func(
			_ context.Context,
			input connector.CredentialValidationInput,
		) (connector.CredentialValidationResult, error) {
			if input.ConnectionID != "" || input.AuthorizationID == "" {
				t.Fatalf("create correlation = %+v", input)
			}
			if _, err := uuid.Parse(input.AuthorizationID); err != nil {
				t.Fatalf(
					"create authorization ID = %q: %v",
					input.AuthorizationID,
					err,
				)
			}
			if _, loaded := authorizationIDs.LoadOrStore(
				input.AuthorizationID,
				struct{}{},
			); loaded {
				t.Fatalf(
					"create reused authorization ID %q",
					input.AuthorizationID,
				)
			}
			return connector.CredentialValidationResult{
				Profile: connector.CredentialProfile{
					AccountID:   "acct_" + input.Fields["token"],
					DisplayName: "Example account",
				},
				GrantedScopes: []string{"read"},
				ScopesKnown:   true,
			}, nil
		},
	)
	ctx := context.Background()

	id, err := s.CreateAPIKey(ctx, "example_app", "pat", "acct-1", map[string]string{"token": "tok_abc"})
	if err != nil {
		t.Fatal(err)
	}

	// alias 不唯一：同名允许；空 alias 也允许
	dupID, err := s.CreateAPIKey(ctx, "example_app", "pat", "acct-1",
		map[string]string{"token": "tok_xyz"})
	if err != nil {
		t.Fatalf("同名 alias 应允许: %v", err)
	}
	noAliasID, err := s.CreateAPIKey(ctx, "example_app", "pat", "",
		map[string]string{"token": "tok_zzz"})
	if err != nil {
		t.Fatalf("空 alias 应允许: %v", err)
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
		t.Fatalf("视图不符: %+v", list[0])
	}

	// credential 已加密：直接读库不应出现明文
	var raw []byte
	if err := pool.QueryRow(ctx, "select credential from connections where id = $1", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "tok_abc") {
		t.Fatal("credential 应为密文")
	}
	plain, err := kr.Decrypt(raw, 1, []byte(id.String()))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := credential.UnmarshalFields(plain)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Fields["token"] != "tok_abc" ||
		stored.Fields["region"] != "us" {
		t.Fatalf("stored normalized fields = %#v", stored.Fields)
	}
	var (
		profileJSON string
		scopes      []string
		scopesKnown bool
	)
	if err := pool.QueryRow(
		ctx,
		`select profile::text, scopes, scopes_known
		   from connections where id = $1`,
		id,
	).Scan(&profileJSON, &scopes, &scopesKnown); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(profileJSON, `"account_id"`) ||
		len(scopes) != 1 ||
		scopes[0] != "read" ||
		!scopesKnown {
		t.Fatalf(
			"validated snapshot profile=%s scopes=%v known=%v",
			profileJSON,
			scopes,
			scopesKnown,
		)
	}

	view, err := s.Get(ctx, id)
	if err != nil || view.ID != id {
		t.Fatalf("get: %+v err=%v", view, err)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, id); !errors.Is(err, connsvc.ErrNotFound) {
		t.Fatalf("重复删除应 ErrNotFound, got %v", err)
	}
	if _, err := s.Get(ctx, uuid.New()); !errors.Is(err, connsvc.ErrNotFound) {
		t.Fatalf("不存在应 ErrNotFound, got %v", err)
	}
}

func TestCreateAPIKeyValidatorFailureLeavesNoConnection(t *testing.T) {
	pool, q, kr := newTestDB(t)
	validationErr := &connector.CredentialValidationError{
		Code:        connector.FailureAuthorizationFailed,
		SafeMessage: "credential is invalid",
	}
	s := newService(
		t,
		q,
		newReg(t),
		kr,
		func(
			_ context.Context,
			input connector.CredentialValidationInput,
		) (connector.CredentialValidationResult, error) {
			if input.ConnectionID != "" || input.AuthorizationID == "" {
				t.Fatalf("create correlation = %+v", input)
			}
			return connector.CredentialValidationResult{}, validationErr
		},
	)
	_, err := s.CreateAPIKey(
		t.Context(),
		"example_app",
		"pat",
		"",
		map[string]string{"token": "tok_bad"},
	)
	if !errors.Is(err, validationErr) {
		t.Fatalf("CreateAPIKey error = %v", err)
	}
	var count int
	if err := pool.QueryRow(
		t.Context(),
		`select count(*) from connections`,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("validator failure left %d connections", count)
	}
}

func TestCreateAPIKeyRejectsPolicyChangedDuringValidation(t *testing.T) {
	pool, q, kr := newTestDB(t)
	// 复用公共 fixture，只补一个参与 policy identity 的配置字段。
	def := testDef()
	def.Type = "policy_fence"
	def.Name = "Policy Fence"
	def.ConfigFields = []connector.ConfigField{{
		Key: "api_origin", Label: "API origin", InputType: connector.InputText,
		Required: true, PolicyIdentity: true,
	}}
	reg := registry.New()
	reg.MustRegister(def)
	configs := configsvc.New(q, reg, kr)
	view, err := configs.Put(
		t.Context(),
		def.Type,
		map[string]any{"api_origin": "old.example"},
		nil,
		time.Time{},
	)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	validators := connector.CredentialValidatorMap{
		def.Type: {
			"pat": func(
				ctx context.Context,
				input connector.CredentialValidationInput,
			) (connector.CredentialValidationResult, error) {
				if input.Config["api_origin"] != "old.example" {
					t.Errorf(
						"validator config = %v, want old.example",
						input.Config["api_origin"],
					)
				}
				close(started)
				select {
				case <-release:
					return connector.CredentialValidationResult{
						Profile: connector.CredentialProfile{
							AccountID: "account",
						},
					}, nil
				case <-ctx.Done():
					return connector.CredentialValidationResult{}, ctx.Err()
				}
			},
		},
	}
	service := connsvc.New(q, reg, kr, configs, validators, nil)
	result := make(chan error, 1)
	go func() {
		_, createErr := service.CreateAPIKey(
			t.Context(),
			def.Type,
			"pat",
			"",
			map[string]string{"token": "tok_secret"},
		)
		result <- createErr
	}()
	<-started
	if _, err := configs.Put(
		t.Context(),
		def.Type,
		map[string]any{"api_origin": "new.example"},
		nil,
		view.UpdatedAt,
	); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; !errors.Is(err, connsvc.ErrConflict) {
		t.Fatalf("CreateAPIKey error = %v, want policy conflict", err)
	}
	var count int
	if err := pool.QueryRow(
		t.Context(),
		`select count(*) from connections where connector_type = $1`,
		string(def.Type),
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale validation inserted %d Connection(s)", count)
	}
}

func TestRecredentialValidatorFailureLeavesCredentialAndVersionsUnchanged(
	t *testing.T,
) {
	_, q, kr := newTestDB(t)
	reg := newReg(t)
	creator := newService(t, q, reg, kr, nil)
	id, err := creator.CreateAPIKey(
		t.Context(),
		"example_app",
		"pat",
		"acct",
		map[string]string{"token": "tok_initial"},
	)
	if err != nil {
		t.Fatal(err)
	}
	before, err := q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}

	validationErr := &connector.CredentialValidationError{
		Code:        connector.FailureAuthorizationFailed,
		SafeMessage: "credential is invalid",
	}
	updater := newService(
		t,
		q,
		reg,
		kr,
		func(
			_ context.Context,
			input connector.CredentialValidationInput,
		) (connector.CredentialValidationResult, error) {
			if input.ConnectionID != id.String() ||
				input.AuthorizationID != "" {
				t.Fatalf("recredential correlation = %+v", input)
			}
			return connector.CredentialValidationResult{}, validationErr
		},
	)
	_, err = updater.RecredentialAPIKey(
		t.Context(),
		id,
		map[string]string{"token": "tok_rejected"},
	)
	if !errors.Is(err, validationErr) {
		t.Fatalf("RecredentialAPIKey error = %v", err)
	}
	after, err := q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf(
			"validator failure mutated connection\nbefore=%+v\nafter=%+v",
			before,
			after,
		)
	}
	plain, err := kr.Decrypt(
		after.Credential,
		int(after.SecretKeyVersion),
		[]byte(id.String()),
	)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := credential.UnmarshalFields(plain)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Fields["token"] != "tok_initial" {
		t.Fatalf("validator failure replaced credential: %#v", stored.Fields)
	}
}

func TestRecredentialAPIKeyUsesDualVersionCAS(t *testing.T) {
	_, q, kr := newTestDB(t)
	reg := newReg(t)
	creator := newService(t, q, reg, kr, nil)
	id, err := creator.CreateAPIKey(
		t.Context(),
		"example_app",
		"pat",
		"acct",
		map[string]string{"token": "tok_initial"},
	)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan string, 2)
	release := map[string]chan struct{}{
		"tok_slow": make(chan struct{}),
		"tok_fast": make(chan struct{}),
	}
	var closeOnce sync.Once
	validator := func(
		ctx context.Context,
		input connector.CredentialValidationInput,
	) (connector.CredentialValidationResult, error) {
		token := input.Fields["token"]
		started <- token
		select {
		case <-release[token]:
			return connector.CredentialValidationResult{
				Profile: connector.CredentialProfile{AccountID: token},
			}, nil
		case <-ctx.Done():
			return connector.CredentialValidationResult{}, ctx.Err()
		}
	}
	updater := newService(t, q, reg, kr, validator)

	type outcome struct {
		token string
		err   error
	}
	outcomes := make(chan outcome, 2)
	run := func(token string) {
		_, err := updater.RecredentialAPIKey(
			t.Context(),
			id,
			map[string]string{"token": token},
		)
		outcomes <- outcome{token: token, err: err}
	}
	go run("tok_slow")
	go run("tok_fast")
	seen := map[string]bool{}
	for len(seen) < 2 {
		seen[<-started] = true
	}
	close(release["tok_fast"])
	first := <-outcomes
	if first.token != "tok_fast" || first.err != nil {
		t.Fatalf("first outcome = %+v", first)
	}
	closeOnce.Do(func() { close(release["tok_slow"]) })
	second := <-outcomes
	if second.token != "tok_slow" ||
		!errors.Is(second.err, connsvc.ErrConflict) {
		t.Fatalf("stale outcome = %+v, want conflict", second)
	}

	row, err := q.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if row.CredentialVersion != 2 || row.AuthorizationGeneration != 2 {
		t.Fatalf(
			"versions = credential %d generation %d, want 2/2",
			row.CredentialVersion,
			row.AuthorizationGeneration,
		)
	}
	plain, err := kr.Decrypt(
		row.Credential,
		int(row.SecretKeyVersion),
		[]byte(id.String()),
	)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := credential.UnmarshalFields(plain)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Fields["token"] != "tok_fast" {
		t.Fatalf("stored stale credential: %#v", stored.Fields)
	}
}
