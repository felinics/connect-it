package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/service/dbgate"
)

func TestProductionServerDoesNotLinkDatabaseInitializer(t *testing.T) {
	t.Parallel()

	output, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list production Server dependencies: %v\n%s", err, output)
	}
	const initializerPackage = "github.com/memohai/connect-it/packages/service/dbmigrate"
	for _, dependency := range strings.Fields(string(output)) {
		if dependency == initializerPackage {
			t.Fatalf("production Server links database initializer %s", dependency)
		}
		if strings.Contains(dependency, "github.com/golang-migrate/") {
			t.Fatalf("production Server links legacy migration library %s", dependency)
		}
	}
}

func TestGatedPoolConfigRunsTheGateOnEveryPhysicalConnection(t *testing.T) {
	t.Parallel()

	gateCalls := 0
	config, err := newGatedPoolConfig(
		"postgres://user:secret@localhost:5433/connect_it?sslmode=disable",
		func(context.Context, *pgx.Conn) error {
			gateCalls++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("newGatedPoolConfig: %v", err)
	}
	if config.AfterConnect == nil {
		t.Fatal("pool does not gate physical connections")
	}
	if err := config.AfterConnect(context.Background(), nil); err != nil {
		t.Fatalf("AfterConnect: %v", err)
	}
	if gateCalls != 1 {
		t.Fatalf("gate calls per physical connection = %d, want 1", gateCalls)
	}

	gateErr := errors.New("identity mismatch")
	rejecting, err := newGatedPoolConfig(
		"postgres://user:secret@localhost:5433/connect_it?sslmode=disable",
		func(context.Context, *pgx.Conn) error { return gateErr },
	)
	if err != nil {
		t.Fatalf("newGatedPoolConfig: %v", err)
	}
	if !errors.Is(
		rejecting.AfterConnect(context.Background(), nil),
		gateErr,
	) {
		t.Fatal("a rejecting gate does not fail the physical connection")
	}
}

func TestGatedPoolConfigRejectsUngatedOrUnparsableInput(t *testing.T) {
	t.Parallel()

	const url = "postgres://user:secret@localhost:5433/connect_it"
	if _, err := newGatedPoolConfig(url, nil); err == nil {
		t.Fatal("an ungated pool config was accepted")
	}
	_, err := newGatedPoolConfig(
		"postgres://user:secret@localhost:notaport/connect_it",
		func(context.Context, *pgx.Conn) error { return nil },
	)
	if err == nil {
		t.Fatal("an unparsable DATABASE_URL was accepted")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("DATABASE_URL credential leaked into %q", err)
	}
}

func TestLoadExpectedDatabaseIdentity(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		dbgate.EnvExpectedDatabaseName:             "connect_it",
		dbgate.EnvExpectedDatabaseSchema:           "connect_it_app",
		dbgate.EnvExpectedDatabaseSystemIdentifier: "7234567890123456789",
		dbgate.EnvExpectedDatabaseApplicationRole:  "connect_it_app",
		dbgate.EnvExpectedDatabaseOwnerRole:        "connect_it_owner",
		"DATABASE_URL":                             "postgres://user:secret@example.invalid/wrong",
	}
	var requested []string
	expected, err := loadExpectedDatabaseIdentity(func(name string) string {
		requested = append(requested, name)
		return values[name]
	})
	if err != nil {
		t.Fatalf("loadExpectedDatabaseIdentity: %v", err)
	}
	if expected.Database != values[dbgate.EnvExpectedDatabaseName] ||
		expected.Schema != values[dbgate.EnvExpectedDatabaseSchema] ||
		expected.SystemIdentifier !=
			values[dbgate.EnvExpectedDatabaseSystemIdentifier] ||
		expected.ApplicationRole !=
			values[dbgate.EnvExpectedDatabaseApplicationRole] ||
		expected.OwnerRole != values[dbgate.EnvExpectedDatabaseOwnerRole] {
		t.Fatalf("loaded database identity = %#v", expected)
	}
	if strings.Contains(strings.Join(requested, ","), "DATABASE_URL") {
		t.Fatalf(
			"expected identity was inferred from DATABASE_URL; requested = %v",
			requested,
		)
	}
}

func TestLoadExpectedDatabaseIdentityRejectsMissingConfiguration(t *testing.T) {
	t.Parallel()

	for _, missing := range []string{
		dbgate.EnvExpectedDatabaseName,
		dbgate.EnvExpectedDatabaseSchema,
		dbgate.EnvExpectedDatabaseSystemIdentifier,
		dbgate.EnvExpectedDatabaseApplicationRole,
		dbgate.EnvExpectedDatabaseOwnerRole,
	} {
		missing := missing
		t.Run(missing, func(t *testing.T) {
			t.Parallel()

			values := map[string]string{
				dbgate.EnvExpectedDatabaseName:             "connect_it",
				dbgate.EnvExpectedDatabaseSchema:           "connect_it_app",
				dbgate.EnvExpectedDatabaseSystemIdentifier: "7234567890123456789",
				dbgate.EnvExpectedDatabaseApplicationRole:  "connect_it_app",
				dbgate.EnvExpectedDatabaseOwnerRole:        "connect_it_owner",
			}
			delete(values, missing)
			_, err := loadExpectedDatabaseIdentity(func(name string) string {
				return values[name]
			})
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf(
					"missing %s error = %v, want variable name",
					missing,
					err,
				)
			}
		})
	}

	if _, err := loadExpectedDatabaseIdentity(nil); err == nil {
		t.Fatal("nil environment reader was accepted")
	}
}

