package oauthsvc

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

var errInvalidStaticOAuthPolicy = errors.New(
	"oauthsvc: static OAuth egress policy is invalid",
)

// newPublicOAuthPolicyClient is the single constructor for code-reviewed
// static OAuth origins. It performs no network I/O; providerkit validates the
// complete PublicOnly policy while constructing the client.
func newPublicOAuthPolicyClient(
	factory *providerkit.Factory,
	provider connector.Type,
	origins []string,
) (*providerkit.Client, error) {
	if factory == nil || provider == "" || len(origins) == 0 {
		return nil, errInvalidStaticOAuthPolicy
	}
	return factory.NewStaticClient(providerkit.Policy{
		Provider:         string(provider),
		BaseURL:          origins[0],
		AllowedOrigins:   origins,
		RedirectMode:     providerkit.RedirectDenyAll,
		NetworkMode:      providerkit.PublicOnly,
		RequestTimeout:   tokenRequestTimeout,
		MaxResponseBytes: maxTokenResponseBytes,
		Retry:            providerkit.RetryPolicy{Disabled: true},
	})
}

// validateOAuthEndpoint 是 OAuth 出站 origin 的唯一校验入口：已展开的 endpoint
// 必须是 https 且落在 Definition 声明的规范 origin 内。两类失败都拒绝出站请求；
// 调用方只负责把它们映射成各自的对外 error。
func validateOAuthEndpoint(
	raw string,
	origins []string,
) (*url.URL, error) {
	if len(origins) == 0 {
		return nil, errInvalidStaticOAuthPolicy
	}
	for _, origin := range origins {
		canonical, err := providerkit.CanonicalizeOrigin(origin)
		if err != nil || canonical != origin {
			return nil, errInvalidStaticOAuthPolicy
		}
	}
	endpoint, err := providerkit.ValidateURLForOrigin(raw, origins)
	if err != nil || endpoint.Scheme != "https" {
		return nil, ErrEgressPolicy
	}
	return endpoint, nil
}

// newTokenPolicyClient validates the concrete, already-expanded token endpoint
// and binds it to the same static policy used by startup preflight.
func newTokenPolicyClient(
	factory *providerkit.Factory,
	provider connector.Type,
	oc *connector.OAuthConfig,
	rawEndpoint string,
) (*providerkit.Client, *url.URL, error) {
	if oc == nil {
		return nil, nil, tokenConfigurationError()
	}
	endpoint, err := validateOAuthEndpoint(
		rawEndpoint,
		oc.Egress.TokenOrigins,
	)
	if errors.Is(err, ErrEgressPolicy) {
		return nil, nil, tokenPolicyError()
	}
	if err != nil {
		return nil, nil, tokenConfigurationError()
	}
	client, err := newPublicOAuthPolicyClient(
		factory,
		provider,
		oc.Egress.TokenOrigins,
	)
	if err != nil {
		return nil, nil, tokenConfigurationError()
	}
	return client, endpoint, nil
}

// PreflightDefinitions constructs every code-defined static OAuth policy
// without performing network I/O. Definitions must come from Registry.All so
// endpoint/origin relationships have already passed registry validation.
func PreflightDefinitions(
	factory *providerkit.Factory,
	definitions []connector.Definition,
) error {
	if factory == nil {
		return errInvalidStaticOAuthPolicy
	}
	for _, definition := range definitions {
		for _, method := range definition.AuthMethods {
			if oauth := method.OAuth; oauth != nil {
				if err := preflightOriginPolicy(
					factory,
					definition.Type,
					method.Key,
					"authorization",
					oauth.Egress.AuthorizationOrigins,
				); err != nil {
					return err
				}
				if err := preflightOriginPolicy(
					factory,
					definition.Type,
					method.Key,
					"token",
					oauth.Egress.TokenOrigins,
				); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func preflightOriginPolicy(
	factory *providerkit.Factory,
	provider connector.Type,
	methodKey string,
	policyKind string,
	origins []string,
) error {
	client, err := newPublicOAuthPolicyClient(factory, provider, origins)
	if err != nil {
		return fmt.Errorf(
			"connector %q auth method %q %s policy: %w",
			provider,
			methodKey,
			policyKind,
			err,
		)
	}
	client.CloseIdleConnections()
	return nil
}
