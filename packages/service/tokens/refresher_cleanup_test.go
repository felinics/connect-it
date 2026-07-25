package tokens

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/testutil"
)

func insertRefreshConnectionFixture(
	t *testing.T,
	pool *pgxpool.Pool,
	queries *store.Queries,
) store.Connection {
	t.Helper()
	id := uuid.New()
	alias := "cleanup-" + id.String()[:8]
	if _, err := pool.Exec(
		t.Context(),
		`insert into connections (
		   id, connector_type, alias, auth_method, credential,
		   secret_key_version, profile, scopes, status, created_at, updated_at
		 ) values ($1, 'example_app', $2, 'oauth', '\x01'::bytea, 1,
		           '{}', '{}', 'active', current_timestamp, current_timestamp)`,
		id,
		alias,
	); err != nil {
		t.Fatal(err)
	}
	created, err := queries.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func seedRequestingRefreshLease(
	t *testing.T,
) (*Refresher, *store.Queries, store.Connection, uuid.UUID) {
	t.Helper()
	pool := testutil.NewDB(t)
	queries := store.New(pool)
	created := insertRefreshConnectionFixture(t, pool, queries)
	owner := uuid.New()
	if _, err := queries.AcquireRefreshLease(
		t.Context(),
		store.AcquireRefreshLeaseParams{
			Owner:                           &owner,
			LeaseSeconds:                    refreshLeaseSeconds,
			ConnectionID:                    created.ID,
			ExpectedAuthorizationGeneration: created.AuthorizationGeneration,
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.MarkRefreshLeaseRequesting(
		t.Context(),
		store.MarkRefreshLeaseRequestingParams{
			ConnectionID:                    created.ID,
			Owner:                           &owner,
			ExpectedCredentialVersion:       created.CredentialVersion,
			ExpectedAuthorizationGeneration: created.AuthorizationGeneration,
		},
	); err != nil {
		t.Fatal(err)
	}
	requesting, err := queries.GetConnection(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &Refresher{q: queries}, queries, requesting, owner
}

// assertRefreshLease 断言 cleanup 之后的 connection 状态：state 为空表示 lease
// 必须已完全释放，非空表示 owner/租期/state 三者都还由本 owner 持有。
func assertRefreshLease(
	t *testing.T,
	queries *store.Queries,
	id uuid.UUID,
	owner uuid.UUID,
	status string,
	state string,
) {
	t.Helper()
	connection, err := queries.GetConnection(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	if connection.RefreshState != nil {
		got = *connection.RefreshState
	}
	held := connection.RefreshOwner != nil &&
		*connection.RefreshOwner == owner &&
		connection.RefreshLeaseUntil != nil
	if connection.Status != status || got != state || held != (state != "") {
		t.Fatalf("refresh lease state = %+v", connection)
	}
}

// 三种失败形态共享同一条 cleanup 路径，区别只在 lease 的去向。每个用例都用已取消
// 的 context：cleanup 必须脱离调用方取消，并始终走 owner/version/generation CAS。
func TestHandleRefreshFailureLeaseOutcomes(t *testing.T) {
	for _, test := range []struct {
		name        string
		endpointErr *oauthsvc.TokenEndpointError
		wantReauth  bool
		wantStatus  string
		wantState   string
	}{
		{
			name: "definite pre-write failure releases the lease",
			endpointErr: &oauthsvc.TokenEndpointError{
				FailureCode: connector.FailureCanceled,
				SafeMessage: "token request was canceled",
			},
			wantStatus: "active",
		},
		{
			name: "uncertain 5xx keeps the lease for no-replay expiry",
			endpointErr: &oauthsvc.TokenEndpointError{
				FailureCode:      connector.FailureUpstreamUnavailable,
				SafeMessage:      "token provider is temporarily unavailable",
				Temporary:        true,
				RequestUncertain: true,
				Status:           503,
			},
			wantStatus: "active",
			wantState:  "requesting",
		},
		{
			name: "invalid_grant marks reauth through the fenced CAS",
			endpointErr: &oauthsvc.TokenEndpointError{
				FailureCode:  connector.FailureAuthorizationFailed,
				SafeMessage:  "authorization grant is no longer valid",
				InvalidGrant: true,
				Status:       400,
			},
			wantReauth: true,
			wantStatus: "reauth_required",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			refresher, queries, row, owner := seedRequestingRefreshLease(t)
			canceledCtx, cancel := context.WithCancel(t.Context())
			cancel()

			err := refresher.handleRefreshFailure(
				canceledCtx,
				row,
				owner,
				test.endpointErr,
			)
			want := error(test.endpointErr)
			if test.wantReauth {
				want = ErrReauthRequired
			}
			if !errors.Is(err, want) {
				t.Fatalf("handleRefreshFailure error = %v, want %v", err, want)
			}
			assertRefreshLease(
				t,
				queries,
				row.ID,
				owner,
				test.wantStatus,
				test.wantState,
			)
		})
	}
}
