package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

type echoInput struct {
	Message string `json:"message"`
}

type echoOutput struct {
	Echoed string `json:"echoed"`
}

func newMCPHandler(
	t *testing.T,
	wantAuth string,
) http.Handler {
	t.Helper()
	server := mcp.NewServer(
		&mcp.Implementation{Name: "fake-upstream", Version: "0.0.1"},
		nil,
	)
	mcp.AddTool(
		server,
		&mcp.Tool{Name: "echo", Description: "echo message"},
		func(
			context.Context,
			*mcp.CallToolRequest,
			echoInput,
		) (*mcp.CallToolResult, echoOutput, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "echo:hi"},
				},
			}, echoOutput{Echoed: "hi"}, nil
		},
	)
	mcp.AddTool(
		server,
		&mcp.Tool{Name: "always_fail", Description: "legacy IsError"},
		func(
			context.Context,
			*mcp.CallToolRequest,
			struct{},
		) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{
					&mcp.TextContent{
						Text: "poison-remote-error-text",
					},
				},
				StructuredContent: map[string]any{
					"secret": "poison-remote-error-structured",
				},
			}, nil, nil
		},
	)
	upstream := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			DisableLocalhostProtection: true,
		},
	)
	return http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if wantAuth != "" &&
			request.Header.Get("Authorization") != wantAuth {
			http.Error(
				writer,
				"raw-provider-body-must-not-escape",
				http.StatusUnauthorized,
			)
			return
		}
		upstream.ServeHTTP(writer, request)
	})
}

type selfHostedHarness struct {
	server   *httptest.Server
	endpoint string
	target   string
}

func newSelfHostedHarness(
	t *testing.T,
	handler http.Handler,
) selfHostedHarness {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return selfHostedHarness{
		server:   server,
		endpoint: "http://" + net.JoinHostPort("provider.test", port) + "/mcp",
		target:   server.Listener.Addr().String(),
	}
}

func selfHostedServer(timeout time.Duration) connector.RemoteMCPServer {
	return connector.RemoteMCPServer{
		Key: "self_hosted",
		Endpoint: connector.Endpoint{
			Source:         connector.EndpointConfigField,
			ConfigFieldKey: "mcp_url",
		},
		Provenance: connector.Provenance{
			Kind: connector.ProvenanceSelfHosted,
		},
		RequestTimeout: timeout,
	}
}

func fixedServer(endpoint, hostname string) connector.RemoteMCPServer {
	return connector.RemoteMCPServer{
		Key:      "official",
		Endpoint: connector.Endpoint{Source: connector.EndpointFixed, URL: endpoint},
		Provenance: connector.Provenance{
			Kind:             connector.ProvenanceOfficial,
			AllowedHostnames: []string{hostname},
		},
		RequestTimeout: time.Second,
	}
}

