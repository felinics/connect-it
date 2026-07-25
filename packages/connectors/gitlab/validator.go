package gitlab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/memohai/connect-it/packages/connectors/internal/credentialvalidator"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	gitLabAccessTokenAuthMethod = "access_token"
	gitLabTokenField            = "token"
	gitLabMCPURLField           = "mcp_url"
	gitLabAllowHTTPField        = "allow_insecure_http"

	// MCP 是实例自身的一个 REST 命名空间，endpoint 必然以此结尾；去掉它就得到
	// 实例根，Doorkeeper 的 token 自省 endpoint 挂在实例根下。
	gitLabMCPPathSuffix = "/api/v4/mcp"
	gitLabTokenInfoPath = "oauth/token/info"

	gitLabMaxResponseBytes = 64 << 10
)

// NewCredentialValidators 构造 access token validator。
//
// GitLab 的 /api/v4/mcp 只接受带 mcp scope 的 OAuth access token，因此这里不能用
// /api/v4/user 之类需要 read_user/api scope 的 endpoint 校验 —— 一个合法的 mcp-only
// token 会被它 403 掉。改用实例自带的 Doorkeeper token 自省 endpoint：它不要求任何
// scope，却能回报 token 主体与真实 scope 集合；PAT 不走 Doorkeeper，因此会被它拒绝，
// 与 MCP endpoint 的判定一致。
func NewCredentialValidators(
	factory *providerkit.Factory,
) (map[string]connector.CredentialValidator, error) {
	if factory == nil {
		return nil, fmt.Errorf("gitlab: provider client factory is required")
	}
	return map[string]connector.CredentialValidator{
		gitLabAccessTokenAuthMethod: func(
			ctx context.Context,
			input connector.CredentialValidationInput,
		) (connector.CredentialValidationResult, error) {
			return validateCredential(ctx, factory, input)
		},
	}, nil
}

// validateCredential 每次校验都现场派生 client：GitLab 是自托管实例，探针的目标要
// 先从 mcp_url 解析出实例根、过明文 HTTP 闸门才能确定，没法在构造期钉死一个 static
// client。实例根既决定请求目标又折进 AccountID，投影不是 credentialvalidator.Probe
// 假设的"只看响应体"的纯函数 —— 共享包的 doc comment 把这种形状列为该手写。
func validateCredential(
	ctx context.Context,
	factory *providerkit.Factory,
	input connector.CredentialValidationInput,
) (connector.CredentialValidationResult, error) {
	fields, err := credentialvalidator.FieldsInput(
		input,
		Definition.Type,
		gitLabAccessTokenAuthMethod,
		connector.AuthAPIKey,
		gitLabTokenField,
	)
	if err != nil {
		return connector.CredentialValidationResult{}, err
	}
	if !credentialvalidator.OpaqueSecret(fields[gitLabTokenField]) {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(connector.FailureAuthorizationFailed, 0, 0)
	}
	authorizer, err := credentialvalidator.Bearer(fields[gitLabTokenField])
	if err != nil {
		return connector.CredentialValidationResult{}, err
	}
	instance, err := gitLabInstanceBaseURL(input.Config)
	if err != nil {
		return connector.CredentialValidationResult{}, err
	}
	allowInsecureHTTP, err := gitLabAllowInsecureHTTP(input.Config)
	if err != nil {
		return connector.CredentialValidationResult{}, err
	}
	client, err := factory.NewDynamicClient(providerkit.DynamicPolicyInput{
		Provider:          string(Definition.Type),
		BaseURL:           instance,
		AllowInsecureHTTP: allowInsecureHTTP,
		RedirectMode:      providerkit.RedirectDenyAll,
		RequestTimeout:    providerkit.DefaultRequestTimeout,
		MaxResponseBytes:  gitLabMaxResponseBytes,
		Retry:             providerkit.RetryPolicy{Disabled: true},
	})
	if err != nil {
		// endpoint 已被规范化过，剩下的拒绝都是部署级网络策略。
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(connector.FailurePolicyDenied, 0, 0)
	}
	defer client.CloseIdleConnections()
	return validateAccessToken(
		ctx,
		client,
		authorizer,
		instance,
		credentialvalidator.RequestLabels(input, "credential_validate"),
	)
}

