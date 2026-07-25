package providerkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"slices"
	"strings"

	"github.com/memohai/connect-it/packages/core/connector"
)

// Request 是一次 Provider HTTP 调用的不可变描述。URL 可以是绝对 URL，也可以是
// 相对 Client Policy.BaseURL 的路径；三种 body 形式最多设置一种。
type Request struct {
	Method  string
	URL     string
	Query   url.Values
	Headers http.Header

	JSON any
	Form url.Values
	Body []byte

	ContentType string
	Authorizer  Authorizer
	Labels      RequestLabels

	// 非幂等写操作只有同时提供 header 名和值时才允许 retry。Request body 在 build
	// 后是固定 bytes，因此满足安全重放条件。
	IdempotencyKeyHeader string
	IdempotencyKey       string
}

type builtRequest struct {
	request    *http.Request
	body       []byte
	retryWrite bool
	footprint  CredentialFootprint
}

var blockedRequestHeaders = map[string]struct{}{
	"Authorization":       {},
	"Content-Type":        {},
	"Content-Length":      {},
	"Cookie":              {},
	"Cookie2":             {},
	"Host":                {},
	"Proxy-Authorization": {},
	"Referer":             {},
	// 保持为空时 net/http 才会自动解压 gzip，ReadResponse 统计解压后字节。
	"Accept-Encoding": {},
}

const (
	maxAuthorizerBodyBytes = 32 << 20
	maxIdempotencyKeyBytes = 8 << 10
)

func buildRequest(ctx context.Context, baseURL string, input Request) (builtRequest, error) {
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete:
	default:
		return builtRequest{}, newRequestError("不支持的 HTTP method")
	}

	target, err := resolveRequestURL(baseURL, input.URL)
	if err != nil {
		return builtRequest{}, err
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return builtRequest{}, newRequestError("Provider URL query 非法")
	}
	for key, values := range input.Query {
		if !validQueryName(key) || len(values) != 1 {
			return builtRequest{}, newRequestError("request query 必须是 scalar")
		}
		query.Set(key, values[0])
	}
	target.RawQuery = query.Encode()
	target, err = ParseAndValidateURL(target.String())
	if err != nil {
		return builtRequest{}, newRequestError("Provider URL 非法")
	}

	body, contentType, err := encodeRequestBody(input)
	if err != nil {
		return builtRequest{}, err
	}
	if (method == http.MethodGet || method == http.MethodHead) && len(body) > 0 {
		return builtRequest{}, newRequestError("GET/HEAD 不允许 request body")
	}

	retryWrite, err := validateIdempotency(input, method)
	if err != nil {
		return builtRequest{}, err
	}

	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return builtRequest{}, newRequestError("创建 Provider request 失败")
	}
	for key, values := range input.Headers {
		trimmed := strings.TrimSpace(key)
		canonical := http.CanonicalHeaderKey(trimmed)
		if canonical == "" || key != trimmed || !validHeaderName(canonical) {
			return builtRequest{}, newRequestError("request header 名不能为空")
		}
		if _, blocked := blockedRequestHeaders[canonical]; blocked {
			return builtRequest{}, newRequestError("request header 由 providerkit 管理")
		}
		for _, value := range values {
			if !validHeaderValue(value) {
				return builtRequest{}, newRequestError("request header value 非法")
			}
			req.Header.Add(canonical, value)
		}
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if retryWrite {
		req.Header.Set(input.IdempotencyKeyHeader, input.IdempotencyKey)
	}
	if input.Authorizer == nil {
		input.Authorizer = NoAuth()
	}
	footprint, err := authorizerFootprint(input.Authorizer)
	if err != nil {
		return builtRequest{}, err
	}
	idempotencyHeader := http.CanonicalHeaderKey(
		strings.TrimSpace(input.IdempotencyKeyHeader),
	)
	if retryWrite {
		for _, name := range footprint.HeaderNames {
			if strings.EqualFold(name, idempotencyHeader) {
				return builtRequest{}, knownError(
					connector.FailureConfigurationError,
					defaultSafeMessage(
						connector.FailureConfigurationError,
					),
				)
			}
		}
	}
	effectiveBody, err := applyAuthorizer(
		req,
		input.Authorizer,
		footprint,
		&bufferedAuthorizerBody{original: body},
	)
	if err != nil {
		return builtRequest{}, err
	}
	if retryWrite {
		values := req.Header.Values(idempotencyHeader)
		if len(values) != 1 || values[0] != input.IdempotencyKey {
			return builtRequest{}, knownError(
				connector.FailureConfigurationError,
				defaultSafeMessage(connector.FailureConfigurationError),
			)
		}
	}
	return builtRequest{
		request: req,
		body:    effectiveBody,
		// A body-mutating Authorizer may be stateful (for example, include a
		// timestamp or nonce). Rebuilding it on a later attempt cannot prove
		// byte-for-byte replay, so an idempotency key alone is insufficient.
		retryWrite: retryWrite && !footprint.Body,
		footprint:  footprint,
	}, nil
}

