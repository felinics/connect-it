package providerkit

import (
	"crypto/x509"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	EnvAllowPrivateProviderNetwork = "CONNECT_IT_ALLOW_PRIVATE_PROVIDER_NETWORK"
	EnvAllowInsecureProviderHTTP   = "CONNECT_IT_ALLOW_INSECURE_PROVIDER_HTTP"
)

// Factory 是 Provider runtime 资源的构造入口。部署级网络开关只能在这里计算，
// Policy 本身不能把 PublicOnly client 提升为私网或明文 HTTP client。
type Factory struct {
	allowPrivateNetwork bool
	allowInsecureHTTP   bool
	resolver            Resolver
	dialContext         DialContextFunc
	observer            Observer
	clock               Clock
	rootCAs             *x509.CertPool
}

type FactoryOption func(*Factory)

// DynamicPolicyInput is the non-secret policy identity for one configured
// self-hosted Provider. AllowedOrigins is deliberately absent: the factory
// derives exactly one origin from BaseURL, so a second user-controlled field
// cannot widen it.
type DynamicPolicyInput struct {
	Provider          string
	BaseURL           string
	AllowInsecureHTTP string
	RedirectMode      RedirectMode
	RequestTimeout    time.Duration
	MaxResponseBytes  int64
	Retry             RetryPolicy
}

func WithPrivateProviderNetwork(enabled bool) FactoryOption {
	return func(factory *Factory) {
		factory.allowPrivateNetwork = enabled
	}
}

func WithInsecureProviderHTTP(enabled bool) FactoryOption {
	return func(factory *Factory) {
		factory.allowInsecureHTTP = enabled
	}
}

func WithResolver(resolver Resolver) FactoryOption {
	return func(factory *Factory) {
		factory.resolver = resolver
	}
}

func WithDialContext(dial DialContextFunc) FactoryOption {
	return func(factory *Factory) {
		factory.dialContext = dial
	}
}

func WithObserver(observer Observer) FactoryOption {
	return func(factory *Factory) {
		factory.observer = observer
	}
}

func WithClock(clock Clock) FactoryOption {
	return func(factory *Factory) {
		factory.clock = clock
	}
}

// WithRootCAs adds trust anchors for policy-bound TLS clients. The pool is
// cloned both when accepted and when each transport is built, so callers
// cannot mutate a live client's trust boundary. Production normally relies on
// the system pool; this option also enables deterministic private-PKI tests
// without weakening certificate or hostname verification.
func WithRootCAs(pool *x509.CertPool) FactoryOption {
	return func(factory *Factory) {
		if pool == nil {
			factory.rootCAs = nil
			return
		}
		factory.rootCAs = pool.Clone()
	}
}

// NewFactory 创建一个不开放私网或明文 HTTP 的 Factory。它不读取环境变量，适用于
// 单元测试和显式装配；生产 composition root 应使用 NewFactoryFromEnv。
func NewFactory(options ...FactoryOption) *Factory {
	factory := &Factory{
		resolver:    defaultResolver{},
		dialContext: defaultDialContext(),
		clock:       realClock{},
	}
	for _, option := range options {
		if option != nil {
			option(factory)
		}
	}
	if factory.resolver == nil {
		factory.resolver = defaultResolver{}
	}
	if factory.dialContext == nil {
		factory.dialContext = defaultDialContext()
	}
	if factory.clock == nil {
		factory.clock = realClock{}
	}
	return factory
}

// NewFactoryFromEnv 严格读取部署级安全开关。空值等价于 false；除 true/false 外的值
// 都是启动错误，不能因为拼写错误意外放宽网络边界。
func NewFactoryFromEnv(options ...FactoryOption) (*Factory, error) {
	allowPrivate, err := strictBoolEnv(EnvAllowPrivateProviderNetwork)
	if err != nil {
		return nil, err
	}
	allowHTTP, err := strictBoolEnv(EnvAllowInsecureProviderHTTP)
	if err != nil {
		return nil, err
	}
	base := append([]FactoryOption(nil), options...)
	// Environment values are appended last so a production caller cannot use
	// a test/injection option to widen the administrator-controlled boundary.
	base = append(
		base,
		WithPrivateProviderNetwork(allowPrivate),
		WithInsecureProviderHTTP(allowHTTP),
	)
	return NewFactory(base...), nil
}

