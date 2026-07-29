// Package managedapi provides the shared implementation for providers whose
// official REST API is exposed through one constrained MCP tool.
package managedapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxResponseBytes int64 = 8 << 20

var httpClient = NewHTTPClient(30 * time.Second)

// NewHTTPClient applies the outbound credential policy shared by managed
// providers. In particular, credentials must never follow a redirect.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Value resolves one request value from the connection or provider config.
type Value func(connector.ManagedCall) (string, error)

func Token() Value {
	return func(call connector.ManagedCall) (string, error) {
		if call.AccessToken == "" {
			return "", errors.New("managedapi: credential is empty")
		}
		return call.AccessToken, nil
	}
}

func Credential(field string) Value {
	return func(call connector.ManagedCall) (string, error) {
		value, _ := call.Credential[field].(string)
		if value == "" {
			return "", fmt.Errorf("managedapi: credential field %q is empty", field)
		}
		return value, nil
	}
}

func Config(field string) Value {
	return func(call connector.ManagedCall) (string, error) {
		value, _ := call.Config[field].(string)
		if value == "" {
			return "", fmt.Errorf("managedapi: config field %q is empty", field)
		}
		return value, nil
	}
}

func Literal(value string) Value {
	return func(connector.ManagedCall) (string, error) { return value, nil }
}

func Affix(prefix string, value Value, suffix string) Value {
	return func(call connector.ManagedCall) (string, error) {
		resolved, err := value(call)
		if err != nil {
			return "", err
		}
		return prefix + resolved + suffix, nil
	}
}

// BaseURL resolves the fixed upstream origin and optional API base path.
type BaseURL func(connector.ManagedCall) (*url.URL, error)

func Fixed(raw string) BaseURL {
	parsed, err := parseBaseURL(raw)
	return func(connector.ManagedCall) (*url.URL, error) {
		if err != nil {
			return nil, err
		}
		clone := *parsed
		return &clone, nil
	}
}

// Selected resolves one of a closed set of provider URLs.
func Selected(value Value, choices map[string]string) BaseURL {
	return func(call connector.ManagedCall) (*url.URL, error) {
		key, err := value(call)
		if err != nil {
			return nil, err
		}
		raw, ok := choices[key]
		if !ok {
			return nil, errors.New("managedapi: invalid API region or environment")
		}
		return parseBaseURL(raw)
	}
}

// Subdomain resolves https://{value}.{hostSuffix}{basePath}. The field value
// is restricted to one DNS label and cannot change the trusted suffix.
func Subdomain(value Value, hostSuffix, basePath string) BaseURL {
	return func(call connector.ManagedCall) (*url.URL, error) {
		label, err := value(call)
		if err != nil {
			return nil, err
		}
		if !validDNSLabel(label) {
			return nil, errors.New("managedapi: invalid tenant identifier")
		}
		return parseBaseURL("https://" + label + "." + hostSuffix + basePath)
	}
}

// Hostname resolves an entire host but only beneath the trusted suffix.
func Hostname(value Value, hostSuffix, basePath string) BaseURL {
	return func(call connector.ManagedCall) (*url.URL, error) {
		host, err := value(call)
		if err != nil {
			return nil, err
		}
		host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
		suffix := strings.ToLower(strings.TrimPrefix(hostSuffix, "."))
		if host != suffix && !strings.HasSuffix(host, "."+suffix) {
			return nil, errors.New("managedapi: host is outside the trusted provider domain")
		}
		return parseBaseURL("https://" + host + basePath)
	}
}

func parseBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("managedapi: invalid HTTPS base URL %q", raw)
	}
	return parsed, nil
}

