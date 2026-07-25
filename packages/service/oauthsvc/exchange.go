package oauthsvc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const maxTokenResponseBytes int64 = 1 << 20

const tokenRequestTimeout = 30 * time.Second

// time.Duration is the representation used when persisting expires_at.
// Reject larger provider values instead of allowing integer multiplication to
// wrap an otherwise non-negative expires_in into the past.
const maxTokenExpiresInSeconds int64 = 9223372036

// TokenEndpointError is a body-free, safe classification of a token endpoint
// failure. RequestUncertain is true when a rotating refresh token may have
// reached the Provider and therefore must not be replayed immediately.
type TokenEndpointError struct {
	FailureCode      connector.FailureCode
	SafeMessage      string
	Temporary        bool
	InvalidGrant     bool
	RequestUncertain bool
	Status           int
	RetryAfter       int
}

func (e *TokenEndpointError) Error() string {
	if e == nil {
		return ""
	}
	if e.SafeMessage != "" {
		return e.SafeMessage
	}
	return string(e.Code())
}

func (e *TokenEndpointError) Code() connector.FailureCode {
	if e == nil || !e.FailureCode.Valid() {
		return connector.FailureInternalError
	}
	return e.FailureCode
}

func (e *TokenEndpointError) UpstreamStatus() int {
	if e == nil || e.Status < 100 || e.Status > 599 {
		return 0
	}
	return e.Status
}

// ToolFailure returns the defensive public projection. Internal transport
// causes and response bodies are never retained by TokenEndpointError.
func (e *TokenEndpointError) ToolFailure() *connector.ToolFailure {
	if e == nil {
		fallback := connector.NormalizeToolFailure(nil)
		return &fallback
	}
	failure, err := connector.NewToolFailure(
		e.Code(),
		e.SafeMessage,
		e.UpstreamStatus(),
		e.RetryAfter,
	)
	if err != nil {
		fallback := connector.NormalizeToolFailure(nil)
		return &fallback
	}
	return failure
}

type standardTokenResponseJSON struct {
	AccessToken  string          `json:"access_token"`
	TokenType    json.RawMessage `json:"token_type"`
	RefreshToken json.RawMessage `json:"refresh_token"`
	ExpiresIn    json.RawMessage `json:"expires_in"`
	Scope        json.RawMessage `json:"scope"`
}

// TokenValue distinguishes an omitted refresh token from an explicitly
// returned value. ScopesKnown=false means the provider omitted the scope
// response member.
type TokenValue struct {
	AccessToken      string
	TokenType        string
	RefreshToken     *string
	ExpiresInSeconds *int64
	Scopes           []string
	ScopesKnown      bool
}

