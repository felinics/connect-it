package github

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/smoketest"
	"github.com/memohai/connect-it/packages/connectors/internal/toolfail"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

const (
	gitHubSmokeTokenEnv       = "CONNECT_IT_GITHUB_SMOKE_TOKEN"
	gitHubSmokeOwnerEnv       = "CONNECT_IT_GITHUB_SMOKE_OWNER"
	gitHubSmokeRepositoryEnv  = "CONNECT_IT_GITHUB_SMOKE_REPOSITORY"
	gitHubSmokeFilePathEnv    = "CONNECT_IT_GITHUB_SMOKE_FILE_PATH"
	gitHubSmokeAllowWritesEnv = "CONNECT_IT_GITHUB_SMOKE_ALLOW_WRITES"

	gitHubSmokeConnection  = "github-real-smoke"
	gitHubSmokeIssuePrefix = "connect-it GitHub real smoke "
	gitHubSmokeIssueBody   = "Created by the opt-in connect-it GitHub real " +
		"smoke test. This issue is closed automatically."

	gitHubRealSmokeCleanupLookupAttempts = 5
)

var (
	gitHubSmokeOwnerPattern = regexp.MustCompile(
		`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`,
	)
	gitHubSmokeRepositoryPattern = regexp.MustCompile(
		`^[A-Za-z0-9_.-]{1,100}$`,
	)
)