func resolveRequestURL(baseURL, target string) (*url.URL, error) {
	base, err := ParseAndValidateURL(baseURL)
	if err != nil {
		return nil, newRequestError("Policy BaseURL 非法")
	}
	if len(target) > maxProviderURLBytes {
		return nil, newRequestError("Provider URL 过长")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		clone := *base
		return &clone, nil
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, newRequestError("Provider URL 非法")
	}
	if parsed.IsAbs() {
		validated, validateErr := ParseAndValidateURL(parsed.String())
		if validateErr != nil {
			return nil, newRequestError("Provider URL 非法")
		}
		return validated, nil
	}
	if parsed.Host != "" || strings.HasPrefix(target, "//") {
		return nil, newRequestError("相对 Provider URL 不得改变 host")
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return nil, newRequestError("相对 Provider URL 不得包含 fragment")
	}
	for _, segment := range strings.Split(parsed.EscapedPath(), "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "." || decoded == ".." {
			return nil, newRequestError("相对 Provider URL 不得包含路径穿越")
		}
	}

	clone := *base
	basePath := clone.EscapedPath()
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	joined := path.Clean(basePath + parsed.EscapedPath())
	if basePath != "/" && joined != strings.TrimSuffix(basePath, "/") &&
		!strings.HasPrefix(joined, basePath) {
		return nil, newRequestError("相对 Provider URL 超出 BaseURL path")
	}
	unescaped, err := url.PathUnescape(joined)
	if err != nil {
		return nil, newRequestError("相对 Provider URL path 非法")
	}
	clone.Path = unescaped
	clone.RawPath = joined
	clone.RawQuery = parsed.RawQuery
	clone.Fragment = ""
	validated, err := ParseAndValidateURL(clone.String())
	if err != nil {
		return nil, newRequestError("相对 Provider URL 非法")
	}
	return validated, nil
}

func encodeRequestBody(input Request) ([]byte, string, error) {
	count := 0
	if input.JSON != nil {
		count++
	}
	if input.Form != nil {
		count++
	}
	if input.Body != nil {
		count++
	}
	if count > 1 {
		return nil, "", newRequestError("JSON、Form、Body 只能设置一种")
	}
	switch {
	case input.JSON != nil:
		body, err := json.Marshal(input.JSON)
		if err != nil {
			return nil, "", newRequestError("JSON request body 无法编码")
		}
		return body, "application/json", nil
	case input.Form != nil:
		return []byte(input.Form.Encode()), "application/x-www-form-urlencoded", nil
	case input.Body != nil:
		contentType := strings.TrimSpace(input.ContentType)
		if contentType == "" {
			return nil, "", newRequestError("raw body 必须声明 ContentType")
		}
		return append([]byte(nil), input.Body...), contentType, nil
	default:
		if strings.TrimSpace(input.ContentType) != "" {
			return nil, "", newRequestError("没有 body 时不能声明 ContentType")
		}
		return nil, "", nil
	}
}

// freezeRequestBody encodes or copies caller-owned body input exactly once per
// logical request. Automatic write retries may rebuild headers/signatures, but
// every attempt must replay the same bytes.
func freezeRequestBody(input Request) (Request, error) {
	body, contentType, err := encodeRequestBody(input)
	if err != nil {
		return Request{}, err
	}
	input.Query = cloneURLValues(input.Query)
	input.Headers = input.Headers.Clone()
	if input.JSON == nil && input.Form == nil && input.Body == nil {
		return input, nil
	}
	input.JSON = nil
	input.Form = nil
	// Starting from a non-nil empty slice preserves an explicitly empty body.
	input.Body = append([]byte{}, body...)
	input.ContentType = contentType
	return input, nil
}

func cloneURLValues(input url.Values) url.Values {
	if input == nil {
		return nil
	}
	out := make(url.Values, len(input))
	for name, values := range input {
		out[name] = append([]string(nil), values...)
	}
	return out
}

func validateIdempotency(input Request, method string) (bool, error) {
	header := http.CanonicalHeaderKey(strings.TrimSpace(input.IdempotencyKeyHeader))
	key := strings.TrimSpace(input.IdempotencyKey)
	if input.IdempotencyKeyHeader !=
		strings.TrimSpace(input.IdempotencyKeyHeader) ||
		input.IdempotencyKey != key {
		return false, newRequestError("idempotency header 和 key 不得包含首尾空白")
	}
	if (header == "") != (key == "") {
		return false, newRequestError("idempotency header 和 key 必须同时设置")
	}
	if header == "" {
		return false, nil
	}
	if !validHeaderName(header) {
		return false, newRequestError("idempotency header 非法")
	}
	if _, blocked := blockedRequestHeaders[header]; blocked {
		return false, newRequestError("idempotency header 不能使用敏感 header")
	}
	if len(key) > maxIdempotencyKeyBytes || !validCredentialValue(key) {
		return false, newRequestError("idempotency key 非法")
	}
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true, nil
	default:
		return false, newRequestError("GET/HEAD 不需要 idempotency key")
	}
}

