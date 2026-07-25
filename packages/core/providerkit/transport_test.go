package providerkit

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

type resolverStub struct {
	addresses []netip.Addr
	err       error
	calls     atomic.Int64
}

func (resolver *resolverStub) LookupNetIP(
	_ context.Context,
	network string,
	_ string,
) ([]netip.Addr, error) {
	if network != "ip" {
		return nil, errors.New("unexpected resolver network")
	}
	resolver.calls.Add(1)
	return append([]netip.Addr(nil), resolver.addresses...), resolver.err
}

func TestGuardedTransportResolveValidatesCompleteDNSAnswer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		addresses    []netip.Addr
		allowPrivate bool
		wantErr      bool
		wantReason   IPReason
	}{
		{
			name:      "public",
			addresses: testAddresses("8.8.8.8", "2606:4700:4700::1111"),
		},
		{
			name:       "private public-only",
			addresses:  testAddresses("10.0.0.8"),
			wantErr:    true,
			wantReason: IPReasonPrivate,
		},
		{
			name:         "private explicit self-hosted",
			addresses:    testAddresses("10.0.0.8"),
			allowPrivate: true,
		},
		{
			name:         "metadata remains blocked with private opt-in",
			addresses:    testAddresses("169.254.169.254"),
			allowPrivate: true,
			wantErr:      true,
			wantReason:   IPReasonMetadata,
		},
		{
			name:       "mixed public and private is rejected",
			addresses:  testAddresses("8.8.8.8", "192.168.1.8"),
			wantErr:    true,
			wantReason: IPReasonPrivate,
		},
		{
			name:         "mixed public and permanently blocked is rejected",
			addresses:    testAddresses("8.8.8.8", "127.0.0.1"),
			allowPrivate: true,
			wantErr:      true,
			wantReason:   IPReasonLoopback,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			resolver := &resolverStub{addresses: test.addresses}
			transport := &guardedTransport{
				resolver:     resolver,
				allowPrivate: test.allowPrivate,
			}
			got, err := transport.resolve(context.Background(), "provider.test")
			if !test.wantErr {
				if err != nil {
					t.Fatalf("resolve() error = %v", err)
				}
				if len(got) != len(test.addresses) {
					t.Fatalf("resolve() returned %d addresses, want %d", len(got), len(test.addresses))
				}
			} else {
				if err == nil {
					t.Fatal("resolve() error = nil")
				}
				var rejected *ResolvedIPError
				if !errors.As(err, &rejected) {
					t.Fatalf("resolve() error type = %T, want *ResolvedIPError", err)
				}
				if rejected.Reason != test.wantReason {
					t.Fatalf("resolve() reason = %q, want %q", rejected.Reason, test.wantReason)
				}
			}
			if gotCalls := resolver.calls.Load(); gotCalls != 1 {
				t.Fatalf("resolver calls = %d, want 1", gotCalls)
			}
		})
	}
}

func TestGuardedTransportPinsDNSAndDialsLiteralAddress(t *testing.T) {
	t.Parallel()

	const responseBody = "pinned"
	serverConnection, clientConnection := net.Pipe()
	t.Cleanup(func() {
		_ = serverConnection.Close()
		_ = clientConnection.Close()
	})

	serverDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(serverConnection)
		request, err := http.ReadRequest(reader)
		if err != nil {
			serverDone <- err
			return
		}
		_ = request.Body.Close()
		if request.Host != "provider.test" {
			serverDone <- errors.New("request host was not preserved")
			return
		}
		_, err = io.WriteString(
			serverConnection,
			"HTTP/1.1 200 OK\r\nContent-Length: "+
				strconv.Itoa(len(responseBody))+
				"\r\nConnection: close\r\n\r\n"+
				responseBody,
		)
		serverDone <- err
	}()

	resolver := &resolverStub{addresses: testAddresses("8.8.8.8")}
	var dialCalls atomic.Int64
	var dialTarget string
	var dialMu sync.Mutex
	transport := newGuardedTransport(
		Policy{
			AllowedOrigins: []string{"http://provider.test:80"},
			AllowPlainHTTP: true,
			RequestTimeout: time.Second,
		},
		resolver,
		func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" {
				return nil, errors.New("unexpected dial network")
			}
			dialCalls.Add(1)
			dialMu.Lock()
			dialTarget = address
			dialMu.Unlock()
			return clientConnection, nil
		},
		false,
	)

	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"http://provider.test/resource",
		nil,
	)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() error = %v", err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	_ = response.Body.Close()

	if string(body) != responseBody {
		t.Fatalf("response body = %q, want %q", body, responseBody)
	}
	if got := resolver.calls.Load(); got != 1 {
		t.Fatalf("resolver calls = %d, want exactly 1", got)
	}
	if got := dialCalls.Load(); got != 1 {
		t.Fatalf("dial calls = %d, want exactly 1", got)
	}
	dialMu.Lock()
	gotTarget := dialTarget
	dialMu.Unlock()
	if gotTarget != "8.8.8.8:80" {
		t.Fatalf("dial target = %q, want literal pinned address", gotTarget)
	}
	if strings.Contains(gotTarget, "provider.test") {
		t.Fatalf("dial target reuses hostname: %q", gotTarget)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("in-memory server error = %v", err)
	}
}