// TestGitHubRealSmoke is an opt-in real-account harness for a dedicated user
// and disposable repository. It validates a PAT and calls every published Tool
// through the official MCP Go SDK over a providerkit-guarded client.
//
// The write gate is mandatory because issue_write creates one uniquely titled
// issue. Before that mutation the harness registers a fixed REST cleanup that
// finds only the exact generated title and closes it. Nothing here logs the
// token, the profile, repository or file contents, MCP payloads, the issue
// body, or Provider error text.
func TestGitHubRealSmoke(t *testing.T) {
	config := smoketest.Config(t, "GitHub", parseGitHubSmokeConfig)

	factory, err := providerkit.NewFactoryFromEnv()
	if err != nil {
		t.Fatal("construct GitHub provider client factory")
	}
	validators, err := NewCredentialValidators(factory)
	if err != nil {
		t.Fatal("construct GitHub credential validators")
	}
	validator, exists := validators["pat"]
	if !exists {
		t.Fatal("GitHub PAT credential validator is not registered")
	}
	validation, err := validator(
		t.Context(),
		connector.CredentialValidationInput{
			ConnectorType: Definition.Type,
			AuthMethodKey: "pat",
			AuthType:      connector.AuthAPIKey,
			ConnectionID:  gitHubSmokeConnection,
			Fields:        map[string]string{"token": config.token},
		},
	)
	if err != nil {
		t.Fatal("validate GitHub PAT")
	}
	if validation.Profile.AccountID == "" ||
		validation.Profile.DisplayName == "" ||
		(validation.ScopesKnown && len(validation.GrantedScopes) == 0) {
		t.Fatal("GitHub validator returned an invalid profile or scope snapshot")
	}
	if validation.ScopesKnown {
		for _, tool := range Definition.Tools {
			for _, required := range tool.RequiredScopes {
				if !ScopeMatcher(validation.GrantedScopes, required) {
					t.Fatalf(
						"GitHub smoke credential lacks a reviewed scope for %s",
						tool.ID,
					)
				}
			}
		}
	}
	smoketest.RequireRejectedCredential(
		t,
		validator,
		connector.CredentialValidationInput{
			ConnectorType: Definition.Type,
			AuthMethodKey: "pat",
			AuthType:      connector.AuthAPIKey,
			ConnectionID:  gitHubSmokeConnection + "-invalid-auth",
			Fields: map[string]string{
				"token": "connect-it-deliberately-invalid-github-token",
			},
		},
	)

	if len(Definition.RemoteMCPServers) != 1 {
		t.Fatal("GitHub real smoke expects exactly one reviewed MCP server")
	}
	server := Definition.RemoteMCPServers[0]
	definitionRegistry := smoketest.Registry(t, Definition)
	policyClient, err := factory.NewStaticClient(providerkit.Policy{
		Provider:         string(Definition.Type),
		BaseURL:          server.Endpoint.URL,
		AllowedOrigins:   []string{"https://api.githubcopilot.com:443"},
		RedirectMode:     providerkit.RedirectFollowPolicy,
		NetworkMode:      providerkit.PublicOnly,
		RequestTimeout:   server.RequestTimeout,
		MaxResponseBytes: providerkit.DefaultMaxResponseBytes,
		Retry:            providerkit.RetryPolicy{Disabled: true},
	})
	if err != nil {
		t.Fatal("construct GitHub guarded MCP client")
	}
	t.Cleanup(policyClient.CloseIdleConnections)
	authorizer, err := providerkit.Bearer(config.token)
	if err != nil {
		t.Fatal("construct GitHub smoke authorization")
	}
	httpClient, err := policyClient.HTTPClient(
		authorizer,
		providerkit.RequestLabels{
			ConnectorType:   string(Definition.Type),
			Operation:       "real_smoke",
			AuthorizationID: gitHubSmokeConnection,
		},
	)
	if err != nil {
		t.Fatal("construct GitHub guarded MCP transport")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	session := smoketest.Connect(
		t,
		ctx,
		"connect-it-github-real-smoke",
		server.Endpoint.URL,
		httpClient,
	)
	run := func(toolID string, arguments map[string]any) {
		t.Helper()
		smoketest.RunRemote(
			t,
			ctx,
			definitionRegistry,
			Definition,
			session,
			toolID,
			arguments,
		)
	}

	run("get_me", map[string]any{})
	run("search_repositories", map[string]any{
		"query":          "repo:" + config.owner + "/" + config.repository,
		"perPage":        int64(1),
		"minimal_output": true,
	})
	run("get_file_contents", map[string]any{
		"owner": config.owner,
		"repo":  config.repository,
		"path":  config.filePath,
	})
	run("list_issues", map[string]any{
		"owner":   config.owner,
		"repo":    config.repository,
		"state":   "OPEN",
		"perPage": int64(1),
	})

	cleanupClient, err := gitHubSmokeCleanupClient(factory)
	if err != nil {
		t.Fatal("construct GitHub smoke cleanup client")
	}
	t.Cleanup(cleanupClient.CloseIdleConnections)
	issueTitle := gitHubSmokeIssuePrefix + smoketest.Marker(t)
	cleanupState := &gitHubSmokeCleanupState{title: issueTitle}
	t.Cleanup(func() {
		gitHubCleanupRealSmokeIssue(t, cleanupClient, config, cleanupState)
	})
	// The remote write may be accepted even when its MCP response is lost.
	// Mark it attempted only after cleanup is registered and immediately
	// before the call so cleanup always performs a bounded exact lookup.
	cleanupState.attempted = true
	run("issue_write", map[string]any{
		"method": "create",
		"owner":  config.owner,
		"repo":   config.repository,
		"title":  issueTitle,
		"body":   gitHubSmokeIssueBody,
	})
}

type gitHubSmokeConfiguration struct {
	token      string
	owner      string
	repository string
	filePath   string
}

type gitHubSmokeCleanupState struct {
	attempted bool
	title     string
}

func parseGitHubSmokeConfig(
	lookup smoketest.Lookup,
) (gitHubSmokeConfiguration, bool, error) {
	values, configured, err := smoketest.Values(
		"GitHub",
		lookup,
		gitHubSmokeTokenEnv,
		gitHubSmokeOwnerEnv,
		gitHubSmokeRepositoryEnv,
		gitHubSmokeFilePathEnv,
		gitHubSmokeAllowWritesEnv,
	)
	if !configured || err != nil {
		return gitHubSmokeConfiguration{}, configured, err
	}
	if err := smoketest.Exactly(
		values,
		gitHubSmokeAllowWritesEnv,
		"true",
	); err != nil {
		return gitHubSmokeConfiguration{}, true, err
	}
	owner := values[gitHubSmokeOwnerEnv]
	repository := values[gitHubSmokeRepositoryEnv]
	filePath := values[gitHubSmokeFilePathEnv]
	if !gitHubSmokeOwnerPattern.MatchString(owner) ||
		!validGitHubSmokeRepository(repository) ||
		!validGitHubSmokeFilePath(filePath) {
		return gitHubSmokeConfiguration{}, true, fmt.Errorf(
			"GitHub real smoke repository coordinates are invalid",
		)
	}
	return gitHubSmokeConfiguration{
		token:      values[gitHubSmokeTokenEnv],
		owner:      owner,
		repository: repository,
		filePath:   filePath,
	}, true, nil
}

func validGitHubSmokeRepository(value string) bool {
	return value != "." &&
		value != ".." &&
		gitHubSmokeRepositoryPattern.MatchString(value)
}

func validGitHubSmokeFilePath(value string) bool {
	if value == "" ||
		value != strings.TrimSpace(value) ||
		strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") ||
		len(value) > 1024 {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, character := range segment {
			if character < 0x20 || character == 0x7f {
				return false
			}
		}
	}
	return true
}

// The shared harness covers the env gate itself; this covers the coordinates
// that become a request path against a real repository.
func TestGitHubSmokeConfigRejectsUnsafeRepositoryCoordinates(t *testing.T) {
	t.Parallel()
	complete := map[string]string{
		gitHubSmokeTokenEnv:       "redacted-token",
		gitHubSmokeOwnerEnv:       "connect-it-smoke",
		gitHubSmokeRepositoryEnv:  "provider-fixture",
		gitHubSmokeFilePathEnv:    "README.md",
		gitHubSmokeAllowWritesEnv: "true",
	}
	for name, value := range map[string]string{
		gitHubSmokeAllowWritesEnv: "1",
		gitHubSmokeFilePathEnv:    "../secret",
		gitHubSmokeRepositoryEnv:  "..",
		gitHubSmokeOwnerEnv:       "not a login",
	} {
		values := maps.Clone(complete)
		values[name] = value
		if _, _, err := parseGitHubSmokeConfig(
			smoketest.MapLookup(values),
		); err == nil {
			t.Errorf("%s accepted an unsafe value", name)
		}
	}
	config, configured, err := parseGitHubSmokeConfig(
		smoketest.MapLookup(complete),
	)
	if !configured || err != nil ||
		config.owner != "connect-it-smoke" ||
		config.repository != "provider-fixture" ||
		config.filePath != "README.md" {
		t.Fatalf("complete GitHub smoke config = %+v err %v", config, err)
	}
}

func gitHubSmokeCleanupClient(
	factory *providerkit.Factory,
) (*providerkit.Client, error) {
	return factory.NewStaticClient(providerkit.Policy{
		Provider:         string(Definition.Type),
		BaseURL:          credentialProfileBaseURL,
		AllowedOrigins:   []string{credentialProfileBaseURL},
		RedirectMode:     providerkit.RedirectDenyAll,
		NetworkMode:      providerkit.PublicOnly,
		RequestTimeout:   providerkit.DefaultRequestTimeout,
		MaxResponseBytes: 1 << 20,
		Retry:            providerkit.RetryPolicy{Disabled: true},
	})
}

func gitHubCleanupRealSmokeIssue(
	t *testing.T,
	client *providerkit.Client,
	config gitHubSmokeConfiguration,
	state *gitHubSmokeCleanupState,
) {
	t.Helper()
	if state == nil || !state.attempted {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	if failure := gitHubCleanupRealSmokeIssueWithWait(
		ctx,
		client,
		config,
		*state,
		smoketest.WaitSecond,
	); failure != nil {
		t.Errorf(
			"cleanup GitHub smoke issue failed: code=%s upstream_status=%d",
			failure.Code,
			failure.UpstreamStatus,
		)
	}
}

func gitHubCleanupRealSmokeIssueWithWait(
	ctx context.Context,
	client *providerkit.Client,
	config gitHubSmokeConfiguration,
	state gitHubSmokeCleanupState,
	wait smoketest.Wait,
) *connector.ToolFailure {
	if !state.attempted {
		return nil
	}
	if client == nil ||
		!strings.HasPrefix(state.title, gitHubSmokeIssuePrefix) ||
		len(state.title) <= len(gitHubSmokeIssuePrefix) ||
		!gitHubSmokeOwnerPattern.MatchString(config.owner) ||
		!validGitHubSmokeRepository(config.repository) {
		return toolfail.New(connector.FailureInvalidInput, 0, 0)
	}
	return smoketest.Retry(
		ctx,
		gitHubRealSmokeCleanupLookupAttempts,
		wait,
		func(ctx context.Context) (bool, bool, *connector.ToolFailure) {
			return gitHubCleanRealSmokeIssue(ctx, client, config, state)
		},
	)
}

type gitHubSmokeIssueIdentity struct {
	Number      int64           `json:"number"`
	Title       string          `json:"title"`
	Body        string          `json:"body"`
	State       string          `json:"state"`
	PullRequest json.RawMessage `json:"pull_request"`
}

func gitHubCleanRealSmokeIssue(
	ctx context.Context,
	client *providerkit.Client,
	config gitHubSmokeConfiguration,
	state gitHubSmokeCleanupState,
) (bool, bool, *connector.ToolFailure) {
	issue, found, failure := gitHubLookupRealSmokeIssue(
		ctx,
		client,
		config,
		state.title,
	)
	if failure != nil {
		return false, smoketest.Retryable(failure), failure
	}
	if !found {
		// A remote create can be accepted while its MCP response is lost, and
		// GitHub list visibility can lag. Absence is uncertain until the
		// bounded lookup budget is exhausted.
		return false, true, nil
	}
	switch issue.State {
	case "closed":
		return true, false, nil
	case "open":
	default:
		return false, false, toolfail.New(
			connector.FailureInvalidResponse,
			http.StatusOK,
			0,
		)
	}

	confirmed, failure := gitHubCloseRealSmokeIssue(
		ctx,
		client,
		config,
		state.title,
		issue.Number,
	)
	if confirmed {
		return true, false, nil
	}
	if failure == nil {
		failure = toolfail.New(
			connector.FailureInvalidResponse,
			http.StatusOK,
			0,
		)
	}
	// A PATCH may have committed even if its response timed out, was
	// truncated, or was malformed. Retry through an exact lookup before
	// deciding whether another idempotent close is needed.
	return false, smoketest.Retryable(failure), failure
}

func gitHubLookupRealSmokeIssue(
	ctx context.Context,
	client *providerkit.Client,
	config gitHubSmokeConfiguration,
	title string,
) (gitHubSmokeIssueIdentity, bool, *connector.ToolFailure) {
	response, failure := gitHubRealSmokeRequest(
		ctx,
		client,
		config,
		providerkit.Request{
			Method: http.MethodGet,
			URL: "/repos/" + providerkit.PathSegment(config.owner) +
				"/" + providerkit.PathSegment(config.repository) +
				"/issues",
			Query: url.Values{
				"state":     {"all"},
				"per_page":  {"100"},
				"sort":      {"created"},
				"direction": {"desc"},
			},
			Headers: gitHubSmokeRESTHeaders(),
			Labels: providerkit.RequestLabels{
				ConnectorType:   string(Definition.Type),
				Operation:       "real_smoke_cleanup_lookup",
				AuthorizationID: gitHubSmokeConnection,
			},
		},
	)
	if failure != nil {
		return gitHubSmokeIssueIdentity{}, false, failure
	}
	if response.StatusCode != http.StatusOK {
		return gitHubSmokeIssueIdentity{}, false,
			gitHubSmokeResponseFailure(response)
	}
	var issues []gitHubSmokeIssueIdentity
	if err := response.DecodeJSON(&issues); err != nil || len(issues) > 100 {
		return gitHubSmokeIssueIdentity{}, false, toolfail.New(
			connector.FailureInvalidResponse,
			response.StatusCode,
			0,
		)
	}
	var exact gitHubSmokeIssueIdentity
	for _, issue := range issues {
		if issue.Title == title &&
			issue.Body == gitHubSmokeIssueBody &&
			len(issue.PullRequest) == 0 &&
			issue.Number > 0 {
			if exact.Number != 0 {
				return gitHubSmokeIssueIdentity{}, false, toolfail.New(
					connector.FailureInvalidResponse,
					response.StatusCode,
					0,
				)
			}
			exact = issue
		}
	}
	if exact.Number == 0 {
		return gitHubSmokeIssueIdentity{}, false, nil
	}
	return exact, true, nil
}

func gitHubCloseRealSmokeIssue(
	ctx context.Context,
	client *providerkit.Client,
	config gitHubSmokeConfiguration,
	title string,
	issueNumber int64,
) (bool, *connector.ToolFailure) {
	if issueNumber <= 0 {
		return false, toolfail.New(connector.FailureInvalidInput, 0, 0)
	}
	response, failure := gitHubRealSmokeRequest(
		ctx,
		client,
		config,
		providerkit.Request{
			Method: http.MethodPatch,
			URL: "/repos/" + providerkit.PathSegment(config.owner) +
				"/" + providerkit.PathSegment(config.repository) +
				"/issues/" + strconv.FormatInt(issueNumber, 10),
			Headers: gitHubSmokeRESTHeaders(),
			JSON: map[string]string{
				"state":        "closed",
				"state_reason": "not_planned",
			},
			Labels: providerkit.RequestLabels{
				ConnectorType:   string(Definition.Type),
				Operation:       "real_smoke_cleanup_close",
				AuthorizationID: gitHubSmokeConnection,
			},
		},
	)
	if failure != nil {
		return false, failure
	}
	if response.StatusCode != http.StatusOK {
		return false, gitHubSmokeResponseFailure(response)
	}
	var closed gitHubSmokeIssueIdentity
	if err := response.DecodeJSON(&closed); err != nil ||
		closed.Number != issueNumber ||
		closed.Title != title ||
		closed.Body != gitHubSmokeIssueBody ||
		closed.State != "closed" ||
		len(closed.PullRequest) != 0 {
		return false, toolfail.New(
			connector.FailureInvalidResponse,
			response.StatusCode,
			0,
		)
	}
	return true, nil
}

func gitHubRealSmokeRequest(
	ctx context.Context,
	client *providerkit.Client,
	config gitHubSmokeConfiguration,
	request providerkit.Request,
) (*providerkit.Response, *connector.ToolFailure) {
	if client == nil {
		return nil, toolfail.New(connector.FailureConfigurationError, 0, 0)
	}
	authorizer, err := providerkit.Bearer(config.token)
	if err != nil {
		return nil, toolfail.New(connector.FailureAuthorizationFailed, 0, 0)
	}
	request.Authorizer = authorizer
	response, err := client.Do(ctx, request)
	if err != nil {
		return response, providerkit.AsToolFailure(err)
	}
	if response == nil {
		return nil, toolfail.New(connector.FailureInvalidResponse, 0, 0)
	}
	return response, nil
}

func gitHubSmokeRESTHeaders() http.Header {
	return http.Header{
		"Accept":               {"application/vnd.github+json"},
		"X-GitHub-Api-Version": {restAPIVersion},
	}
}

func gitHubSmokeResponseFailure(
	response *providerkit.Response,
) *connector.ToolFailure {
	if response == nil {
		return toolfail.New(connector.FailureInvalidResponse, 0, 0)
	}
	if err := response.StatusError(time.Now()); err != nil {
		return providerkit.AsToolFailure(err)
	}
	return toolfail.New(
		connector.FailureInvalidResponse,
		response.StatusCode,
		0,
	)
}

func TestGitHubSmokeCleanupMatchesOwnedIssueAndVerifiesClosure(
	t *testing.T,
) {
	t.Parallel()
	const (
		issueNumber = int64(42)
		issueTitle  = gitHubSmokeIssuePrefix + "20260724T010203.000000000Z"
	)
	var calls int
	server := testkit.NewServerWithOptions(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls++
			testkit.AssertHeader(
				t,
				request,
				"Authorization",
				"Bearer redacted-token",
			)
			writer.Header().Set("Content-Type", "application/json")
			switch calls {
			case 1:
				if request.Method != http.MethodGet ||
					request.URL.Path != "/repos/connect-it-smoke/provider-fixture/issues" {
					t.Error("GitHub cleanup issue-list request mapping changed")
				}
				testkit.AssertQuery(t, request, "state", "all")
				testkit.AssertQuery(t, request, "per_page", "100")
				_ = json.NewEncoder(writer).Encode([]map[string]any{
					gitHubSmokeIssueFixture(issueNumber, issueTitle, "open"),
				})
			case 2:
				if request.Method != http.MethodPatch ||
					request.URL.Path != "/repos/connect-it-smoke/provider-fixture/issues/42" {
					t.Error("GitHub cleanup issue-close request mapping changed")
				}
				var body map[string]string
				testkit.DecodeJSONBody(t, request, &body)
				if body["state"] != "closed" ||
					body["state_reason"] != "not_planned" {
					t.Error("GitHub cleanup issue-close body changed")
				}
				_ = json.NewEncoder(writer).Encode(
					gitHubSmokeIssueFixture(issueNumber, issueTitle, "closed"),
				)
			default:
				t.Error("GitHub cleanup made an unexpected request")
				writer.WriteHeader(http.StatusInternalServerError)
			}
		}),
		testkit.ServerOptions{Provider: string(Definition.Type)},
	)
	gitHubCleanupRealSmokeIssue(
		t,
		server.Client,
		gitHubSmokeFixtureConfig("redacted-token"),
		&gitHubSmokeCleanupState{attempted: true, title: issueTitle},
	)
	if calls != 2 {
		t.Fatalf("GitHub cleanup calls = %d, want 2", calls)
	}
}