// PathSegment 对单个 path segment 做编码。调用方必须逐段编码，不能把完整路径传入。
func PathSegment(value string) string {
	return url.PathEscape(value)
}

func newRequestError(message string) error {
	err, createErr := NewError(connector.FailureInvalidInput, message)
	if createErr != nil {
		return fmt.Errorf("providerkit request: %s", message)
	}
	return err
}

// bufferedAuthorizerBody is non-nil only for Client.Do requests. Streaming
// protocol requests pass nil so their body and replay function remain owned by
// the protocol client. Body mutation is allowed only when the Authorizer also
// declares it in its credential footprint.
type bufferedAuthorizerBody struct {
	original []byte
}

// authorizerMutationFence captures every request field an Authorizer does not
// own. Header values and RawQuery may change only inside the declared
// CredentialFootprint; buffered bodies are the sole optional exception.
type authorizerMutationFence struct {
	method           string
	host             string
	context          context.Context
	url              url.URL
	header           http.Header
	contentLength    int64
	transferEncoding []string
	body             io.ReadCloser
	getBody          func() (io.ReadCloser, error)
	trailer          http.Header
	close            bool
	requestURI       string
	cancel           <-chan struct{}
	response         *http.Response
}

func captureAuthorizerMutationFence(
	request *http.Request,
) authorizerMutationFence {
	return authorizerMutationFence{
		method:           request.Method,
		host:             request.Host,
		context:          request.Context(),
		url:              *request.URL,
		header:           request.Header.Clone(),
		contentLength:    request.ContentLength,
		transferEncoding: slices.Clone(request.TransferEncoding),
		body:             request.Body,
		getBody:          request.GetBody,
		trailer:          request.Trailer.Clone(),
		close:            request.Close,
		requestURI:       request.RequestURI,
		cancel:           request.Cancel,
		response:         request.Response,
	}
}

func (fence authorizerMutationFence) accepts(
	request *http.Request,
	bodyMutable bool,
) bool {
	bodyUnchanged := sameRequestBody(fence.body, request.Body)
	if bodyMutable {
		bodyUnchanged = hasRequestBody(fence.body) ==
			hasRequestBody(request.Body)
	}
	return request.URL != nil &&
		request.Method == fence.method &&
		request.Host == fence.host &&
		sameRequestContext(fence.context, request.Context()) &&
		sameURLExceptQuery(fence.url, request.URL) &&
		(bodyMutable || request.ContentLength == fence.contentLength) &&
		slices.Equal(request.TransferEncoding, fence.transferEncoding) &&
		bodyUnchanged &&
		(request.GetBody != nil) == (fence.getBody != nil) &&
		maps.EqualFunc(request.Trailer, fence.trailer, slices.Equal) &&
		request.Close == fence.close &&
		request.RequestURI == fence.requestURI &&
		request.Cancel == fence.cancel &&
		request.Response == fence.response
}