func newHarnessClient(
	t *testing.T,
	target string,
	resolver providerkit.Resolver,
	private bool,
	insecure bool,
) *Client {
	t.Helper()
	options := []providerkit.FactoryOption{
		providerkit.WithResolver(resolver),
		providerkit.WithDialContext(testkit.DialTarget(target)),
		providerkit.WithPrivateProviderNetwork(private),
		providerkit.WithInsecureProviderHTTP(insecure),
	}
	client, err := New(providerkit.NewFactory(options...))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func callRequest(
	harness selfHostedHarness,
	server connector.RemoteMCPServer,
) CallRequest {
	return CallRequest{
		ConnectorType:     "test_mcp",
		ConnectionID:      uuid.New(),
		ToolID:            "echo",
		Server:            server,
		Endpoint:          harness.endpoint,
		BearerToken:       "secret-token",
		AllowInsecureHTTP: "true",
		RemoteToolName:    "echo",
		Arguments:         json.RawMessage(`{"message":"hi"}`),
	}
}

func TestCallToolAndListToolsUseOfficialSDKThroughGuardedClient(
	t *testing.T,
) {
	harness := newSelfHostedHarness(
		t,
		newMCPHandler(t, "Bearer secret-token"),
	)
	client := newHarnessClient(
		t,
		harness.target,
		testkit.Resolver{},
		false,
		true,
	)
	server := selfHostedServer(2 * time.Second)

	result, err := client.CallTool(t.Context(), callRequest(harness, server))
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed() || result.Text != "echo:hi" {
		t.Fatalf("result = %+v", result)
	}
	var structured map[string]any
	if err := json.Unmarshal(result.Structured, &structured); err != nil {
		t.Fatalf("structured output = %q: %v", result.Structured, err)
	}
	if structured["echoed"] != "hi" {
		t.Fatalf("structured output = %s", result.Structured)
	}

	names, err := client.ListTools(t.Context(), ListRequest{
		ConnectorType:     "test_mcp",
		Operation:         OperationVerify,
		AuthorizationID:   "mcp-list-test-authorization",
		Server:            server,
		Endpoint:          harness.endpoint,
		BearerToken:       "secret-token",
		AllowInsecureHTTP: "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"always_fail", "echo"}) {
		t.Fatalf("names = %v", names)
	}
}

func TestCallToolNormalizesIsErrorAndDropsProviderPayload(t *testing.T) {
	harness := newSelfHostedHarness(t, newMCPHandler(t, ""))
	client := newHarnessClient(
		t,
		harness.target,
		testkit.Resolver{},
		false,
		true,
	)
	request := callRequest(harness, selfHostedServer(time.Second))
	request.BearerToken = ""
	request.ToolID = "always_fail"
	request.RemoteToolName = "always_fail"
	request.Arguments = json.RawMessage(`{}`)

	result, err := client.CallTool(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure == nil ||
		result.Failure.Code != connector.FailureProviderError ||
		result.Text != "" ||
		len(result.Structured) != 0 {
		t.Fatalf("normalized result = %+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, poison := range []string{
		"poison-remote-error-text",
		"poison-remote-error-structured",
	} {
		if strings.Contains(string(encoded), poison) {
			t.Fatalf("normalized result leaked %q: %s", poison, encoded)
		}
	}
}

func TestPolicyDerivationCannotBeDowngradedByCaller(t *testing.T) {
	factory := providerkit.NewFactory(
		providerkit.WithPrivateProviderNetwork(true),
		providerkit.WithInsecureProviderHTTP(true),
	)
	client, err := New(factory)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("fixed policy is exact public-only", func(t *testing.T) {
		server := fixedServer(
			"https://api.example.test/mcp",
			"api.example.test",
		)
		policyClient, err := client.policyClient(
			"github",
			server,
			server.Endpoint.URL,
			"false",
		)
		if err != nil {
			t.Fatal(err)
		}
		policy := policyClient.Policy()
		if policy.NetworkMode != providerkit.PublicOnly ||
			policy.AllowPlainHTTP ||
			policy.BaseURL != "https://api.example.test:443/mcp" ||
			!slices.Equal(
				policy.AllowedOrigins,
				[]string{"https://api.example.test:443"},
			) ||
			len(policy.AllowedRedirectOrigins) != 0 {
			t.Fatalf("fixed policy = %+v", policy)
		}
	})

	tests := []struct {
		name      string
		server    connector.RemoteMCPServer
		endpoint  string
		allowHTTP string
	}{
		{
			name: "fixed HTTP stays forbidden",
			server: fixedServer(
				"http://api.example.test/mcp",
				"api.example.test",
			),
			endpoint: "http://api.example.test/mcp",
		},
		{
			name: "fixed private literal stays forbidden",
			server: fixedServer(
				"https://127.0.0.1/mcp",
				"127.0.0.1",
			),
			endpoint: "https://127.0.0.1/mcp",
		},
		{
			name: "fixed endpoint cannot be replaced",
			server: fixedServer(
				"https://api.example.test/mcp",
				"api.example.test",
			),
			endpoint: "https://attacker.example/mcp",
		},
		{
			name: "fixed endpoint cannot opt in HTTP",
			server: fixedServer(
				"https://api.example.test/mcp",
				"api.example.test",
			),
			endpoint:  "https://api.example.test/mcp",
			allowHTTP: "true",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.policyClient(
				"github",
				test.server,
				test.endpoint,
				test.allowHTTP,
			); err == nil {
				t.Fatal("unsafe fixed policy was accepted")
			}
		})
	}
}

func TestSelfHostedHTTPRequiresConnectorAndDeploymentGates(t *testing.T) {
	harness := newSelfHostedHarness(t, newMCPHandler(t, ""))
	server := selfHostedServer(time.Second)

	tests := []struct {
		name       string
		configGate string
		deployGate bool
		wantOK     bool
	}{
		{
			name:       "both disabled",
			configGate: "false",
		},
		{
			name:       "deployment only",
			configGate: "false",
			deployGate: true,
		},
		{
			name:       "connector only",
			configGate: "true",
		},
		{
			name:       "both enabled",
			configGate: "true",
			deployGate: true,
			wantOK:     true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newHarnessClient(
				t,
				harness.target,
				testkit.Resolver{},
				false,
				test.deployGate,
			)
			request := callRequest(harness, server)
			request.BearerToken = ""
			request.AllowInsecureHTTP = test.configGate
			_, err := client.CallTool(t.Context(), request)
			if (err == nil) != test.wantOK {
				t.Fatalf("CallTool() error = %v, wantOK=%v", err, test.wantOK)
			}
		})
	}
}

func TestSelfHostedPrivateNetworkRequiresDeploymentGateAndPermanentBlocksRemain(
	t *testing.T,
) {
	harness := newSelfHostedHarness(t, newMCPHandler(t, ""))
	server := selfHostedServer(time.Second)
	request := callRequest(harness, server)
	request.BearerToken = ""

	privateResolver := testkit.Resolver{
		Addresses: []netip.Addr{netip.MustParseAddr("10.20.30.40")},
	}
	denied := newHarnessClient(
		t,
		harness.target,
		privateResolver,
		false,
		true,
	)
	if _, err := denied.CallTool(t.Context(), request); failureCode(err) !=
		connector.FailurePolicyDenied {
		t.Fatalf("private network without deployment gate = %v", err)
	}

	allowed := newHarnessClient(
		t,
		harness.target,
		privateResolver,
		true,
		true,
	)
	if _, err := allowed.CallTool(t.Context(), request); err != nil {
		t.Fatalf("private network with both gates: %v", err)
	}

	for _, rawIP := range []string{
		"127.0.0.1",
		"169.254.10.20",
		"169.254.169.254",
		"224.0.0.1",
		"0.0.0.0",
	} {
		t.Run(rawIP, func(t *testing.T) {
			client := newHarnessClient(
				t,
				harness.target,
				testkit.Resolver{
					Addresses: []netip.Addr{netip.MustParseAddr(rawIP)},
				},
				true,
				true,
			)
			if _, err := client.CallTool(
				t.Context(),
				request,
			); failureCode(err) != connector.FailurePolicyDenied {
				t.Fatalf("permanently blocked IP %s = %v", rawIP, err)
			}
		})
	}
}

func TestSameOriginRedirectKeepsBearerAndCrossOriginNeverReceivesIt(
	t *testing.T,
) {
	t.Run("same origin", func(t *testing.T) {
		upstream := newMCPHandler(t, "Bearer redirect-secret")
		var redirected atomic.Int64
		harness := newSelfHostedHarness(
			t,
			http.HandlerFunc(func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				switch request.URL.Path {
				case "/mcp":
					if request.Header.Get("Authorization") !=
						"Bearer redirect-secret" {
						t.Errorf("initial request lost bearer")
					}
					http.Redirect(
						writer,
						request,
						"/actual",
						http.StatusTemporaryRedirect,
					)
				case "/actual":
					redirected.Add(1)
					upstream.ServeHTTP(writer, request)
				default:
					http.NotFound(writer, request)
				}
			}),
		)
		client := newHarnessClient(
			t,
			harness.target,
			testkit.Resolver{},
			false,
			true,
		)
		request := callRequest(harness, selfHostedServer(time.Second))
		request.BearerToken = "redirect-secret"
		if _, err := client.CallTool(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		if redirected.Load() == 0 {
			t.Fatal("same-origin redirect target was not reached")
		}
	})

	t.Run("cross origin", func(t *testing.T) {
		var targetCalls atomic.Int64
		var targetAuth atomic.Value
		harness := newSelfHostedHarness(
			t,
			http.HandlerFunc(func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				if strings.HasPrefix(request.Host, "redirect.test") {
					targetCalls.Add(1)
					targetAuth.Store(request.Header.Get("Authorization"))
					http.Error(writer, "unexpected", http.StatusBadRequest)
					return
				}
				_, port, err := net.SplitHostPort(request.Host)
				if err != nil {
					http.Error(writer, "bad host", http.StatusBadRequest)
					return
				}
				writer.Header().Set(
					"Location",
					"http://"+net.JoinHostPort("redirect.test", port)+
						"/target",
				)
				writer.WriteHeader(http.StatusTemporaryRedirect)
			}),
		)
		client := newHarnessClient(
			t,
			harness.target,
			testkit.Resolver{},
			false,
			true,
		)
		_, err := client.CallTool(
			t.Context(),
			callRequest(harness, selfHostedServer(time.Second)),
		)
		if failureCode(err) != connector.FailurePolicyDenied {
			t.Fatalf("cross-origin redirect failure = %v", err)
		}
		if targetCalls.Load() != 0 {
			t.Fatalf(
				"cross-origin target received request/auth=%v",
				targetAuth.Load(),
			)
		}
	})
}

func TestBoundedBodyTimeoutAndSafeTypedErrors(t *testing.T) {
	t.Run("bounded response", func(t *testing.T) {
		harness := newSelfHostedHarness(
			t,
			http.HandlerFunc(func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				writer.Header().Set(
					"Content-Type",
					"application/json",
				)
				writer.Header().Set(
					"Content-Length",
					strconv.FormatInt(
						providerkit.DefaultMaxResponseBytes+1,
						10,
					),
				)
				writer.WriteHeader(http.StatusOK)
			}),
		)
		client := newHarnessClient(
			t,
			harness.target,
			testkit.Resolver{},
			false,
			true,
		)
		request := callRequest(harness, selfHostedServer(time.Second))
		request.BearerToken = ""
		_, err := client.CallTool(t.Context(), request)
		if failureCode(err) != connector.FailureResponseTooLarge {
			t.Fatalf("oversize failure = %v", err)
		}
	})

	t.Run("total timeout", func(t *testing.T) {
		harness := newSelfHostedHarness(
			t,
			http.HandlerFunc(func(
				_ http.ResponseWriter,
				request *http.Request,
			) {
				timer := time.NewTimer(250 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-request.Context().Done():
				case <-timer.C:
				}
			}),
		)
		client := newHarnessClient(
			t,
			harness.target,
			testkit.Resolver{},
			false,
			true,
		)
		request := callRequest(
			harness,
			selfHostedServer(50*time.Millisecond),
		)
		request.BearerToken = ""
		_, err := client.CallTool(t.Context(), request)
		if failureCode(err) != connector.FailureTimeout {
			t.Fatalf("timeout failure = %v", err)
		}
	})

	t.Run("401 is explicit only with credential and error is redacted", func(t *testing.T) {
		const (
			token  = "poison-bearer-token"
			body   = "poison-provider-body"
			secret = "poison-path"
		)
		harness := newSelfHostedHarness(
			t,
			http.HandlerFunc(func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				http.Error(writer, body, http.StatusUnauthorized)
			}),
		)
		harness.endpoint = strings.Replace(
			harness.endpoint,
			"/mcp",
			"/"+secret,
			1,
		)
		client := newHarnessClient(
			t,
			harness.target,
			testkit.Resolver{},
			false,
			true,
		)
		request := callRequest(harness, selfHostedServer(time.Second))
		request.BearerToken = token
		_, err := client.CallTool(t.Context(), request)
		var carrier interface {
			ToolFailure() *connector.ToolFailure
		}
		if !errors.As(err, &carrier) {
			t.Fatalf("error lacks ToolFailure carrier: %T %v", err, err)
		}
		failure := carrier.ToolFailure()
		if failure.Code != connector.FailureAuthorizationFailed ||
			!failure.IndicatesCredentialInvalid() {
			t.Fatalf("401 failure = %+v", failure)
		}
		for _, poison := range []string{token, body, secret, harness.endpoint} {
			if strings.Contains(err.Error(), poison) {
				t.Fatalf("error leaked %q: %v", poison, err)
			}
		}

		request.BearerToken = ""
		_, err = client.CallTool(t.Context(), request)
		if got := toolFailure(err); got == nil ||
			got.IndicatesCredentialInvalid() {
			t.Fatalf("credential-free 401 became invalid signal: %+v", got)
		}
	})
}