// Three ways a create or close can leave the account uncertain, and one shared
// requirement: never stop before an exact lookup proves the issue is closed.
// The bounded retry loop itself is tested once, in internal/smoketest.
func TestGitHubSmokeCleanupRechecksUncertainOutcomes(t *testing.T) {
	t.Parallel()
	const issueNumber = int64(43)
	tests := []struct {
		name      string
		responses []func(http.ResponseWriter, *http.Request, string)
	}{
		{
			name: "create response was lost",
			responses: []func(http.ResponseWriter, *http.Request, string){
				gitHubSmokeList(0, ""),
				gitHubSmokeList(0, ""),
				gitHubSmokeList(issueNumber, "open"),
				gitHubSmokeClose(issueNumber),
			},
		},
		{
			name: "close response was malformed",
			responses: []func(http.ResponseWriter, *http.Request, string){
				gitHubSmokeList(issueNumber, "open"),
				gitHubSmokeFailedClose(func(writer http.ResponseWriter) {
					_, _ = writer.Write([]byte(`{"number":`))
				}),
				gitHubSmokeList(issueNumber, "closed"),
			},
		},
		{
			name: "close failed temporarily",
			responses: []func(http.ResponseWriter, *http.Request, string){
				gitHubSmokeList(issueNumber, "open"),
				gitHubSmokeFailedClose(func(writer http.ResponseWriter) {
					http.Error(
						writer,
						"provider-private-close-error",
						http.StatusServiceUnavailable,
					)
				}),
				gitHubSmokeList(issueNumber, "closed"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			issueTitle := gitHubSmokeIssuePrefix + test.name
			var calls int
			server := testkit.NewServerWithOptions(
				t,
				http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					calls++
					writer.Header().Set("Content-Type", "application/json")
					if calls > len(test.responses) {
						t.Error("GitHub cleanup made an unexpected request")
						writer.WriteHeader(http.StatusInternalServerError)
						return
					}
					test.responses[calls-1](writer, request, issueTitle)
				}),
				testkit.ServerOptions{Provider: string(Definition.Type)},
			)
			var waits int
			failure := gitHubCleanupRealSmokeIssueWithWait(
				t.Context(),
				server.Client,
				gitHubSmokeFixtureConfig("redacted-token"),
				gitHubSmokeCleanupState{attempted: true, title: issueTitle},
				func(context.Context) bool {
					waits++
					return true
				},
			)
			if failure != nil {
				t.Fatalf("GitHub cleanup failure = %+v", failure)
			}
			if calls != len(test.responses) {
				t.Fatalf(
					"GitHub cleanup calls = %d, want %d",
					calls,
					len(test.responses),
				)
			}
		})
	}
}

