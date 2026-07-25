package connector

import (
	"context"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

// CredentialProfile 是 validator 从 Provider 取得的非敏感账户身份。
type CredentialProfile struct {
	AccountID   string
	DisplayName string
}

// CredentialValidationResult 是 credential 验证成功后的身份与权限快照。
// ScopesKnown 为 false 时，GrantedScopes 不参与 Tool scope 授权判断。
type CredentialValidationResult struct {
	Profile       CredentialProfile
	GrantedScopes []string
	ScopesKnown   bool
}

// CredentialValidationInput 是按 AuthType 归一化后的 validator 输入。
// API Key/Custom Credential 使用 Fields；OAuth 使用 AccessToken/TokenType。
type CredentialValidationInput struct {
	ConnectorType Type
	AuthMethodKey string
	AuthType      AuthMethodType
	// Exactly one correlation ID must be set. AuthorizationID identifies a
	// pre-connection authorization attempt; ConnectionID identifies an
	// existing connection.
	ConnectionID    string
	AuthorizationID string
	Config          map[string]any
	Fields          map[string]string
	AccessToken     string
	TokenType       string
}

// CredentialValidator 在 Connection 进入 active 前验证 credential。
type CredentialValidator func(
	context.Context,
	CredentialValidationInput,
) (CredentialValidationResult, error)

// CredentialValidatorMap 按 Connector type 和 auth method key 索引 validator。
type CredentialValidatorMap map[Type]map[string]CredentialValidator

// ScopeMatcher 判断一项 required scope 是否被 granted scopes 满足。
// 没有注册 Provider-local matcher 时，调用方使用精确匹配。
type ScopeMatcher func(granted []string, required string) bool

// ScopeMatcherMap 按 Connector type 和 auth method key 索引 matcher。
type ScopeMatcherMap map[Type]map[string]ScopeMatcher

// ExactScopeMatcher 是没有注册 Provider-local matcher 时的默认精确匹配。
func ExactScopeMatcher(granted []string, required string) bool {
	return slices.Contains(granted, required)
}

// MaxScopeBytes 是单个 scope 字符串允许的最大字节数。
const MaxScopeBytes = 4096

// NormalizeScopes 去空白、去重并排序 scope 集合，同时拒绝非法字符串。
// 这是全服务唯一的 scope 归一化实现：任何更宽松的变体都会静默放大授权，
// 因此新调用方必须复用它而不是自带一份。
func NormalizeScopes(scopes []string) ([]string, error) {
	seen := make(map[string]struct{}, len(scopes))
	normalized := make([]string, 0, len(scopes))
	for _, raw := range scopes {
		scope := strings.TrimSpace(raw)
		if scope == "" {
			continue
		}
		if !utf8.ValidString(scope) ||
			len(scope) > MaxScopeBytes ||
			strings.ContainsAny(scope, "\x00\r\n") {
			return nil, errors.New("connector: invalid scope")
		}
		if _, exists := seen[scope]; exists {
			continue
		}
		seen[scope] = struct{}{}
		normalized = append(normalized, scope)
	}
	slices.Sort(normalized)
	return normalized, nil
}

// CredentialValidationError 是 validator 可安全返回给连接 API 的预期失败。
// Provider 原始响应和底层 error chain 不得放入该类型。
type CredentialValidationError struct {
	Code              FailureCode
	SafeMessage       string
	Temporary         bool
	RetryAfterSeconds int
	UpstreamStatus    int
}

func (e *CredentialValidationError) Error() string {
	if e == nil {
		return ""
	}
	if e.SafeMessage != "" {
		return e.SafeMessage
	}
	return string(e.Code)
}