func strictBoolEnv(name string) (bool, error) {
	raw, exists := os.LookupEnv(name)
	if !exists || raw == "" {
		return false, nil
	}
	if raw != strings.TrimSpace(raw) {
		return false, fmt.Errorf("%s 必须是 true 或 false", name)
	}
	value, err := strconv.ParseBool(raw)
	if err != nil || (raw != "true" && raw != "false") {
		return false, fmt.Errorf("%s 必须是 true 或 false", name)
	}
	return value, nil
}

// NewStaticClient validates a reviewed, code-defined policy. Different
// policies use independent Transports/pools.
func (factory *Factory) NewStaticClient(policy Policy) (*Client, error) {
	if policy.NetworkMode != PublicOnly {
		return nil, &PolicyError{
			Field:  "NetworkMode",
			Reason: "self-hosted policies must use NewDynamicClient",
		}
	}
	return factory.newClient(policy)
}

// NewDynamicClient derives an exact initial origin from a configured BaseURL.
// It is the only constructor self-hosted Provider handlers should use.
func (factory *Factory) NewDynamicClient(
	input DynamicPolicyInput,
) (*Client, error) {
	allowHTTP, err := strictNormalizedBool(
		"AllowInsecureHTTP",
		input.AllowInsecureHTTP,
	)
	if err != nil {
		return nil, err
	}
	baseURL, err := parseBaseURL(input.BaseURL)
	if err != nil {
		return nil, &PolicyError{
			Field:  "BaseURL",
			Reason: errorReason(err),
		}
	}
	origin, err := CanonicalOrigin(baseURL)
	if err != nil {
		return nil, &PolicyError{
			Field:  "BaseURL",
			Reason: errorReason(err),
		}
	}
	return factory.newClient(Policy{
		Provider:         input.Provider,
		BaseURL:          baseURL.String(),
		AllowedOrigins:   []string{origin},
		RedirectMode:     input.RedirectMode,
		NetworkMode:      SelfHostedOptIn,
		AllowPlainHTTP:   allowHTTP,
		RequestTimeout:   input.RequestTimeout,
		MaxResponseBytes: input.MaxResponseBytes,
		Retry:            input.Retry,
	})
}

func strictNormalizedBool(field, raw string) (bool, error) {
	switch raw {
	case "false":
		return false, nil
	case "true":
		return true, nil
	default:
		return false, &PolicyError{
			Field:  field,
			Reason: "must be exactly true or false",
		}
	}
}

func (factory *Factory) newClient(policy Policy) (*Client, error) {
	if factory == nil {
		return nil, fmt.Errorf("providerkit: factory 不能为空")
	}
	normalized, err := NormalizePolicy(policy)
	if err != nil {
		return nil, err
	}
	effectivePrivate := normalized.NetworkMode == SelfHostedOptIn &&
		factory.allowPrivateNetwork
	if err := validateLiteralOrigins(
		append(
			append([]string(nil), normalized.AllowedOrigins...),
			normalized.AllowedRedirectOrigins...,
		),
		effectivePrivate,
	); err != nil {
		return nil, &PolicyError{
			Field:  "AllowedOrigins",
			Reason: "contains an IP blocked by deployment policy",
		}
	}
	if normalized.AllowPlainHTTP && !factory.allowInsecureHTTP {
		return nil, &PolicyError{
			Field:  "AllowPlainHTTP",
			Reason: "deployment has not enabled insecure Provider HTTP",
		}
	}
	transport := newGuardedTransport(
		normalized,
		factory.resolver,
		factory.dialContext,
		effectivePrivate,
		factory.rootCAs,
	)
	return newClient(
		normalized,
		transport,
		factory.observer,
		factory.clock,
	), nil
}