func TestConcurrencyQueueIsBoundedAndContextAware(t *testing.T) {
	client, err := New(providerkit.NewFactory())
	if err != nil {
		t.Fatal(err)
	}
	client.slots = make(chan struct{}, 1)
	client.slots <- struct{}{}
	defer func() { <-client.slots }()

	request := CallRequest{
		ConnectorType: "github",
		ConnectionID:  uuid.New(),
		ToolID:        "echo",
		Server: fixedServer(
			"https://api.example.test/mcp",
			"api.example.test",
		),
		Endpoint:       "https://api.example.test/mcp",
		RemoteToolName: "echo",
	}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	if _, err := client.CallTool(
		ctx,
		request,
	); failureCode(err) != connector.FailureCanceled {
		t.Fatalf("queued call failure = %v", err)
	}
}

func TestCheckEndpointIsStrictAndDoesNotEchoRawValue(t *testing.T) {
	tests := []struct {
		endpoint      string
		allowInsecure bool
		wantErr       bool
	}{
		{"https://mcp.example.test/mcp", false, false},
		{"http://mcp.example.test/mcp", false, true},
		{"http://mcp.example.test/mcp", true, false},
		{"ftp://mcp.example.test/mcp", true, true},
		{"https://mcp.example.test/mcp?secret=poison", false, true},
		{"not a url poison", false, true},
	}
	for _, test := range tests {
		err := CheckEndpoint(test.endpoint, test.allowInsecure)
		if (err != nil) != test.wantErr {
			t.Errorf(
				"CheckEndpoint(%q, %v) = %v, wantErr=%v",
				test.endpoint,
				test.allowInsecure,
				err,
				test.wantErr,
			)
		}
		if err != nil &&
			(strings.Contains(err.Error(), "poison") ||
				strings.Contains(err.Error(), test.endpoint)) {
			t.Fatalf("endpoint error leaked raw value: %v", err)
		}
	}
}

func failureCode(err error) connector.FailureCode {
	failure := toolFailure(err)
	if failure == nil {
		return ""
	}
	return failure.Code
}

func toolFailure(err error) *connector.ToolFailure {
	if err == nil {
		return nil
	}
	var carrier interface {
		ToolFailure() *connector.ToolFailure
	}
	if !errors.As(err, &carrier) {
		return nil
	}
	return carrier.ToolFailure()
}