func validDNSLabel(value string) bool {
	if value == "" || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// RequestAuth applies a provider's fixed authentication scheme.
type RequestAuth func(*http.Request, connector.ManagedCall) error

func Headers(values map[string]Value) RequestAuth {
	return func(req *http.Request, call connector.ManagedCall) error {
		for name, source := range values {
			value, err := source(call)
			if err != nil {
				return err
			}
			req.Header.Set(name, value)
		}
		return nil
	}
}

func Bearer() RequestAuth {
	return Headers(map[string]Value{"Authorization": Affix("Bearer ", Token(), "")})
}

func Header(name string) RequestAuth {
	return Headers(map[string]Value{name: Token()})
}

func Authorization(prefix string) RequestAuth {
	return Headers(map[string]Value{"Authorization": Affix(prefix, Token(), "")})
}

func Basic(username, password Value) RequestAuth {
	return func(req *http.Request, call connector.ManagedCall) error {
		user, err := username(call)
		if err != nil {
			return err
		}
		pass, err := password(call)
		if err != nil {
			return err
		}
		req.SetBasicAuth(user, pass)
		return nil
	}
}

func Query(values map[string]Value) RequestAuth {
	return func(req *http.Request, call connector.ManagedCall) error {
		query := req.URL.Query()
		for name, source := range values {
			value, err := source(call)
			if err != nil {
				return err
			}
			query.Set(name, value)
		}
		req.URL.RawQuery = query.Encode()
		return nil
	}
}

func Chain(auth ...RequestAuth) RequestAuth {
	return func(req *http.Request, call connector.ManagedCall) error {
		for _, apply := range auth {
			if apply != nil {
				if err := apply(req, call); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

type Spec struct {
	ProviderName string
	BaseURL      BaseURL
	Auth         RequestAuth
	Headers      map[string]string
	PathPrefix   Value
}

// Implementation returns one real managed tool. It deliberately accepts only
// relative paths and fixed request metadata; callers cannot replace the
// provider origin or its authentication headers.
func Implementation(spec Spec) connector.Managed {
	description := "Call the official " + spec.ProviderName + " REST API. path is a relative API path; connect-it injects authentication and pins the official origin."
	return connector.Managed{Tools: []connector.ManagedTool{{
		Tool: mcp.Tool{
			Name:        "api_request",
			Description: description,
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"method", "path"},
				"properties": map[string]any{
					"method": map[string]any{
						"type": "string",
						"enum": []string{
							http.MethodGet,
							http.MethodPost,
							http.MethodPut,
							http.MethodPatch,
							http.MethodDelete,
						},
						"description": "HTTP method",
					},
					"path": map[string]any{
						"type":        "string",
						"minLength":   1,
						"description": "Relative API path beginning with /",
					},
					"query": map[string]any{
						"type":                 "object",
						"additionalProperties": true,
						"description":          "Query parameters; array values are encoded as repeated parameters",
					},
					"body": map[string]any{
						"description": "JSON or form request body",
					},
					"content_type": map[string]any{
						"type": "string",
						"enum": []string{
							"application/json",
							"application/x-www-form-urlencoded",
						},
						"default":     "application/json",
						"description": "Encoding used for body",
					},
				},
			},
		},
		Handler: handler(spec),
	}}}
}

type requestArguments struct {
	Method      string                     `json:"method"`
	Path        string                     `json:"path"`
	Query       map[string]json.RawMessage `json:"query"`
	Body        json.RawMessage            `json:"body"`
	ContentType string                     `json:"content_type"`
}

func handler(spec Spec) connector.ManagedHandler {
	return func(ctx context.Context, call connector.ManagedCall) (*mcp.CallToolResult, error) {
		if spec.BaseURL == nil {
			return nil, errors.New("managedapi: base URL is not configured")
		}
		var args requestArguments
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			return toolError(http.StatusBadRequest, "arguments must be a JSON object", nil), nil
		}
		if err := validatePath(args.Path); err != nil {
			return toolError(http.StatusBadRequest, err.Error(), nil), nil
		}
		base, err := spec.BaseURL(call)
		if err != nil {
			return nil, err
		}
		requestURL := *base
		prefix := ""
		if spec.PathPrefix != nil {
			prefix, err = spec.PathPrefix(call)
			if err != nil {
				return nil, err
			}
		}
		requestURL.Path = joinURLPath(base.Path, prefix, args.Path)
		if err := addQuery(&requestURL, args.Query); err != nil {
			return toolError(http.StatusBadRequest, err.Error(), nil), nil
		}

		body, err := encodeBody(args.Body, args.ContentType)
		if err != nil {
			return toolError(http.StatusBadRequest, err.Error(), nil), nil
		}
		req, err := http.NewRequestWithContext(ctx, args.Method, requestURL.String(), body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if len(args.Body) > 0 && string(args.Body) != "null" {
			req.Header.Set("Content-Type", args.ContentType)
		}
		for name, value := range spec.Headers {
			req.Header.Set(name, value)
		}
		if spec.Auth != nil {
			if err := spec.Auth(req, call); err != nil {
				return nil, err
			}
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > maxResponseBytes {
			return nil, fmt.Errorf("managedapi: response exceeds %d bytes", maxResponseBytes)
		}
		return responseResult(resp.StatusCode, data), nil
	}
}

func validatePath(value string) error {
	if !strings.HasPrefix(value, "/") || strings.ContainsAny(value, `\?#`) {
		return errors.New("path must be an absolute-path reference beginning with /")
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return errors.New("path must not contain . or .. segments")
		}
	}
	return nil
}

func joinURLPath(parts ...string) string {
	joined := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		joined += "/" + strings.Trim(part, "/")
	}
	if joined == "" {
		return "/"
	}
	// path.Clean removes duplicate separators after all dot segments have
	// already been rejected by validatePath.
	return path.Clean(joined)
}

func addQuery(target *url.URL, raw map[string]json.RawMessage) error {
	query := target.Query()
	for key, encoded := range raw {
		var value any
		if err := json.Unmarshal(encoded, &value); err != nil {
			return fmt.Errorf("query parameter %q is invalid JSON", key)
		}
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				text, err := scalarString(item)
				if err != nil {
					return fmt.Errorf("query parameter %q: %w", key, err)
				}
				query.Add(key, text)
			}
		default:
			text, err := scalarString(typed)
			if err != nil {
				return fmt.Errorf("query parameter %q: %w", key, err)
			}
			query.Set(key, text)
		}
	}
	target.RawQuery = query.Encode()
	return nil
}