func TestCookieSecureForBaseURL(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		raw    string
		secure bool
	}{
		{"HTTPS origin", "https://connect.example", true},
		{"HTTPS path", "HTTPS://Connect.Example/tenant", true},
		{"HTTP local development", "http://localhost:8080", false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := cookieSecureForBaseURL(tc.raw)
			if err != nil {
				t.Fatalf("cookieSecureForBaseURL(%q): %v", tc.raw, err)
			}
			if got != tc.secure {
				t.Fatalf(
					"cookieSecureForBaseURL(%q) = %t, want %t",
					tc.raw,
					got,
					tc.secure,
				)
			}
		})
	}
}

func TestCookieSecureForBaseURLRejectsInvalidPublicURL(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"",
		"/relative",
		"ftp://connect.example",
		"https://user:secret@connect.example",
		"https://connect.example?next=/admin",
		"https://connect.example/#fragment",
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if _, err := cookieSecureForBaseURL(raw); err == nil {
				t.Fatalf("cookieSecureForBaseURL(%q) error = nil", raw)
			}
		})
	}
}

func TestProviderRequestObserverRequiresWriter(t *testing.T) {
	t.Parallel()

	if _, err := newProviderRequestObserver(nil, 1); !errors.Is(
		err,
		errProviderObserverWriterRequired,
	) {
		t.Fatalf("nil writer error = %v", err)
	}
}

func TestProviderRequestObserverLogsOnlyRequestEventWhitelist(
	t *testing.T,
) {
	t.Parallel()

	var output bytes.Buffer
	observer, err := newProviderRequestObserver(&output, 4)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(
		context.Background(),
		struct{ name string }{"sensitive"},
		"context-secret-must-not-be-logged",
	)
	observer.Observe(ctx, providerkit.RequestEvent{
		Labels: providerkit.RequestLabels{
			ConnectorType: "github",
			ToolID:        "search_issues",
			ConnectionID:  "connection-id",
		},
		ProviderHost:   "api.github.com",
		Method:         "GET",
		Duration:       25 * time.Millisecond,
		Attempt:        2,
		UpstreamStatus: 429,
		ErrorCode:      connector.FailureRateLimited,
		ResponseBytes:  128,
		PolicyOutcome:  providerkit.PolicyOutcomeAllowed,
	})
	closeCtx, cancel := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cancel()
	if err := observer.Close(closeCtx); err != nil {
		t.Fatal(err)
	}

	raw := strings.TrimSpace(output.String())
	if strings.Contains(raw, "context-secret-must-not-be-logged") {
		t.Fatal("context value leaked into Provider request log")
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatalf("structured Provider log = %q: %v", raw, err)
	}
	allowedKeys := map[string]bool{
		"time":            true,
		"level":           true,
		"msg":             true,
		"connector_type":  true,
		"tool_id":         true,
		"connection_id":   true,
		"provider_host":   true,
		"method":          true,
		"duration":        true,
		"attempt":         true,
		"upstream_status": true,
		"error_code":      true,
		"response_bytes":  true,
		"policy_outcome":  true,
	}
	for key := range record {
		if !allowedKeys[key] {
			t.Fatalf("unexpected Provider log field %q: %s", key, raw)
		}
	}
	for key := range allowedKeys {
		if _, exists := record[key]; !exists {
			t.Fatalf("missing Provider log field %q: %s", key, raw)
		}
	}
	if record["msg"] != "provider_request" ||
		record["connector_type"] != "github" ||
		record["tool_id"] != "search_issues" ||
		record["connection_id"] != "connection-id" ||
		record["provider_host"] != "api.github.com" ||
		record["method"] != "GET" ||
		record["error_code"] != string(connector.FailureRateLimited) ||
		record["policy_outcome"] !=
			string(providerkit.PolicyOutcomeAllowed) {
		t.Fatalf("Provider log = %#v", record)
	}
}

func TestProviderRequestObserverOperationUsesAuthorizationCorrelation(
	t *testing.T,
) {
	t.Parallel()

	var output bytes.Buffer
	observer, err := newProviderRequestObserver(&output, 1)
	if err != nil {
		t.Fatal(err)
	}
	observer.Observe(context.Background(), providerkit.RequestEvent{
		Labels: providerkit.RequestLabels{
			ConnectorType:   "github",
			Operation:       "oauth_exchange",
			AuthorizationID: "authorization-id",
		},
		ProviderHost:  "github.com",
		Method:        "POST",
		Duration:      time.Millisecond,
		Attempt:       1,
		ResponseBytes: 0,
		PolicyOutcome: providerkit.PolicyOutcomeAllowed,
	})
	closeCtx, cancel := context.WithTimeout(
		context.Background(),
		time.Second,
	)
	defer cancel()
	if err := observer.Close(closeCtx); err != nil {
		t.Fatal(err)
	}

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &record); err != nil {
		t.Fatal(err)
	}
	if record["operation"] != "oauth_exchange" ||
		record["authorization_id"] != "authorization-id" {
		t.Fatalf("operation log = %#v", record)
	}
	for _, forbidden := range []string{
		"tool_id",
		"connection_id",
		"url",
		"query",
		"request_body",
		"response_body",
		"error",
	} {
		if _, exists := record[forbidden]; exists {
			t.Fatalf("operation log contains forbidden field %q", forbidden)
		}
	}
}
