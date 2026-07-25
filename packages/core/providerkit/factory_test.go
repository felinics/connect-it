package providerkit

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

func TestNewFactoryUsesClosedSafeDefaults(t *testing.T) {
	t.Parallel()

	factory := NewFactory()
	if factory == nil {
		t.Fatal("NewFactory() returned nil")
	}
	if factory.allowPrivateNetwork {
		t.Fatal("private Provider network enabled by default")
	}
	if factory.allowInsecureHTTP {
		t.Fatal("insecure Provider HTTP enabled by default")
	}
	if factory.resolver == nil || factory.dialContext == nil || factory.clock == nil {
		t.Fatalf("factory dependencies not initialized: %#v", factory)
	}

	client, err := factory.NewStaticClient(factoryTestHTTPSPolicy())
	if err != nil {
		t.Fatalf("NewStaticClient() error = %v", err)
	}
	if client.transport == nil || client.transport.transport == nil {
		t.Fatal("NewStaticClient() did not create a guarded transport")
	}
	if client.transport.transport.Proxy != nil {
		t.Fatal("guarded transport must ignore environment proxies")
	}
	if client.transport.allowPrivate {
		t.Fatal("PublicOnly client inherited private-network capability")
	}
	if client.policy.AllowPlainHTTP {
		t.Fatal("HTTPS policy unexpectedly allows plain HTTP")
	}
}

func TestNewFactoryDoesNotReadDeploymentEnvironment(t *testing.T) {
	t.Setenv(EnvAllowPrivateProviderNetwork, "true")
	t.Setenv(EnvAllowInsecureProviderHTTP, "true")

	factory := NewFactory()
	if factory.allowPrivateNetwork || factory.allowInsecureHTTP {
		t.Fatalf(
			"NewFactory() read deployment environment: private=%v http=%v",
			factory.allowPrivateNetwork,
			factory.allowInsecureHTTP,
		)
	}
}

func TestStrictBoolEnv(t *testing.T) {
	const name = "CONNECT_IT_PROVIDERKIT_STRICT_BOOL_TEST"
	original, existed := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset test environment: %v", err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(name, original)
			return
		}
		_ = os.Unsetenv(name)
	})

	value, err := strictBoolEnv(name)
	if err != nil || value {
		t.Fatalf("unset strictBoolEnv() = (%v, %v)", value, err)
	}

	for _, test := range []struct {
		raw     string
		want    bool
		wantErr bool
	}{
		{raw: "", want: false},
		{raw: " ", wantErr: true},
		{raw: "\t", wantErr: true},
		{raw: "true", want: true},
		{raw: "false", want: false},
		{raw: " true", wantErr: true},
		{raw: "true ", wantErr: true},
		{raw: "TRUE", wantErr: true},
		{raw: "False", wantErr: true},
		{raw: "1", wantErr: true},
		{raw: "0", wantErr: true},
		{raw: "t", wantErr: true},
		{raw: "yes", wantErr: true},
	} {
		if err := os.Setenv(name, test.raw); err != nil {
			t.Fatalf("set test environment: %v", err)
		}
		got, parseErr := strictBoolEnv(name)
		if (parseErr != nil) != test.wantErr || got != test.want {
			t.Errorf(
				"strictBoolEnv(%q) = (%v, %v), want (%v, error=%v)",
				test.raw,
				got,
				parseErr,
				test.want,
				test.wantErr,
			)
		}
	}
}