func validateAccessToken(
	ctx context.Context,
	client *providerkit.Client,
	authorizer providerkit.Authorizer,
	instanceBaseURL string,
	labels providerkit.RequestLabels,
) (connector.CredentialValidationResult, error) {
	response, err := client.Do(ctx, providerkit.Request{
		Method:     http.MethodGet,
		URL:        gitLabTokenInfoPath,
		Authorizer: authorizer,
		Headers:    http.Header{"Accept": {"application/json"}},
		Labels:     labels,
	})
	if err != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.ProviderError(err)
	}
	var info struct {
		ResourceOwnerID int64 `json:"resource_owner_id"`
		// Doorkeeper 在不同版本里用 scope 或 scopes 呈现同一个数组，都收下。
		Scope  []string `json:"scope"`
		Scopes []string `json:"scopes"`
	}
	if err := response.DecodeJSON(&info); err != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.ProviderError(err)
	}
	if info.ResourceOwnerID <= 0 {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(connector.FailureInvalidResponse, 0, 0)
	}
	userID := strconv.FormatInt(info.ResourceOwnerID, 10)
	scopes, err := connector.NormalizeScopes(append(info.Scope, info.Scopes...))
	if err != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(connector.FailureInvalidResponse, 0, 0)
	}
	if len(scopes) == 0 {
		scopes = nil
	}
	return connector.CredentialValidationResult{
		Profile: connector.CredentialProfile{
			AccountID:   gitLabScopedAccountID(instanceBaseURL, userID),
			DisplayName: "GitLab user " + userID,
		},
		GrantedScopes: scopes,
		// 自省结果没给出 scope 时（旧实例的响应形状不同）宁可判为未知，
		// 也不要把空集合当成"确实没有任何 scope"去否决 Tool 授权。
		ScopesKnown: scopes != nil,
	}, nil
}

// gitLabInstanceBaseURL derives the instance root from the configured MCP
// endpoint. Requiring the exact /api/v4/mcp suffix is also what keeps a
// mistyped endpoint from being probed with the operator's access token.
func gitLabInstanceBaseURL(config map[string]any) (string, error) {
	raw, err := credentialvalidator.RequiredConfigString(
		config, gitLabMCPURLField,
	)
	if err != nil {
		return "", err
	}
	configurationError := credentialvalidator.Error(
		connector.FailureConfigurationError, 0, 0,
	)
	endpoint, parseErr := providerkit.ParseAndValidateURL(raw)
	if parseErr != nil || endpoint.RawQuery != "" || endpoint.ForceQuery {
		return "", configurationError
	}
	root, found := strings.CutSuffix(
		strings.TrimSuffix(endpoint.EscapedPath(), "/"),
		gitLabMCPPathSuffix,
	)
	if !found {
		return "", configurationError
	}
	escaped := root + "/"
	decoded, unescapeErr := url.PathUnescape(escaped)
	if unescapeErr != nil {
		return "", configurationError
	}
	endpoint.Path = decoded
	endpoint.RawPath = escaped
	normalized, normalizeErr := providerkit.NormalizeBaseURL(endpoint.String())
	if normalizeErr != nil {
		return "", configurationError
	}
	return normalized, nil
}

func gitLabAllowInsecureHTTP(config map[string]any) (string, error) {
	value, err := credentialvalidator.RequiredConfigString(
		config, gitLabAllowHTTPField,
	)
	if err != nil {
		return "", err
	}
	if value != "false" && value != "true" {
		return "", credentialvalidator.Error(
			connector.FailureConfigurationError, 0, 0,
		)
	}
	return value, nil
}

// gitLabScopedAccountID namespaces an upstream user ID by the canonical
// instance base, including a deployment subpath. Hashing prevents instance
// topology from leaking through an otherwise opaque account key.
func gitLabScopedAccountID(instanceBaseURL, userID string) string {
	sum := sha256.Sum256(
		[]byte("connect-it/gitlab-instance/v1\x00" + instanceBaseURL),
	)
	return "gitlab:" + hex.EncodeToString(sum[:]) + ":user:" + userID
}
