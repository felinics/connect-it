package notion

import (
	"context"
	"net/http"
	"net/url"

	"github.com/memohai/connect-it/packages/connectors/internal/credentialvalidator"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	notionAuthMethod = "oauth"

	// mcp.notion.com 同时是 resource server 与 authorization server，
	// introspection（RFC 7662）是它唯一公开、可用来判定 access token 是否有效的
	// 端点：api.notion.com 的公开 integration API 不签发也不认这套 token。
	notionOAuthBaseURL      = "https://mcp.notion.com/"
	notionIntrospectionPath = "/introspect"
)

// NewCredentialValidators constructs Notion validators from the injected,
// policy-bound Provider client factory.
func NewCredentialValidators(
	factory *providerkit.Factory,
) (map[string]connector.CredentialValidator, error) {
	client, err := factory.NewStaticClient(providerkit.Policy{
		Provider:         string(Definition.Type),
		BaseURL:          notionOAuthBaseURL,
		AllowedOrigins:   []string{notionOAuthBaseURL},
		RedirectMode:     providerkit.RedirectDenyAll,
		NetworkMode:      providerkit.PublicOnly,
		RequestTimeout:   providerkit.DefaultRequestTimeout,
		MaxResponseBytes: 64 << 10,
		Retry:            providerkit.RetryPolicy{Disabled: true},
	})
	if err != nil {
		return nil, err
	}
	return newCredentialValidators(client), nil
}

func newCredentialValidators(
	client *providerkit.Client,
) map[string]connector.CredentialValidator {
	return map[string]connector.CredentialValidator{
		notionAuthMethod: func(
			ctx context.Context,
			input connector.CredentialValidationInput,
		) (connector.CredentialValidationResult, error) {
			token, err := credentialvalidator.OAuthInput(
				input,
				Definition.Type,
				notionAuthMethod,
			)
			if err != nil {
				return connector.CredentialValidationResult{}, err
			}
			clientID, err := credentialvalidator.RequiredConfigString(
				input.Config,
				"client_id",
			)
			if err != nil {
				return connector.CredentialValidationResult{}, err
			}
			clientSecret, err := credentialvalidator.RequiredConfigString(
				input.Config,
				"client_secret",
			)
			if err != nil {
				return connector.CredentialValidationResult{}, err
			}
			return validateCredential(
				ctx,
				client,
				clientID,
				clientSecret,
				token,
				credentialvalidator.RequestLabels(
					input,
					"credential_validate",
				),
			)
		},
	}
}

type notionIntrospection struct {
	Active   *bool  `json:"active"`
	Subject  string `json:"sub"`
	Username string `json:"username"`
}

func validateCredential(
	ctx context.Context,
	client *providerkit.Client,
	clientID string,
	clientSecret string,
	token string,
	labels providerkit.RequestLabels,
) (connector.CredentialValidationResult, error) {
	// introspection 用注册客户端自身认证（client_secret_basic）。被验证的
	// access token 按 RFC 7662 只能放在 form body，没有 header 形式；重定向
	// 一律拒绝，因此 body 里的 token 不会被跟随到另一个 origin。
	authorizer, err := providerkit.Basic(clientID, clientSecret)
	if err != nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(
				connector.FailureConfigurationError,
				0,
				0,
			)
	}
	return credentialvalidator.Run(
		ctx,
		client,
		credentialvalidator.Probe[notionIntrospection]{
			Method:  http.MethodPost,
			Path:    notionIntrospectionPath,
			Headers: http.Header{"Accept": {"application/json"}},
			Form: url.Values{
				"token":           {token},
				"token_type_hint": {"access_token"},
			},
			Authorizer: authorizer,
			Project:    projectNotionIntrospection,
		},
		labels,
	)
}

func projectNotionIntrospection(
	payload notionIntrospection,
) (connector.CredentialValidationResult, error) {
	if payload.Active == nil {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(connector.FailureInvalidResponse, 0, 0)
	}
	if !*payload.Active {
		return connector.CredentialValidationResult{},
			credentialvalidator.Error(
				connector.FailureAuthorizationFailed,
				0,
				0,
			)
	}
	displayName := credentialvalidator.OptionalProviderString(payload.Username)
	if displayName == "" {
		displayName = "Notion workspace"
	}
	// sub 与 username 都是 RFC 7662 的可选字段，Notion 未公开 introspection 的
	// 响应契约。缺失时留空而不是伪造身份；active 才是这次验证的判据。
	return connector.CredentialValidationResult{
		Profile: connector.CredentialProfile{
			AccountID:   credentialvalidator.OptionalProviderString(payload.Subject),
			DisplayName: displayName,
		},
		// Notion 的 authorization server 只公布单一 "default" scope，Tool 也不
		// 声明 RequiredScopes；实际可用范围由 workspace 的套餐与授权页勾选决定，
		// introspection 的 scope 不足以当授权判据。
		ScopesKnown: false,
	}, nil
}
