package oauthsvc

import (
	"errors"
	"fmt"
	"strings"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

// ExpandEndpoints 就地展开 OAuth 配置里的全部 endpoint 占位符。可选的
// RefreshTokenEndpoint 为空时跳过；其余 endpoint 由 Registry 保证非空。
func ExpandEndpoints(oc *connector.OAuthConfig, config map[string]any) error {
	if oc == nil {
		return errors.New("oauthsvc: OAuth config is missing")
	}
	for _, endpoint := range []*string{
		&oc.AuthorizationEndpoint,
		&oc.TokenEndpoint,
		&oc.RefreshTokenEndpoint,
	} {
		if *endpoint == "" {
			continue
		}
		expanded, err := expandEndpoint(*endpoint, config)
		if err != nil {
			return err
		}
		*endpoint = expanded
	}
	return nil
}

// expandEndpoint replaces OAuth endpoint placeholders with escaped public
// configuration. Placeholders may affect only path/query data; scheme,
// hostname and port are always fixed by the reviewed Definition.
func expandEndpoint(endpoint string, config map[string]any) (string, error) {
	authorityEnd := len(endpoint)
	if scheme := strings.Index(endpoint, "://"); scheme >= 0 {
		if relative := strings.IndexAny(endpoint[scheme+3:], "/?#"); relative >= 0 {
			authorityEnd = scheme + 3 + relative
		}
	}
	if len(providerkit.EndpointPlaceholders(endpoint[:authorityEnd])) > 0 {
		return "", fmt.Errorf(
			"oauth endpoint placeholder 不得改变 scheme、hostname 或 port",
		)
	}

	out, missing := providerkit.ExpandEndpoint(
		endpoint,
		func(key string) string {
			v, _ := config[key].(string)
			return v
		},
	)
	if len(missing) > 0 {
		return "", fmt.Errorf(
			"oauth endpoint 占位符 {%s} 缺少对应配置值",
			missing[0],
		)
	}
	if _, err := providerkit.ParseAndValidateURL(out); err != nil {
		return "", fmt.Errorf("oauth endpoint 展开后非法")
	}
	return out, nil
}