func TestFactoryDynamicPolicyDerivesExactOrigin(t *testing.T) {
	t.Parallel()

	factory := NewFactory()
	client, err := factory.NewDynamicClient(DynamicPolicyInput{
		Provider:          "gitlab",
		BaseURL:           "https://GitLab.Example.test/api/v4",
		AllowInsecureHTTP: "false",
		RedirectMode:      RedirectDenyAll,
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := client.Policy()
	if len(policy.AllowedOrigins) != 1 ||
		policy.AllowedOrigins[0] != "https://gitlab.example.test:443" ||
		len(policy.AllowedRedirectOrigins) != 0 ||
		policy.RedirectMode != RedirectDenyAll ||
		policy.NetworkMode != SelfHostedOptIn {
		t.Fatalf("derived dynamic policy = %#v", policy)
	}
}

func TestFactoryDynamicPolicyStrictlyParsesHTTPOptIn(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{"", "False", " true", "yes", "0"} {
		_, err := NewFactory().NewDynamicClient(DynamicPolicyInput{
			Provider:          "gitlab",
			BaseURL:           "https://gitlab.example.test/api/v4",
			AllowInsecureHTTP: raw,
		})
		if err == nil {
			t.Fatalf("AllowInsecureHTTP %q was accepted", raw)
		}
	}

	input := DynamicPolicyInput{
		Provider:          "gitlab",
		BaseURL:           "http://gitlab.example.test/api/v4",
		AllowInsecureHTTP: "true",
	}
	if _, err := NewFactory().NewDynamicClient(input); err == nil {
		t.Fatal("dynamic policy bypassed deployment HTTP gate")
	}
	client, err := NewFactory(
		WithInsecureProviderHTTP(true),
	).NewDynamicClient(input)
	if err != nil {
		t.Fatal(err)
	}
	if !client.Policy().AllowPlainHTTP {
		t.Fatal("both dynamic HTTP gates did not enable plain HTTP")
	}
}

func TestFactoryRejectsBlockedLiteralOriginsAtConstruction(t *testing.T) {
	t.Parallel()

	for _, rawURL := range []string{
		"https://127.0.0.1/",
		"https://169.254.169.254/",
		"https://10.0.0.8/",
	} {
		origin, err := CanonicalizeOrigin(rawURL)
		if err != nil {
			t.Fatal(err)
		}
		_, err = NewFactory().NewStaticClient(Policy{
			Provider:       "literal",
			BaseURL:        rawURL,
			AllowedOrigins: []string{origin},
		})
		if err == nil {
			t.Fatalf("blocked literal %q passed client construction", rawURL)
		}
	}

	privateInput := DynamicPolicyInput{
		Provider:          "gitlab",
		BaseURL:           "https://10.0.0.8/api/v4",
		AllowInsecureHTTP: "false",
	}
	if _, err := NewFactory().NewDynamicClient(privateInput); err == nil {
		t.Fatal("dynamic private literal bypassed deployment gate")
	}
	if _, err := NewFactory(
		WithPrivateProviderNetwork(true),
	).NewDynamicClient(privateInput); err != nil {
		t.Fatalf("reviewed private literal with both gates failed: %v", err)
	}
}

func TestNewFactoryFromEnvStrictlyLoadsBothDeploymentGates(t *testing.T) {
	t.Setenv(EnvAllowPrivateProviderNetwork, "true")
	t.Setenv(EnvAllowInsecureProviderHTTP, "true")

	factory, err := NewFactoryFromEnv()
	if err != nil {
		t.Fatalf("NewFactoryFromEnv() error = %v", err)
	}
	if !factory.allowPrivateNetwork || !factory.allowInsecureHTTP {
		t.Fatalf(
			"environment gates = private:%v http:%v",
			factory.allowPrivateNetwork,
			factory.allowInsecureHTTP,
		)
	}

	t.Setenv(EnvAllowPrivateProviderNetwork, "TRUE")
	if _, err = NewFactoryFromEnv(); err == nil {
		t.Fatal("invalid private-network environment value accepted")
	}

	t.Setenv(EnvAllowPrivateProviderNetwork, "false")
	t.Setenv(EnvAllowInsecureProviderHTTP, "yes")
	if _, err = NewFactoryFromEnv(); err == nil {
		t.Fatal("invalid insecure-HTTP environment value accepted")
	}
}

func TestNewFactoryFromEnvCannotBeWidenedByOptions(t *testing.T) {
	t.Setenv(EnvAllowPrivateProviderNetwork, "false")
	t.Setenv(EnvAllowInsecureProviderHTTP, "false")

	factory, err := NewFactoryFromEnv(
		WithPrivateProviderNetwork(true),
		WithInsecureProviderHTTP(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if factory.allowPrivateNetwork || factory.allowInsecureHTTP {
		t.Fatal("FactoryOption bypassed administrator-controlled environment gates")
	}
}

func TestFactoryCombinesPolicyAndDeploymentNetworkGates(t *testing.T) {
	t.Parallel()

	publicPolicy := factoryTestHTTPSPolicy()
	selfHostedPolicy := factoryTestHTTPSPolicy()
	selfHostedPolicy.Provider = "gitlab-self-hosted"
	selfHostedPolicy.NetworkMode = SelfHostedOptIn

	for _, test := range []struct {
		name               string
		factory            *Factory
		policy             Policy
		wantAllowPrivate   bool
		wantAllowPlainHTTP bool
	}{
		{
			name:    "public deployment closed",
			factory: NewFactory(),
			policy:  publicPolicy,
		},
		{
			name: "public policy cannot be widened",
			factory: NewFactory(
				WithPrivateProviderNetwork(true),
				WithInsecureProviderHTTP(true),
			),
			policy: publicPolicy,
		},
		{
			name:    "self hosted deployment closed",
			factory: NewFactory(),
			policy:  selfHostedPolicy,
		},
		{
			name: "self hosted private opt in",
			factory: NewFactory(
				WithPrivateProviderNetwork(true),
			),
			policy:           selfHostedPolicy,
			wantAllowPrivate: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client, err := test.factory.newClient(test.policy)
			if err != nil {
				t.Fatalf("newClient() error = %v", err)
			}
			if got := client.transport.allowPrivate; got != test.wantAllowPrivate {
				t.Fatalf("allowPrivate = %v, want %v", got, test.wantAllowPrivate)
			}
			if got := client.policy.AllowPlainHTTP; got != test.wantAllowPlainHTTP {
				t.Fatalf(
					"AllowPlainHTTP = %v, want %v",
					got,
					test.wantAllowPlainHTTP,
				)
			}
		})
	}
}

func TestFactoryRequiresBothPlainHTTPGates(t *testing.T) {
	t.Parallel()

	httpPolicy := Policy{
		Provider:         "gitlab-self-hosted",
		BaseURL:          "http://gitlab.example.test/api/v4",
		AllowedOrigins:   []string{"http://gitlab.example.test"},
		NetworkMode:      SelfHostedOptIn,
		AllowPlainHTTP:   true,
		RequestTimeout:   time.Second,
		MaxResponseBytes: 1024,
		Retry: RetryPolicy{
			MaxRetries:     1,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     time.Millisecond,
		},
	}

	if _, err := NewFactory().newClient(httpPolicy); err == nil {
		t.Fatal("policy opt-in bypassed deployment insecure-HTTP gate")
	} else {
		var policyErr *PolicyError
		if !errors.As(err, &policyErr) || policyErr.Field != "AllowPlainHTTP" {
			t.Fatalf("error = %T %v, want AllowPlainHTTP PolicyError", err, err)
		}
	}

	client, err := NewFactory(
		WithInsecureProviderHTTP(true),
	).newClient(httpPolicy)
	if err != nil {
		t.Fatalf("both HTTP gates enabled: %v", err)
	}
	if !client.policy.AllowPlainHTTP {
		t.Fatal("normalized policy lost explicit HTTP opt-in")
	}

	publicHTTP := httpPolicy
	publicHTTP.Provider = "fixed-saas"
	publicHTTP.NetworkMode = PublicOnly
	if _, err = NewFactory(
		WithInsecureProviderHTTP(true),
	).NewStaticClient(publicHTTP); err == nil {
		t.Fatal("PublicOnly policy was widened to plain HTTP")
	}

	missingPolicyGate := httpPolicy
	missingPolicyGate.AllowPlainHTTP = false
	if _, err = NewFactory(
		WithInsecureProviderHTTP(true),
	).newClient(missingPolicyGate); err == nil {
		t.Fatal("deployment HTTP gate bypassed policy opt-in")
	}
}

func TestStaticConstructorRejectsSelfHostedPolicies(t *testing.T) {
	t.Parallel()

	policy := factoryTestHTTPSPolicy()
	policy.NetworkMode = SelfHostedOptIn
	if _, err := NewFactory(
		WithPrivateProviderNetwork(true),
	).NewStaticClient(policy); err == nil {
		t.Fatal("static constructor accepted a self-hosted policy")
	}
}

func TestFactoryCreatesIndependentPolicyBoundTransports(t *testing.T) {
	t.Parallel()

	factory := NewFactory()
	policy := factoryTestHTTPSPolicy()
	first, err := factory.NewStaticClient(policy)
	if err != nil {
		t.Fatalf("first NewStaticClient() error = %v", err)
	}
	second, err := factory.NewStaticClient(policy)
	if err != nil {
		t.Fatalf("second NewStaticClient() error = %v", err)
	}
	if first == second ||
		first.transport == second.transport ||
		first.transport.transport == second.transport.transport {
		t.Fatal("clients unexpectedly share a client, guard, or connection pool")
	}
	if first.transport.transport.TLSClientConfig ==
		second.transport.transport.TLSClientConfig {
		t.Fatal("clients unexpectedly share mutable TLS configuration")
	}

	policy.AllowedOrigins[0] = "https://evil.example"
	policy.AllowedRedirectOrigins[0] = "https://evil.example"
	if got := first.Policy(); got.AllowedOrigins[0] != "https://api.example.com:443" ||
		got.AllowedRedirectOrigins[0] != "https://redirect.example.com:443" {
		t.Fatalf("client policy aliases caller slices: %#v", got)
	}

	returned := first.Policy()
	returned.AllowedOrigins[0] = "https://mutated.example"
	if got := first.Policy().AllowedOrigins[0]; got != "https://api.example.com:443" {
		t.Fatalf("Policy() returned aliased slice: %q", got)
	}
}

func TestFactoryDependencyInjectionAndNilFallbacks(t *testing.T) {
	t.Parallel()

	resolver := &factoryTestResolver{}
	clock := &factoryTestClock{
		now: time.Date(2026, time.July, 23, 0, 0, 0, 0, time.UTC),
	}
	observer := ObserverFunc(func(context.Context, RequestEvent) {})
	dialCalls := 0
	dial := func(context.Context, string, string) (net.Conn, error) {
		dialCalls++
		return nil, errors.New("test dialer")
	}
	factory := NewFactory(
		nil,
		WithResolver(resolver),
		WithClock(clock),
		WithObserver(observer),
		WithDialContext(dial),
	)
	if factory.resolver != resolver || factory.clock != clock ||
		factory.observer == nil || factory.dialContext == nil {
		t.Fatalf("injected dependencies not retained: %#v", factory)
	}
	client, err := factory.NewStaticClient(factoryTestHTTPSPolicy())
	if err != nil {
		t.Fatalf("NewStaticClient() error = %v", err)
	}
	if client.clock != clock || client.observer == nil ||
		client.transport.resolver != resolver ||
		client.transport.dialContext == nil {
		t.Fatalf("dependencies not propagated to client: %#v", client)
	}
	if dialCalls != 0 {
		t.Fatal("constructing a client performed network I/O")
	}
	if sleepErr := client.clock.Sleep(
		context.Background(),
		37*time.Second,
	); sleepErr != nil {
		t.Fatalf("injected clock Sleep() error = %v", sleepErr)
	}
	if len(clock.sleeps) != 1 || clock.sleeps[0] != 37*time.Second {
		t.Fatalf("injected clock sleeps = %#v", clock.sleeps)
	}

	fallback := NewFactory(
		WithResolver(nil),
		WithDialContext(nil),
		WithClock(nil),
	)
	if fallback.resolver == nil || fallback.dialContext == nil || fallback.clock == nil {
		t.Fatal("nil injected dependencies were not replaced with safe defaults")
	}
}

func TestFactoryInvalidStaticAndDynamicPoliciesFailLocally(t *testing.T) {
	t.Parallel()

	factory := NewFactory()
	invalidPolicies := []Policy{
		{},
		{
			Provider:       "static-saas",
			BaseURL:        "https://api.example.com",
			AllowedOrigins: []string{"https://other.example.com"},
		},
		{
			Provider:       "dynamic-self-hosted",
			BaseURL:        "https://user:secret@api.example.com",
			AllowedOrigins: []string{"https://api.example.com"},
			NetworkMode:    SelfHostedOptIn,
		},
	}
	for index, policy := range invalidPolicies {
		if _, err := factory.NewStaticClient(policy); err == nil {
			t.Fatalf("invalid policy %d accepted", index)
		} else {
			var policyErr *PolicyError
			if !errors.As(err, &policyErr) {
				t.Fatalf("invalid policy error = %T %v, want PolicyError", err, err)
			}
		}
	}

	// Static runtime construction can propagate the returned error and abort
	// startup. A dynamic Connection can reject only this value; the same
	// Factory remains healthy for subsequent valid Connections.
	if _, err := factory.NewStaticClient(factoryTestHTTPSPolicy()); err != nil {
		t.Fatalf("invalid dynamic policy poisoned factory: %v", err)
	}

	var nilFactory *Factory
	if _, err := nilFactory.NewStaticClient(factoryTestHTTPSPolicy()); err == nil {
		t.Fatal("nil factory did not return an error")
	}
}

func factoryTestHTTPSPolicy() Policy {
	return Policy{
		Provider:               "fixed-saas",
		BaseURL:                "https://api.example.com/v1",
		AllowedOrigins:         []string{"https://api.example.com"},
		AllowedRedirectOrigins: []string{"https://redirect.example.com"},
		RequestTimeout:         time.Second,
		MaxResponseBytes:       1024,
		Retry: RetryPolicy{
			MaxRetries:     1,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     time.Millisecond,
		},
	}
}

type factoryTestResolver struct {
	calls int
}

func (resolver *factoryTestResolver) LookupNetIP(
	context.Context,
	string,
	string,
) ([]netip.Addr, error) {
	resolver.calls++
	return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
}

type factoryTestClock struct {
	now      time.Time
	sleeps   []time.Duration
	sleepErr error
}

func (clock *factoryTestClock) Now() time.Time {
	return clock.now
}

func (clock *factoryTestClock) Sleep(
	_ context.Context,
	duration time.Duration,
) error {
	clock.sleeps = append(clock.sleeps, duration)
	return clock.sleepErr
}

var _ Resolver = (*factoryTestResolver)(nil)
var _ Clock = (*factoryTestClock)(nil)