func applyAuthorizer(
	request *http.Request,
	authorizer Authorizer,
	footprint CredentialFootprint,
	bufferedBody *bufferedAuthorizerBody,
) (effectiveBody []byte, err error) {
	fence := captureAuthorizerMutationFence(request)
	bodyMutable := bufferedBody != nil && footprint.Body
	if bufferedBody == nil && footprint.Body {
		return nil, authorizerConfigurationError(nil)
	}

	defer func() {
		if recover() != nil {
			effectiveBody = nil
			err = authorizerConfigurationError(nil)
		}
	}()
	if applyErr := authorizer.Apply(request); applyErr != nil {
		return nil, authorizerConfigurationError(applyErr)
	}
	normalizedHeaders, normalizeErr := normalizeRequestHeaders(
		request.Header,
	)
	if normalizeErr != nil {
		return nil, authorizerConfigurationError(normalizeErr)
	}
	request.Header = normalizedHeaders

	if !fence.accepts(request, bodyMutable) {
		return nil, authorizerConfigurationError(nil)
	}

	declaredHeaders := make(map[string]struct{}, len(footprint.HeaderNames))
	for _, name := range footprint.HeaderNames {
		declaredHeaders[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	for _, name := range changedHeaderNames(fence.header, request.Header) {
		if _, declared := declaredHeaders[name]; !declared {
			return nil, authorizerConfigurationError(nil)
		}
	}

	beforeQuery, parseErr := url.ParseQuery(fence.url.RawQuery)
	if parseErr != nil {
		return nil, authorizerConfigurationError(parseErr)
	}
	afterQuery, parseErr := url.ParseQuery(request.URL.RawQuery)
	if parseErr != nil {
		return nil, authorizerConfigurationError(parseErr)
	}
	declaredQuery := make(map[string]struct{}, len(footprint.QueryParamNames))
	for _, name := range footprint.QueryParamNames {
		declaredQuery[name] = struct{}{}
	}
	for _, name := range changedQueryNames(beforeQuery, afterQuery) {
		if _, declared := declaredQuery[name]; !declared {
			return nil, authorizerConfigurationError(nil)
		}
	}

	if hasHeaderFold(request.Header, "Referer") {
		return nil, authorizerConfigurationError(nil)
	}
	validatedURL, validateErr := ParseAndValidateURL(request.URL.String())
	if validateErr != nil {
		return nil, authorizerConfigurationError(validateErr)
	}
	request.URL = validatedURL

	// Function values cannot be compared. Restore the exact replay function so
	// an Authorizer cannot replace bytes used by a later 307/308 redirect.
	request.GetBody = fence.getBody
	if bufferedBody == nil {
		return nil, nil
	}

	effectiveBody = slices.Clone(bufferedBody.original)
	if bodyMutable && hasRequestBody(request.Body) {
		limit := max(int64(maxAuthorizerBodyBytes), int64(len(effectiveBody)))
		effectiveBody, err = io.ReadAll(io.LimitReader(request.Body, limit+1))
		if err != nil || int64(len(effectiveBody)) > limit {
			return nil, authorizerConfigurationError(err)
		}
	}
	resetRequestBody(request, effectiveBody)
	return effectiveBody, nil
}

func authorizerConfigurationError(cause error) error {
	options := make([]ErrorOption, 0, 1)
	if cause != nil {
		options = append(options, WithCause(cause))
	}
	return knownError(
		connector.FailureConfigurationError,
		defaultSafeMessage(connector.FailureConfigurationError),
		options...,
	)
}

func hasRequestBody(body io.ReadCloser) bool {
	return body != nil && body != http.NoBody
}

func sameURLExceptQuery(before url.URL, after *url.URL) bool {
	if after == nil {
		return false
	}
	candidate := *after
	candidate.RawQuery = before.RawQuery
	return candidate == before
}

func changedHeaderNames(before, after http.Header) []string {
	names := make(map[string]struct{}, len(before)+len(after))
	for name := range before {
		names[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	for name := range after {
		names[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	changed := make([]string, 0, len(names))
	for name := range names {
		if !slices.Equal(before.Values(name), after.Values(name)) {
			changed = append(changed, name)
		}
	}
	return changed
}

func normalizeRequestHeaders(input http.Header) (http.Header, error) {
	normalized := make(http.Header, len(input))
	for rawName, values := range input {
		name := http.CanonicalHeaderKey(rawName)
		if !validHeaderName(rawName) || name == "" {
			return nil, errors.New("providerkit: invalid request header")
		}
		if _, exists := normalized[name]; exists {
			return nil, errors.New("providerkit: duplicate request header")
		}
		for _, value := range values {
			if !validHeaderValue(value) {
				return nil, errors.New("providerkit: invalid request header")
			}
			normalized[name] = append(normalized[name], value)
		}
	}
	return normalized, nil
}

func changedQueryNames(before, after url.Values) []string {
	names := make(map[string]struct{}, len(before)+len(after))
	for name := range before {
		names[name] = struct{}{}
	}
	for name := range after {
		names[name] = struct{}{}
	}
	changed := make([]string, 0, len(names))
	for name := range names {
		if !slices.Equal(before[name], after[name]) {
			changed = append(changed, name)
		}
	}
	return changed
}

func sameRequestBody(before, after io.ReadCloser) bool {
	if before == nil || before == http.NoBody {
		return after == nil || after == http.NoBody
	}
	if after == nil || after == http.NoBody {
		return false
	}
	beforeType := reflect.TypeOf(before)
	if beforeType != reflect.TypeOf(after) || !beforeType.Comparable() {
		return false
	}
	return reflect.ValueOf(before).Interface() ==
		reflect.ValueOf(after).Interface()
}

func sameRequestContext(before, after context.Context) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	beforeType := reflect.TypeOf(before)
	if beforeType != reflect.TypeOf(after) || !beforeType.Comparable() {
		return false
	}
	return reflect.ValueOf(before).Interface() ==
		reflect.ValueOf(after).Interface()
}

func resetRequestBody(request *http.Request, body []byte) {
	if len(body) == 0 {
		request.Body = nil
		request.GetBody = nil
		request.ContentLength = 0
		return
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	request.ContentLength = int64(len(body))
}