// ExchangeToken requests a token through a fresh policy-bound providerkit
// client. The factory owns DNS/IP/TLS policy; redirects are denied even when
// they remain on the same origin, and the complete response is bounded before
// protocol parsing.
func ExchangeToken(
	ctx context.Context,
	factory *providerkit.Factory,
	provider connector.Type,
	oc *connector.OAuthConfig,
	clientID string,
	clientSecret string,
	coreParams url.Values,
	correlation providerkit.RequestLabels,
) (TokenValue, error) {
	if factory == nil {
		return TokenValue{}, tokenConfigurationError()
	}
	if oc == nil || oc.TokenEndpoint == "" || clientID == "" {
		return TokenValue{}, tokenConfigurationError()
	}
	if (correlation.ConnectionID == "") ==
		(correlation.AuthorizationID == "") {
		return TokenValue{}, tokenConfigurationError()
	}
	isRefresh := coreParams.Get("grant_type") == "refresh_token"
	rawEndpoint := oc.TokenEndpoint
	if isRefresh && oc.RefreshTokenEndpoint != "" {
		rawEndpoint = oc.RefreshTokenEndpoint
	}
	client, endpoint, err := newTokenPolicyClient(
		factory,
		provider,
		oc,
		rawEndpoint,
	)
	if err != nil {
		return TokenValue{}, err
	}
	defer client.CloseIdleConnections()

	outboundParams := mergeTokenParams(oc.ExtraTokenParams, coreParams)
	authorizer := providerkit.NoAuth()
	switch oc.EffectiveTokenEndpointAuth() {
	case connector.TokenAuthBasic:
		// Extra params and callers cannot smuggle a second client
		// authentication mode into the body.
		outboundParams.Del("client_id")
		outboundParams.Del("client_secret")
		// RFC 6749 §2.3.1：Basic 认证的用户名密码须先做 form 编码。
		authorizer, err = providerkit.Basic(
			url.QueryEscape(clientID),
			url.QueryEscape(clientSecret),
		)
		if err != nil {
			return TokenValue{}, tokenConfigurationError()
		}
	case connector.TokenAuthPost:
		outboundParams.Set("client_id", clientID)
		outboundParams.Set("client_secret", clientSecret)
	case connector.TokenAuthNone:
		outboundParams.Set("client_id", clientID)
		outboundParams.Del("client_secret")
	default:
		return TokenValue{}, tokenConfigurationError()
	}
	var requestTrace tokenRequestTrace
	traceContext := httptrace.WithClientTrace(
		ctx,
		&httptrace.ClientTrace{
			DNSDone: func(info httptrace.DNSDoneInfo) {
				if info.Err != nil {
					requestTrace.dnsFailed.Store(true)
				}
			},
			ConnectDone: func(_ string, _ string, err error) {
				if err == nil {
					requestTrace.connectSucceeded.Store(true)
					return
				}
				requestTrace.connectFailed.Store(true)
			},
			TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
				if err != nil {
					requestTrace.tlsFailed.Store(true)
				}
			},
			GotConn: func(httptrace.GotConnInfo) {
				requestTrace.gotConn.Store(true)
			},
			WroteRequest: func(httptrace.WroteRequestInfo) {
				// The callback also runs when writing failed part-way through.
				// Either case is uncertain for a rotating refresh token.
				requestTrace.wroteRequest.Store(true)
			},
		},
	)
	operation := "oauth_token_exchange"
	if isRefresh {
		operation = "oauth_token_refresh"
	}
	request := providerkit.Request{
		Method:     http.MethodPost,
		URL:        endpoint.String(),
		Authorizer: authorizer,
		Headers: http.Header{
			"Accept": []string{"application/json"},
		},
		Labels: providerkit.RequestLabels{
			ConnectorType:   string(provider),
			Operation:       operation,
			ConnectionID:    correlation.ConnectionID,
			AuthorizationID: correlation.AuthorizationID,
		},
	}
	// An unreviewed request format never reaches the Provider: the request is
	// only issued below, after this switch has produced a known body.
	switch oc.EffectiveTokenRequestFormat() {
	case connector.TokenRequestForm:
		request.Form = outboundParams
	case connector.TokenRequestJSON:
		request.JSON = tokenJSONBody(outboundParams)
	default:
		return TokenValue{}, tokenConfigurationError()
	}
	response, requestErr := client.Do(traceContext, request)
	if response == nil {
		return TokenValue{}, classifyGuardedTokenFailure(
			requestErr,
			&requestTrace,
		)
	}
	if response.StatusCode != http.StatusOK {
		return TokenValue{}, classifyTokenEndpointFailure(
			response.StatusCode,
			response.Header,
			response.Body,
		)
	}
	if requestErr != nil {
		return TokenValue{}, classifyGuardedTokenFailure(
			requestErr,
			&requestTrace,
		)
	}
	token, err := parseStandardTokenResponse(response.Body, oc)
	if err != nil {
		return TokenValue{}, invalidTokenResponseError(
			response.StatusCode,
		)
	}
	return token, nil
}

func invalidTokenResponseError(status int) *TokenEndpointError {
	return &TokenEndpointError{
		FailureCode:      connector.FailureInvalidResponse,
		SafeMessage:      "token provider returned an invalid response",
		RequestUncertain: true,
		Status:           status,
	}
}

func classifyTokenEndpointFailure(
	status int,
	header http.Header,
	body []byte,
) error {
	var envelope struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	retryAfter := 0
	if delay, ok := providerkit.ParseRetryAfter(
		header.Get("Retry-After"),
		time.Now(),
	); ok && delay > 0 {
		retryAfter = int((delay + time.Second - 1) / time.Second)
	}
	classified := &TokenEndpointError{
		FailureCode:      connector.FailureProviderError,
		SafeMessage:      "token provider rejected the request",
		RequestUncertain: true,
		Status:           status,
		RetryAfter:       retryAfter,
	}
	switch {
	case envelope.Error == "invalid_grant":
		classified.FailureCode = connector.FailureAuthorizationFailed
		classified.SafeMessage = "authorization grant is no longer valid"
		classified.InvalidGrant = true
		classified.RequestUncertain = false
	case status == http.StatusUnauthorized:
		classified.FailureCode = connector.FailureAuthorizationFailed
		classified.SafeMessage = "token provider rejected client authentication"
	case status == http.StatusTooManyRequests:
		classified.FailureCode = connector.FailureRateLimited
		classified.SafeMessage = "token provider rate limit was reached"
		classified.Temporary = true
	case status >= http.StatusInternalServerError:
		classified.FailureCode = connector.FailureUpstreamUnavailable
		classified.SafeMessage = "token provider is temporarily unavailable"
		classified.Temporary = true
	case status >= http.StatusBadRequest &&
		status < http.StatusInternalServerError:
		// Keep the generic Provider rejection. A complete HTTP error proves
		// only what response was returned, not that a rotating refresh token
		// was left unconsumed.
	default:
		classified.FailureCode = connector.FailureInvalidResponse
		classified.SafeMessage = "token provider returned an unexpected status"
	}
	return classified
}