func scalarString(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(typed), nil
	case nil:
		return "", nil
	default:
		return "", errors.New("value must be a string, number, boolean, null, or an array of those")
	}
}

func encodeBody(raw json.RawMessage, contentType string) (io.Reader, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	switch contentType {
	case "", "application/json":
		return bytes.NewReader(raw), nil
	case "application/x-www-form-urlencoded":
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return nil, errors.New("form body must be a JSON object")
		}
		form := url.Values{}
		for key, value := range fields {
			text, err := scalarString(value)
			if err != nil {
				return nil, fmt.Errorf("form field %q: %w", key, err)
			}
			form.Set(key, text)
		}
		return strings.NewReader(form.Encode()), nil
	default:
		return nil, errors.New("unsupported content_type")
	}
}

func responseResult(statusCode int, data []byte) *mcp.CallToolResult {
	text := string(data)
	if len(data) == 0 {
		text = http.StatusText(statusCode)
	}
	structured := map[string]any{"status": statusCode}
	var decoded any
	if len(data) > 0 && json.Unmarshal(data, &decoded) == nil {
		structured["body"] = decoded
	} else if len(data) > 0 {
		structured["body"] = text
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: structured,
		IsError:           statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices,
	}
}

func toolError(statusCode int, message string, body any) *mcp.CallToolResult {
	structured := map[string]any{"status": statusCode, "error": message}
	if body != nil {
		structured["body"] = body
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: message}},
		StructuredContent: structured,
		IsError:           true,
	}
}
