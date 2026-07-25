package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/smoketest"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

const (
	gmailSmokeAccessTokenEnv  = "CONNECT_IT_GMAIL_SMOKE_ACCESS_TOKEN"
	gmailSmokeCleanupTokenEnv = "CONNECT_IT_GMAIL_SMOKE_CLEANUP_ACCESS_TOKEN"
	gmailSmokeMailboxEnv      = "CONNECT_IT_GMAIL_SMOKE_MAILBOX"
	gmailSmokeAllowWritesEnv  = "CONNECT_IT_GMAIL_SMOKE_ALLOW_WRITES"

	gmailSmokeConnection    = "gmail-real-smoke"
	gmailSmokeSubjectPrefix = "connect-it Gmail real smoke "

	gmailSmokeCleanupLookupAttempts = 15
	gmailSmokeCleanupDeleteAttempts = 3
)

var gmailSmokeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

// TestGmailRealSmoke is an opt-in real-account harness for one dedicated
// mailbox. It validates the OAuth credential and calls the published Managed
// and Remote MCP Tools.
//
// Both writes use unique markers. Before either write, the harness registers
// fixed, providerkit-guarded REST cleanup. Draft cleanup locates the exact
// generated Subject and recipient before deleting the draft; sent-mail cleanup
// verifies the returned message ID and headers before permanently deleting only
// that smoke message. A separate janitor token with https://mail.google.com/
// scope is required because the Connector's reviewed readonly+compose grant
// cannot delete messages. The harness never logs tokens, mailbox payloads,
// Subjects, bodies, MCP results, or Provider error text.
func TestGmailRealSmoke(t *testing.T) {
	config := smoketest.Config(t, "Gmail", parseGmailSmokeConfig)

	factory, err := providerkit.NewFactoryFromEnv()
	if err != nil {
		t.Fatal("construct Gmail provider client factory")
	}
	validators, err := NewCredentialValidators(factory)
	if err != nil {
		t.Fatal("construct Gmail credential validators")
	}
	validator, exists := validators["oauth"]
	if !exists {
		t.Fatal("Gmail OAuth credential validator is not registered")
	}
	gmailValidateSmokeCredential(
		t,
		validator,
		config.accessToken,
		config.mailbox,
		gmailSmokeConnection,
	)
	gmailValidateSmokeCredential(
		t,
		validator,
		config.cleanupToken,
		config.mailbox,
		gmailSmokeConnection+"-cleanup",
	)
	smoketest.RequireRejectedCredential(
		t,
		validator,
		gmailSmokeCredentialInput(
			"connect-it-deliberately-invalid-gmail-token",
			gmailSmokeConnection+"-invalid-auth",
		),
	)

	handlers, err := NewHandlers(factory)
	if err != nil {
		t.Fatal("construct Gmail handlers")
	}
	if len(Definition.RemoteMCPServers) != 1 {
		t.Fatal("Gmail real smoke expects exactly one reviewed MCP server")
	}
	server := Definition.RemoteMCPServers[0]
	definitionRegistry := smoketest.Registry(
		t,
		Definition,
		"list_messages",
		"send_message",
	)
	policyClient, err := factory.NewStaticClient(providerkit.Policy{
		Provider:         string(Definition.Type),
		BaseURL:          server.Endpoint.URL,
		AllowedOrigins:   []string{"https://gmailmcp.googleapis.com:443"},
		RedirectMode:     providerkit.RedirectFollowPolicy,
		NetworkMode:      providerkit.PublicOnly,
		RequestTimeout:   server.RequestTimeout,
		MaxResponseBytes: providerkit.DefaultMaxResponseBytes,
		Retry:            providerkit.RetryPolicy{Disabled: true},
	})
	if err != nil {
		t.Fatal("construct Gmail guarded MCP client")
	}
	t.Cleanup(policyClient.CloseIdleConnections)
	authorizer, err := providerkit.Bearer(config.accessToken)
	if err != nil {
		t.Fatal("construct Gmail smoke authorization")
	}
	httpClient, err := policyClient.HTTPClient(
		authorizer,
		providerkit.RequestLabels{
			ConnectorType:   string(Definition.Type),
			Operation:       "real_smoke",
			AuthorizationID: gmailSmokeConnection,
		},
	)
	if err != nil {
		t.Fatal("construct Gmail guarded MCP transport")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	session := smoketest.Connect(
		t,
		ctx,
		"connect-it-gmail-real-smoke",
		server.Endpoint.URL,
		httpClient,
	)
	runManaged := func(toolID string, arguments map[string]any) json.RawMessage {
		t.Helper()
		return smoketest.Run(t, definitionRegistry, handlers, smoketest.TokenCall(
			Definition,
			gmailSmokeConnection,
			toolID,
			arguments,
			config.accessToken,
		))
	}
	runRemote := func(toolID string, arguments map[string]any) {
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

	readQuery := `subject:"connect-it Gmail real smoke"`
	runManaged("list_messages", map[string]any{
		"q":           readQuery,
		"max_results": float64(1),
	})
	runRemote("search_threads", map[string]any{
		"query":    readQuery,
		"pageSize": int64(1),
	})

	cleanupClient, err := gmailSmokeCleanupClient(factory)
	if err != nil {
		t.Fatal("construct Gmail smoke cleanup client")
	}
	t.Cleanup(cleanupClient.CloseIdleConnections)

	marker := smoketest.Marker(t)
	body := "connect-it opt-in Gmail real smoke marker " + marker

	draftSubject := gmailSmokeSubjectPrefix + marker + " draft"
	draftAttempted := false
	t.Cleanup(func() {
		gmailCleanupRealSmokeDraft(
			t,
			cleanupClient,
			config,
			draftSubject,
			draftAttempted,
			smoketest.WaitSecond,
		)
	})
	draftAttempted = true
	runRemote("create_draft", map[string]any{
		"to":      config.mailbox,
		"subject": draftSubject,
		"body":    body,
	})

	sentSubject := gmailSmokeSubjectPrefix + marker + " sent"
	sentAttempted := false
	sentMessageID := ""
	t.Cleanup(func() {
		gmailCleanupRealSmokeMessage(
			t,
			cleanupClient,
			config,
			sentSubject,
			sentMessageID,
			sentAttempted,
			smoketest.WaitSecond,
		)
	})
	sentAttempted = true
	sentOutput := runManaged("send_message", map[string]any{
		"to":      config.mailbox,
		"subject": sentSubject,
		"body":    body,
	})
	var sent struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(sentOutput, &sent); err != nil ||
		!gmailValidSmokeID(sent.ID) {
		t.Fatal("send_message returned an invalid smoke-message identity")
	}
	sentMessageID = sent.ID
}

type gmailSmokeConfiguration struct {
	accessToken  string
	cleanupToken string
	mailbox      string
}

func parseGmailSmokeConfig(
	lookup smoketest.Lookup,
) (gmailSmokeConfiguration, bool, error) {
	values, configured, err := smoketest.Values(
		"Gmail",
		lookup,
		gmailSmokeAccessTokenEnv,
		gmailSmokeCleanupTokenEnv,
		gmailSmokeMailboxEnv,
		gmailSmokeAllowWritesEnv,
	)
	if !configured || err != nil {
		return gmailSmokeConfiguration{}, configured, err
	}
	if err := smoketest.Exactly(
		values,
		gmailSmokeAllowWritesEnv,
		"true",
	); err != nil {
		return gmailSmokeConfiguration{}, true, err
	}
	if !gmailValidSmokeMailbox(values[gmailSmokeMailboxEnv]) {
		return gmailSmokeConfiguration{}, true, fmt.Errorf(
			"%s must be a bare dedicated mailbox address",
			gmailSmokeMailboxEnv,
		)
	}
	// The janitor grant can delete mail; the Connector's own grant cannot.
	// Sharing one token would silently widen what the smoke run authorizes.
	if values[gmailSmokeAccessTokenEnv] == values[gmailSmokeCleanupTokenEnv] {
		return gmailSmokeConfiguration{}, true, fmt.Errorf(
			"%s and %s must use distinct least-privilege authorizations",
			gmailSmokeAccessTokenEnv,
			gmailSmokeCleanupTokenEnv,
		)
	}
	return gmailSmokeConfiguration{
		accessToken:  values[gmailSmokeAccessTokenEnv],
		cleanupToken: values[gmailSmokeCleanupTokenEnv],
		mailbox:      values[gmailSmokeMailboxEnv],
	}, true, nil
}

func gmailValidSmokeMailbox(value string) bool {
	if value == "" ||
		value != strings.TrimSpace(value) ||
		len(value) > 320 ||
		strings.ContainsAny(value, "\r\n") {
		return false
	}
	address, err := mail.ParseAddress(value)
	return err == nil &&
		address.Name == "" &&
		address.Address == value &&
		strings.Contains(value, "@")
}

// The shared harness covers the env gate itself; this covers Gmail's own three
// rules — the write gate, the bare mailbox, and the separate janitor grant.
func TestGmailSmokeConfigGate(t *testing.T) {
	t.Parallel()
	complete := map[string]string{
		gmailSmokeAccessTokenEnv:  "redacted-runtime-token",
		gmailSmokeCleanupTokenEnv: "redacted-cleanup-token",
		gmailSmokeMailboxEnv:      "connect-it-smoke@example.test",
		gmailSmokeAllowWritesEnv:  "true",
	}
	for name, value := range map[string]string{
		gmailSmokeAllowWritesEnv:  "TRUE",
		gmailSmokeMailboxEnv:      "Display <smoke@example.test>",
		gmailSmokeCleanupTokenEnv: complete[gmailSmokeAccessTokenEnv],
	} {
		values := maps.Clone(complete)
		values[name] = value
		if _, _, err := parseGmailSmokeConfig(
			smoketest.MapLookup(values),
		); err == nil {
			t.Errorf("%s accepted an unsafe value", name)
		}
	}
	config, configured, err := parseGmailSmokeConfig(
		smoketest.MapLookup(complete),
	)
	if !configured || err != nil ||
		config.mailbox != "connect-it-smoke@example.test" {
		t.Fatalf("complete Gmail smoke config = %+v err %v", config, err)
	}
}

func gmailSmokeCredentialInput(
	accessToken string,
	connectionID string,
) connector.CredentialValidationInput {
	return connector.CredentialValidationInput{
		ConnectorType: Definition.Type,
		AuthMethodKey: "oauth",
		AuthType:      connector.AuthOAuth2,
		ConnectionID:  connectionID,
		AccessToken:   accessToken,
		TokenType:     "Bearer",
	}
}

func gmailValidateSmokeCredential(
	t *testing.T,
	validator connector.CredentialValidator,
	accessToken string,
	mailbox string,
	connectionID string,
) {
	t.Helper()
	validation, err := validator(
		t.Context(),
		gmailSmokeCredentialInput(accessToken, connectionID),
	)
	if err != nil {
		t.Fatal("validate Gmail smoke credential")
	}
	if validation.Profile.AccountID != "" ||
		!strings.EqualFold(validation.Profile.DisplayName, mailbox) ||
		validation.ScopesKnown ||
		len(validation.GrantedScopes) != 0 {
		t.Fatal("Gmail validator returned an invalid mailbox or scope snapshot")
	}
}

func gmailSmokeCleanupClient(
	factory *providerkit.Factory,
) (*providerkit.Client, error) {
	return factory.NewStaticClient(providerkit.Policy{
		Provider:         string(Definition.Type),
		BaseURL:          managedBaseURL,
		AllowedOrigins:   []string{managedBaseURL},
		RedirectMode:     providerkit.RedirectDenyAll,
		NetworkMode:      providerkit.PublicOnly,
		RequestTimeout:   providerkit.DefaultRequestTimeout,
		MaxResponseBytes: 1 << 20,
		Retry:            providerkit.RetryPolicy{Disabled: true},
	})
}

type gmailSmokeHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type gmailSmokeMessage struct {
	ID      string `json:"id"`
	Payload struct {
		Headers []gmailSmokeHeader `json:"headers"`
	} `json:"payload"`
}

// gmailCleanupRealSmokeResource is the one guarded cleanup both writes use:
// resolve exactly one resource this harness created, then permanently delete
// only that one. A lookup that finds nothing is retried, because a just-created
// draft or message can take seconds to become visible and absence is therefore
// never proof that nothing was created.
func gmailCleanupRealSmokeResource(
	t *testing.T,
	client *providerkit.Client,
	cleanupToken string,
	resource string,
	subject string,
	attempted bool,
	wait smoketest.Wait,
	resourcePath func(string) string,
	find func(context.Context, providerkit.Authorizer) ([]string, error),
) {
	t.Helper()
	if !attempted {
		return
	}
	if wait == nil || client == nil || !gmailValidSmokeSubject(subject) {
		gmailReportSmokeCleanupStateFailure(
			t,
			resource,
			"invalid",
			connector.FailureConfigurationError,
		)
		return
	}
	authorizer, err := providerkit.Bearer(cleanupToken)
	if err != nil {
		gmailReportSmokeCleanupStateFailure(
			t,
			resource,
			"authorization",
			connector.FailureConfigurationError,
		)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	matches, err := smoketest.Resolve(
		ctx,
		gmailSmokeCleanupLookupAttempts,
		wait,
		func(ctx context.Context) ([]string, bool, error) {
			found, err := find(ctx, authorizer)
			if err != nil {
				return nil, false, err
			}
			return found, len(found) != 0, nil
		},
	)
	if err != nil {
		gmailReportSmokeCleanupFailure(t, resource, "lookup", err)
		return
	}
	if len(matches) != 1 {
		gmailReportSmokeCleanupStateFailure(
			t,
			resource,
			"multiple",
			connector.FailureInvalidResponse,
		)
		return
	}

	resourceID := matches[0]
	failure := smoketest.Retry(
		ctx,
		gmailSmokeCleanupDeleteAttempts,
		wait,
		func(ctx context.Context) (bool, bool, *connector.ToolFailure) {
			response, err := client.Do(ctx, providerkit.Request{
				Method:     http.MethodDelete,
				URL:        resourcePath(resourceID),
				Authorizer: authorizer,
				Labels:     gmailSmokeCleanupLabels(),
			})
			if err != nil {
				// The janitor already removed it, or a previous attempt's
				// response was lost after the delete committed.
				if gmailSmokeNotFound(err) {
					return true, false, nil
				}
				failure := providerkit.AsToolFailure(err)
				return false, smoketest.Retryable(failure), failure
			}
			if response == nil ||
				(response.StatusCode != http.StatusOK &&
					response.StatusCode != http.StatusNoContent) ||
				len(response.Body) != 0 {
				return false, false, connector.Failure(
					connector.FailureInvalidResponse,
					connector.DefaultFailureMessage(
						connector.FailureInvalidResponse,
					),
					0,
					0,
				)
			}
			return true, false, nil
		},
	)
	if failure != nil {
		gmailReportSmokeCleanupStateFailure(
			t,
			resource,
			resourceID,
			failure.Code,
		)
	}
}

// wait is the backoff between two bounded lookup attempts; the real harness
// passes smoketest.WaitSecond and tests inject their own.
func gmailCleanupRealSmokeDraft(
	t *testing.T,
	client *providerkit.Client,
	config gmailSmokeConfiguration,
	subject string,
	attempted bool,
	wait smoketest.Wait,
) {
	t.Helper()
	gmailCleanupRealSmokeResource(
		t,
		client,
		config.cleanupToken,
		"draft",
		subject,
		attempted,
		wait,
		func(draftID string) string {
			return "/gmail/v1/users/me/drafts/" +
				providerkit.PathSegment(draftID)
		},
		func(
			ctx context.Context,
			authorizer providerkit.Authorizer,
		) ([]string, error) {
			return gmailFindSmokeDrafts(
				ctx,
				client,
				authorizer,
				subject,
				config.mailbox,
			)
		},
	)
}

func gmailCleanupRealSmokeMessage(
	t *testing.T,
	client *providerkit.Client,
	config gmailSmokeConfiguration,
	subject string,
	messageID string,
	attempted bool,
	wait smoketest.Wait,
) {
	t.Helper()
	if attempted && messageID != "" && !gmailValidSmokeID(messageID) {
		gmailReportSmokeCleanupStateFailure(
			t,
			"message",
			"invalid",
			connector.FailureConfigurationError,
		)
		return
	}
	gmailCleanupRealSmokeResource(
		t,
		client,
		config.cleanupToken,
		"message",
		subject,
		attempted,
		wait,
		func(id string) string {
			return "/gmail/v1/users/me/messages/" +
				providerkit.PathSegment(id)
		},
		func(
			ctx context.Context,
			authorizer providerkit.Authorizer,
		) ([]string, error) {
			// send_message returned an ID, so the exact identity is verifiable
			// without a search. Anything else is a resource we did not create.
			if messageID != "" {
				matched, err := gmailSmokeMessageByIDMatches(
					ctx,
					client,
					authorizer,
					messageID,
					subject,
					config.mailbox,
				)
				if err != nil {
					return nil, err
				}
				if !matched {
					return nil, errors.New(
						"Gmail smoke message identity did not match",
					)
				}
				return []string{messageID}, nil
			}
			return gmailFindSmokeMessages(
				ctx,
				client,
				authorizer,
				subject,
				config.mailbox,
			)
		},
	)
}

func gmailSmokeCleanupLabels() providerkit.RequestLabels {
	return providerkit.RequestLabels{
		ConnectorType:   string(Definition.Type),
		Operation:       "real_smoke_cleanup",
		AuthorizationID: gmailSmokeConnection + "-cleanup",
	}
}

func gmailFindSmokeDrafts(
	ctx context.Context,
	client *providerkit.Client,
	authorizer providerkit.Authorizer,
	subject string,
	mailbox string,
) ([]string, error) {
	response, err := client.Do(ctx, providerkit.Request{
		Method: http.MethodGet,
		URL:    "/gmail/v1/users/me/drafts",
		Query: url.Values{
			"q":          {`subject:"` + subject + `"`},
			"maxResults": {"50"},
		},
		Authorizer: authorizer,
		Labels:     gmailSmokeCleanupLabels(),
	})
	if err != nil {
		return nil, err
	}
	var listing struct {
		Drafts []struct {
			ID string `json:"id"`
		} `json:"drafts"`
	}
	if err := response.DecodeJSON(&listing); err != nil {
		return nil, err
	}
	if len(listing.Drafts) > 50 {
		return nil, errors.New("invalid Gmail smoke draft listing")
	}

	matches := make([]string, 0, 1)
	for _, candidate := range listing.Drafts {
		if !gmailValidSmokeID(candidate.ID) {
			return nil, errors.New("invalid Gmail smoke draft ID")
		}
		response, err := client.Do(ctx, providerkit.Request{
			Method: http.MethodGet,
			URL: gmailSmokeMetadataURL(
				"/gmail/v1/users/me/drafts/" +
					providerkit.PathSegment(candidate.ID),
			),
			Authorizer: authorizer,
			Labels:     gmailSmokeCleanupLabels(),
		})
		if err != nil {
			if gmailSmokeNotFound(err) {
				continue
			}
			return nil, err
		}
		var draft struct {
			ID      string            `json:"id"`
			Message gmailSmokeMessage `json:"message"`
		}
		if err := response.DecodeJSON(&draft); err != nil {
			return nil, err
		}
		if draft.ID != candidate.ID {
			return nil, errors.New("invalid Gmail smoke draft identity")
		}
		if gmailSmokeMessageMatches(draft.Message, subject, mailbox) {
			matches = append(matches, draft.ID)
		}
	}
	return matches, nil
}

func gmailFindSmokeMessages(
	ctx context.Context,
	client *providerkit.Client,
	authorizer providerkit.Authorizer,
	subject string,
	mailbox string,
) ([]string, error) {
	response, err := client.Do(ctx, providerkit.Request{
		Method: http.MethodGet,
		URL:    "/gmail/v1/users/me/messages",
		Query: url.Values{
			"q":          {`in:sent subject:"` + subject + `"`},
			"maxResults": {"50"},
		},
		Authorizer: authorizer,
		Labels:     gmailSmokeCleanupLabels(),
	})
	if err != nil {
		return nil, err
	}
	var listing struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := response.DecodeJSON(&listing); err != nil {
		return nil, err
	}
	if len(listing.Messages) > 50 {
		return nil, errors.New("invalid Gmail smoke message listing")
	}

	matches := make([]string, 0, 1)
	for _, candidate := range listing.Messages {
		if !gmailValidSmokeID(candidate.ID) {
			return nil, errors.New("invalid Gmail smoke message ID")
		}
		match, err := gmailSmokeMessageByIDMatches(
			ctx,
			client,
			authorizer,
			candidate.ID,
			subject,
			mailbox,
		)
		if err != nil {
			if gmailSmokeNotFound(err) {
				continue
			}
			return nil, err
		}
		if match {
			matches = append(matches, candidate.ID)
		}
	}
	return matches, nil
}

func gmailSmokeMessageByIDMatches(
	ctx context.Context,
	client *providerkit.Client,
	authorizer providerkit.Authorizer,
	messageID string,
	subject string,
	mailbox string,
) (bool, error) {
	response, err := client.Do(ctx, providerkit.Request{
		Method: http.MethodGet,
		URL: gmailSmokeMetadataURL(
			"/gmail/v1/users/me/messages/" +
				providerkit.PathSegment(messageID),
		),
		Authorizer: authorizer,
		Labels:     gmailSmokeCleanupLabels(),
	})
	if err != nil {
		return false, err
	}
	var message gmailSmokeMessage
	if err := response.DecodeJSON(&message); err != nil {
		return false, err
	}
	if message.ID != messageID {
		return false, errors.New("invalid Gmail smoke message identity")
	}
	return gmailSmokeMessageMatches(message, subject, mailbox), nil
}

func gmailSmokeMetadataURL(requestPath string) string {
	// providerkit's Request.Query intentionally accepts scalar values only.
	// Gmail's metadataHeaders parameter is repeated, so keep these two
	// non-secret, code-fixed values in the reviewed relative request target.
	return requestPath +
		"?format=metadata&metadataHeaders=Subject&metadataHeaders=To"
}

func gmailSmokeMessageMatches(
	message gmailSmokeMessage,
	subject string,
	mailbox string,
) bool {
	if !gmailValidSmokeID(message.ID) {
		return false
	}
	var subjectValue, recipientValue string
	var subjectCount, recipientCount int
	for _, header := range message.Payload.Headers {
		switch {
		case strings.EqualFold(header.Name, "Subject"):
			subjectValue = header.Value
			subjectCount++
		case strings.EqualFold(header.Name, "To"):
			recipientValue = header.Value
			recipientCount++
		}
	}
	if subjectCount != 1 ||
		recipientCount != 1 ||
		subjectValue != subject {
		return false
	}
	recipients, err := mail.ParseAddressList(recipientValue)
	if err != nil || len(recipients) != 1 {
		return false
	}
	return strings.EqualFold(recipients[0].Address, mailbox)
}

func gmailValidSmokeSubject(subject string) bool {
	return strings.HasPrefix(subject, gmailSmokeSubjectPrefix) &&
		len(subject) <= 200 &&
		!strings.ContainsAny(subject, "\"\r\n")
}

func gmailValidSmokeID(value string) bool {
	return gmailSmokeIDPattern.MatchString(value)
}

func gmailSmokeNotFound(err error) bool {
	failure := providerkit.AsToolFailure(err)
	return failure != nil && failure.UpstreamStatus == http.StatusNotFound
}

func gmailReportSmokeCleanupFailure(
	t *testing.T,
	resource string,
	resourceID string,
	err error,
) {
	t.Helper()
	failure := gmailSmokeCleanupFailure(err)
	t.Errorf(
		"cleanup Gmail smoke %s %s failed: code=%s upstream_status=%d",
		resource,
		resourceID,
		failure.Code,
		failure.UpstreamStatus,
	)
}

func gmailSmokeCleanupFailure(err error) connector.ToolFailure {
	return connector.NormalizeToolFailure(providerkit.AsToolFailure(err))
}

func gmailReportSmokeCleanupStateFailure(
	t *testing.T,
	resource string,
	resourceID string,
	code connector.FailureCode,
) {
	t.Helper()
	t.Errorf(
		"cleanup Gmail smoke %s %s failed: code=%s upstream_status=0",
		resource,
		resourceID,
		code,
	)
}

// Both writes share one cleanup shape: a first listing that has not caught up
// is uncertainty, not proof that nothing was created, so the lookup is retried
// and then exactly one verified resource is deleted. The two rows differ in the
// collection, its search query and the identity payload.
func TestGmailSmokeCleanupRetriesUncertainLookupAndDeletesExactMatch(
	t *testing.T,
) {
	const mailbox = "connect-it-smoke@example.test"
	tests := []struct {
		name       string
		collection string
		resourceID string
		query      string
		identity   func(subject string) any
	}{
		{
			name:       "draft",
			collection: "drafts",
			resourceID: "draft_fixture",
			query:      `subject:"%s"`,
			identity: func(subject string) any {
				return map[string]any{
					"id": "draft_fixture",
					"message": gmailSmokeMessageFixture(
						"message_fixture",
						subject,
						mailbox,
					),
				}
			},
		},
		{
			name:       "sent message",
			collection: "messages",
			resourceID: "message_fixture",
			query:      `in:sent subject:"%s"`,
			identity: func(subject string) any {
				return gmailSmokeMessageFixture(
					"message_fixture",
					subject,
					mailbox,
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			subject := gmailSmokeSubjectPrefix + "fixture " + test.name
			collectionPath := "/gmail/v1/users/me/" + test.collection
			resourcePath := collectionPath + "/" + test.resourceID
			listCalls := 0
			deleteCalls := 0
			server := testkit.NewServerWithOptions(
				t,
				http.HandlerFunc(func(
					writer http.ResponseWriter,
					request *http.Request,
				) {
					testkit.AssertHeader(
						t,
						request,
						"Authorization",
						"Bearer redacted-cleanup-token",
					)
					writer.Header().Set("Content-Type", "application/json")
					switch {
					case request.Method == http.MethodGet &&
						request.URL.Path == collectionPath:
						listCalls++
						if request.URL.Query().Get("q") !=
							fmt.Sprintf(test.query, subject) ||
							request.URL.Query().Get("maxResults") != "50" {
							t.Error("Gmail cleanup lookup query changed")
						}
						if listCalls == 1 {
							_, _ = fmt.Fprintf(
								writer,
								`{%q:[]}`,
								test.collection,
							)
							return
						}
						_, _ = fmt.Fprintf(
							writer,
							`{%q:[{"id":%q}]}`,
							test.collection,
							test.resourceID,
						)
					case request.Method == http.MethodGet &&
						request.URL.Path == resourcePath:
						gmailAssertSmokeMetadataQuery(t, request)
						_ = json.NewEncoder(writer).Encode(
							test.identity(subject),
						)
					case request.Method == http.MethodDelete &&
						request.URL.Path == resourcePath:
						deleteCalls++
						writer.WriteHeader(http.StatusNoContent)
					default:
						t.Error("Gmail cleanup request mapping changed")
						writer.WriteHeader(http.StatusNotFound)
					}
				}),
				testkit.ServerOptions{Provider: string(Definition.Type)},
			)
			config := gmailSmokeConfiguration{
				cleanupToken: "redacted-cleanup-token",
				mailbox:      mailbox,
			}
			noWait := func(context.Context) bool { return true }
			if test.collection == "drafts" {
				gmailCleanupRealSmokeDraft(
					t, server.Client, config, subject, true, noWait,
				)
			} else {
				// The empty message ID is the lost-send-response case: cleanup
				// must then find the message by its generated Subject.
				gmailCleanupRealSmokeMessage(
					t, server.Client, config, subject, "", true, noWait,
				)
			}
			if listCalls != 2 || deleteCalls != 1 {
				t.Fatalf(
					"Gmail uncertain cleanup calls = list:%d delete:%d",
					listCalls,
					deleteCalls,
				)
			}
		})
	}
}

func TestGmailSmokeMessageIdentityRequiresSoleExactRecipient(t *testing.T) {
	t.Parallel()
	const (
		subject = gmailSmokeSubjectPrefix + "fixture message"
		mailbox = "connect-it-smoke@example.test"
	)
	message := gmailSmokeMessage{ID: "message_fixture"}
	message.Payload.Headers = []gmailSmokeHeader{
		{Name: "Subject", Value: subject},
		{Name: "To", Value: mailbox + ", other@example.test"},
	}
	if gmailSmokeMessageMatches(message, subject, mailbox) {
		t.Fatal("Gmail cleanup accepted a multi-recipient message identity")
	}
	message.Payload.Headers[1].Value = mailbox
	if !gmailSmokeMessageMatches(message, subject, mailbox) {
		t.Fatal("Gmail cleanup rejected the exact sole-recipient identity")
	}
}

func TestGmailSmokeCleanupGenericErrorIsRedacted(t *testing.T) {
	t.Parallel()
	const providerSecret = "provider-cleanup-secret"
	failure := gmailSmokeCleanupFailure(errors.New(providerSecret))
	if failure.Code != connector.FailureInternalError ||
		strings.Contains(failure.Message, providerSecret) {
		t.Fatal("Gmail cleanup retained a generic Provider error")
	}
}

func gmailAssertSmokeMetadataQuery(t *testing.T, request *http.Request) {
	t.Helper()
	if request.URL.Query().Get("format") != "metadata" ||
		len(request.URL.Query()["metadataHeaders"]) != 2 {
		t.Error("Gmail cleanup metadata query changed")
	}
}

func gmailSmokeMessageFixture(
	messageID string,
	subject string,
	mailbox string,
) map[string]any {
	return map[string]any{
		"id": messageID,
		"payload": map[string]any{
			"headers": []map[string]string{
				{"name": "Subject", "value": subject},
				{"name": "To", "value": mailbox},
			},
		},
	}
}