func classifyGuardedTokenFailure(
	err error,
	trace *tokenRequestTrace,
) error {
	if err == nil {
		return &TokenEndpointError{
			FailureCode:      connector.FailureInvalidResponse,
			SafeMessage:      "token provider returned an invalid response",
			RequestUncertain: trace != nil && trace.wroteRequest.Load(),
		}
	}
	failure := providerkit.AsToolFailure(err)
	temporary := false
	switch failure.Code {
	case connector.FailureUpstreamUnavailable,
		connector.FailureTimeout,
		connector.FailureRateLimited:
		temporary = true
	}
	uncertain := trace != nil && !trace.definitelyNotSent(err)
	switch failure.Code {
	case connector.FailureConfigurationError,
		connector.FailureInvalidInput:
		uncertain = false
	case connector.FailurePolicyDenied:
		// Policy can reject either during local URL/request validation or after
		// a Provider response attempted a forbidden redirect.
		uncertain = trace != nil && trace.wroteRequest.Load()
	}
	return &TokenEndpointError{
		FailureCode:      failure.Code,
		SafeMessage:      failure.Message,
		Temporary:        temporary,
		RequestUncertain: uncertain,
		Status:           failure.UpstreamStatus,
		RetryAfter:       failure.RetryAfterSeconds,
	}
}

func tokenConfigurationError() *TokenEndpointError {
	return &TokenEndpointError{
		FailureCode: connector.FailureConfigurationError,
		SafeMessage: "OAuth token provider is not configured correctly",
	}
}

func tokenPolicyError() *TokenEndpointError {
	return &TokenEndpointError{
		FailureCode: connector.FailurePolicyDenied,
		SafeMessage: "OAuth token endpoint was denied by policy",
	}
}