func TestGuardedTransportDisablesEnvironmentProxy(t *testing.T) {
	t.Parallel()

	transport := newGuardedTransport(
		Policy{RequestTimeout: time.Second},
		&resolverStub{},
		func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("not used")
		},
		false,
	)
	if transport.transport.Proxy != nil {
		t.Fatal("http.Transport.Proxy is non-nil; environment proxy could bypass DNS pinning")
	}
	if !transport.transport.ForceAttemptHTTP2 {
		t.Fatal("ForceAttemptHTTP2 = false with custom DialContext")
	}
	if transport.transport.TLSClientConfig == nil ||
		transport.transport.TLSClientConfig.MinVersion < 0x0303 {
		t.Fatal("TLS minimum version is weaker than TLS 1.2")
	}
}

func TestGuardedTransportEnforcesSchemeAndEffectivePortOrigin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		rawURL      string
		host        string
		allowHTTP   bool
		wantOutcome PolicyOutcome
	}{
		{
			name:        "plain HTTP blocked before resolution",
			rawURL:      "http://provider.test/resource",
			wantOutcome: PolicyOutcomeBlockedScheme,
		},
		{
			name:        "different explicit port blocked",
			rawURL:      "https://provider.test:8443/resource",
			wantOutcome: PolicyOutcomeBlockedOrigin,
		},
		{
			name:        "different scheme is a different origin",
			rawURL:      "http://provider.test:443/resource",
			allowHTTP:   true,
			wantOutcome: PolicyOutcomeBlockedOrigin,
		},
		{
			name:        "Host override cannot escape URL origin",
			rawURL:      "https://provider.test/resource",
			host:        "attacker.test",
			wantOutcome: PolicyOutcomeBlockedOrigin,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			resolver := &resolverStub{addresses: testAddresses("8.8.8.8")}
			transport := newGuardedTransport(
				Policy{
					AllowedOrigins: []string{"https://provider.test:443"},
					AllowPlainHTTP: test.allowHTTP,
					RequestTimeout: time.Second,
				},
				resolver,
				func(context.Context, string, string) (net.Conn, error) {
					return nil, errors.New("must not dial")
				},
				false,
			)
			request, err := http.NewRequest(http.MethodGet, test.rawURL, nil)
			if err != nil {
				t.Fatalf("http.NewRequest() error = %v", err)
			}
			if test.host != "" {
				request.Host = test.host
			}
			_, err = transport.RoundTrip(request)
			if err == nil {
				t.Fatal("RoundTrip() error = nil")
			}
			if got := policyOutcomeForError(err); got != test.wantOutcome {
				t.Fatalf("policy outcome = %q, want %q", got, test.wantOutcome)
			}
			var providerError *Error
			if !errors.As(err, &providerError) ||
				providerError.Code() != connector.FailurePolicyDenied {
				t.Fatalf("error = %v, want policy_denied", err)
			}
			if got := resolver.calls.Load(); got != 0 {
				t.Fatalf("resolver calls = %d, want 0", got)
			}
		})
	}
}

func TestDialPinnedRejectsHostnameMismatchWithoutDial(t *testing.T) {
	t.Parallel()

	var dialCalls atomic.Int64
	transport := &guardedTransport{
		dialContext: func(context.Context, string, string) (net.Conn, error) {
			dialCalls.Add(1)
			return nil, errors.New("must not dial")
		},
	}
	ctx := context.WithValue(
		context.Background(),
		pinnedResolutionContextKey{},
		pinnedResolution{
			host:      "provider.test",
			port:      "443",
			addresses: testAddresses("8.8.8.8"),
		},
	)
	if _, err := transport.dialPinned(ctx, "tcp", "attacker.test:443"); err == nil {
		t.Fatal("dialPinned() accepted a hostname different from the DNS pin")
	}
	if got := dialCalls.Load(); got != 0 {
		t.Fatalf("dial calls = %d, want 0", got)
	}
}

func testAddresses(values ...string) []netip.Addr {
	addresses := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		addresses = append(addresses, netip.MustParseAddr(value))
	}
	return addresses
}