func TestGitHubSmokeCleanupProviderErrorsAreBoundedAndRedacted(t *testing.T) {
	t.Parallel()
	const (
		issueTitle   = gitHubSmokeIssuePrefix + "provider-error"
		secretToken  = "redacted-token-must-not-escape"
		providerBody = "provider-private-error-body"
	)
	var calls int
	server := testkit.NewServerWithOptions(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls++
			if request.Header.Get("Authorization") != "Bearer "+secretToken {
				t.Error("GitHub cleanup authorization changed")
			}
			http.Error(writer, providerBody, http.StatusServiceUnavailable)
		}),
		testkit.ServerOptions{Provider: string(Definition.Type)},
	)
	failure := gitHubCleanupRealSmokeIssueWithWait(
		t.Context(),
		server.Client,
		gitHubSmokeFixtureConfig(secretToken),
		gitHubSmokeCleanupState{attempted: true, title: issueTitle},
		func(context.Context) bool { return true },
	)
	if failure == nil ||
		failure.Code != connector.FailureUpstreamUnavailable ||
		failure.UpstreamStatus != http.StatusServiceUnavailable {
		t.Fatalf("GitHub cleanup provider failure = %+v", failure)
	}
	serialized := fmt.Sprintf("%+v", failure)
	if strings.Contains(serialized, secretToken) ||
		strings.Contains(serialized, providerBody) {
		t.Fatal("GitHub cleanup failure exposed a credential or Provider body")
	}
	if calls != gitHubRealSmokeCleanupLookupAttempts {
		t.Fatalf(
			"GitHub cleanup generic-error calls = %d, want %d",
			calls,
			gitHubRealSmokeCleanupLookupAttempts,
		)
	}
}