func mergeTokenParams(
	extra map[string]string,
	core url.Values,
) url.Values {
	out := make(url.Values, len(extra)+len(core))
	for key, value := range extra {
		out.Set(key, value)
	}
	// Core protocol parameters are always written last. Registry rejects
	// reserved keys in ExtraTokenParams, and this ordering is a second local
	// guarantee that exact-key extras cannot replace code, verifier or grant.
	for key, values := range core {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func tokenJSONBody(values url.Values) map[string]any {
	out := make(map[string]any, len(values))
	for key, items := range values {
		switch len(items) {
		case 0:
			out[key] = []string{}
		case 1:
			out[key] = items[0]
		default:
			out[key] = append([]string(nil), items...)
		}
	}
	return out
}

// tokenRequestTrace records only ordering evidence needed by rotating-token
// replay safety. A completed request write always wins over earlier connection
// failures (for example, a redirect whose second dial fails).
type tokenRequestTrace struct {
	dnsFailed        atomic.Bool
	connectFailed    atomic.Bool
	connectSucceeded atomic.Bool
	tlsFailed        atomic.Bool
	gotConn          atomic.Bool
	wroteRequest     atomic.Bool
}

// definitelyNotSent recognizes failures that the standard HTTP transport can
// prove happened before the OAuth request was eligible to write. Unknown
// transports and failures after GotConn remain uncertain by default.
func (trace *tokenRequestTrace) definitelyNotSent(err error) bool {
	if trace == nil || trace.wroteRequest.Load() || trace.gotConn.Load() {
		return false
	}
	if trace.tlsFailed.Load() {
		// TLSHandshakeDone(err) occurs before GotConn, including after a
		// successful HTTP proxy CONNECT tunnel. OAuth headers/body were not sent.
		return true
	}
	if trace.dnsFailed.Load() &&
		!trace.connectSucceeded.Load() {
		return true
	}
	if trace.connectFailed.Load() ||
		trace.connectSucceeded.Load() {
		// ConnectDone precedes GotConn. If RoundTrip returned without GotConn,
		// the connection never became eligible to carry the OAuth request.
		// This also covers a rejected HTTP proxy CONNECT tunnel.
		return true
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return true
	}
	var operationError *net.OpError
	if !errors.As(err, &operationError) {
		return false
	}
	switch operationError.Op {
	case "dial", "lookup":
		return true
	case "proxyconnect":
		// net/http emits proxyconnect only while establishing the tunnel,
		// before GotConn and before the OAuth request is written.
		return true
	default:
		return false
	}
}

func parseStandardTokenResponse(
	body []byte,
	oc *connector.OAuthConfig,
) (TokenValue, error) {
	var raw standardTokenResponseJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return TokenValue{}, fmt.Errorf(
			"oauthsvc: token 响应不是 JSON: %w",
			err,
		)
	}
	if raw.AccessToken == "" {
		return TokenValue{}, errors.New(
			"oauthsvc: token 响应缺少 access_token",
		)
	}
	tokenType, err := parseBearerTokenType(raw.TokenType)
	if err != nil {
		return TokenValue{}, err
	}
	refreshToken, err := parseOptionalNonEmptyString(
		raw.RefreshToken,
		"refresh_token",
	)
	if err != nil {
		return TokenValue{}, err
	}
	expiresIn, err := parseOptionalNonNegativeInt64(
		raw.ExpiresIn,
		"expires_in",
	)
	if err != nil {
		return TokenValue{}, err
	}
	if expiresIn != nil && *expiresIn > maxTokenExpiresInSeconds {
		return TokenValue{}, errors.New(
			"oauthsvc: token 响应 expires_in 超出支持范围",
		)
	}
	tok := TokenValue{
		AccessToken:      raw.AccessToken,
		TokenType:        tokenType,
		RefreshToken:     refreshToken,
		ExpiresInSeconds: expiresIn,
	}
	if len(raw.Scope) == 0 {
		return tok, nil
	}
	if oc == nil {
		return TokenValue{}, errors.New(
			"oauthsvc: 缺少 OAuth 配置",
		)
	}

	var scopeValue any
	if err := json.Unmarshal(raw.Scope, &scopeValue); err != nil {
		return TokenValue{}, errors.New(
			"oauthsvc: token 响应 scope 必须是字符串",
		)
	}
	scope, ok := scopeValue.(string)
	if !ok {
		return TokenValue{}, errors.New(
			"oauthsvc: token 响应 scope 必须是字符串",
		)
	}
	tok.Scopes, err = parseTokenScopes(scope, oc.EffectiveTokenScopeSeparator())
	if err != nil {
		return TokenValue{}, err
	}
	tok.ScopesKnown = true
	return tok, nil
}

func parseOptionalNonEmptyString(
	raw json.RawMessage,
	field string,
) (*string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return nil, fmt.Errorf(
			"oauthsvc: token 响应 %s 必须是非空字符串",
			field,
		)
	}
	return &value, nil
}

func parseOptionalNonNegativeInt64(
	raw json.RawMessage,
	field string,
) (*int64, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(string(raw)) == "null" {
		return nil, fmt.Errorf(
			"oauthsvc: token 响应 %s 必须是非负整数",
			field,
		)
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil || value < 0 {
		return nil, fmt.Errorf(
			"oauthsvc: token 响应 %s 必须是非负整数",
			field,
		)
	}
	return &value, nil
}

func parseBearerTokenType(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("oauthsvc: token 响应缺少 token_type")
	}
	var tokenType string
	if err := json.Unmarshal(raw, &tokenType); err != nil || tokenType == "" {
		return "", errors.New("oauthsvc: token 响应 token_type 必须是非空字符串")
	}
	if !strings.EqualFold(tokenType, "Bearer") {
		return "", errors.New("oauthsvc: token 响应只支持 Bearer token_type")
	}
	return "Bearer", nil
}

func parseTokenScopes(scope string, separator connector.OAuthScopeSeparator) ([]string, error) {
	var values []string
	switch separator {
	case connector.OAuthScopeSpace:
		values = strings.Fields(scope)
	case connector.OAuthScopeComma:
		values = strings.Split(scope, ",")
	default:
		return nil, fmt.Errorf("oauthsvc: 非法 TokenScopeSeparator %q", separator)
	}
	return connector.NormalizeScopes(values)
}

// initialGrantedScopes 返回标准 OAuth 响应确立的初始授权事实。RFC 6749 规定
// 省略 scope 等同于请求的 scope，因此这一步永远得到已知的 scope 集合。
func initialGrantedScopes(
	tok TokenValue,
	requested []string,
) ([]string, error) {
	if tok.ScopesKnown {
		return connector.NormalizeScopes(tok.Scopes)
	}
	return connector.NormalizeScopes(requested)
}
