package oauthsvc

import (
	"fmt"
	"regexp"
)

var endpointPlaceholder = regexp.MustCompile(`\{([a-z0-9_]+)\}`)

// ExpandEndpoint 把 OAuth endpoint 中的 {config_key} 占位符替换为管理员配置值
// （config 是运行时装配、合并默认值之后的公开配置）。
// OAuthConfig 是纯数据，模板替换是 oauthsvc 的运行时职责；
// 不含占位符的 endpoint 原样返回。占位符缺少对应的非空字符串值时报错。
func ExpandEndpoint(endpoint string, config map[string]any) (string, error) {
	var firstErr error
	out := endpointPlaceholder.ReplaceAllStringFunc(endpoint, func(m string) string {
		key := m[1 : len(m)-1]
		v, ok := config[key].(string)
		if !ok || v == "" {
			if firstErr == nil {
				firstErr = fmt.Errorf("oauth endpoint 占位符 {%s} 缺少对应配置值", key)
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