func gitHubSmokeFixtureConfig(token string) gitHubSmokeConfiguration {
	return gitHubSmokeConfiguration{
		token:      token,
		owner:      "connect-it-smoke",
		repository: "provider-fixture",
	}
}

func gitHubSmokeIssueFixture(
	number int64,
	title string,
	state string,
) map[string]any {
	return map[string]any{
		"number": number,
		"title":  title,
		"body":   gitHubSmokeIssueBody,
		"state":  state,
	}
}

// gitHubSmokeList answers the exact-lookup GET. A zero number means the issue
// is not visible yet, which is how a lost create response looks.
func gitHubSmokeList(
	number int64,
	state string,
) func(http.ResponseWriter, *http.Request, string) {
	return func(writer http.ResponseWriter, request *http.Request, title string) {
		if request.Method != http.MethodGet {
			http.Error(writer, "expected a lookup", http.StatusBadRequest)
			return
		}
		if number == 0 {
			_, _ = writer.Write([]byte("[]"))
			return
		}
		_ = json.NewEncoder(writer).Encode([]map[string]any{
			gitHubSmokeIssueFixture(number, title, state),
		})
	}
}

func gitHubSmokeClose(
	number int64,
) func(http.ResponseWriter, *http.Request, string) {
	return func(writer http.ResponseWriter, request *http.Request, title string) {
		if request.Method != http.MethodPatch {
			http.Error(writer, "expected a close", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(writer).Encode(
			gitHubSmokeIssueFixture(number, title, "closed"),
		)
	}
}

func gitHubSmokeFailedClose(
	respond func(http.ResponseWriter),
) func(http.ResponseWriter, *http.Request, string) {
	return func(writer http.ResponseWriter, request *http.Request, _ string) {
		if request.Method != http.MethodPatch {
			http.Error(writer, "expected a close", http.StatusBadRequest)
			return
		}
		respond(writer)
	}
}
