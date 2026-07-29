package oauthsvc

import (
	"fmt"
	"regexp"
)

var endpointPlaceholder = regexp.MustCompile(`\{([a-z0-9_]+)\}`)

// ExpandEndpoint replaces {config_key} placeholders in an OAuth endpoint with
// administrator config values, where config is the public config assembled at
// runtime with defaults merged in. OAuthConfig is pure data, so template
// expansion is a runtime responsibility of oauthsvc. An endpoint without
// placeholders is returned unchanged, and a placeholder with no corresponding
// non-empty string value is an error.
func ExpandEndpoint(endpoint string, config map[string]any) (string, error) {
	var firstErr error
	out := endpointPlaceholder.ReplaceAllStringFunc(endpoint, func(m string) string {
		key := m[1 : len(m)-1]
		v, ok := config[key].(string)
		if !ok || v == "" {
			if firstErr == nil {
				firstErr = fmt.Errorf("oauth endpoint placeholder {%s} has no config value", key)
			}
			return m
		}
		return v
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}
