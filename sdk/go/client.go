// Package connectit provides the small control-plane client used by trusted Go
// services integrating with Connect-It.
package connectit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxErrorBodyBytes = 1 << 20

// Client calls the Connect-It control-plane API. APIToken is used only for
// /v1 endpoints; MCP requests use short-lived session tokens instead.
type Client struct {
	baseURL    *url.URL
	apiToken   string
	httpClient *http.Client
}

// Option customizes a Client.
type Option func(*Client)

// WithHTTPClient replaces the default 45-second control-plane HTTP client.
// The client is cloned and Connect-It still prevents cross-origin redirects.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// New creates a Connect-It client for a trusted downstream service.
func New(baseURL, apiToken string, options ...Option) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	apiToken = strings.TrimSpace(apiToken)
	if baseURL == "" {
		return nil, errors.New("connectit: base URL is required")
	}
	if apiToken == "" {
		return nil, errors.New("connectit: API token is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("connectit: invalid base URL %q", baseURL)
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""

	client := &Client{
		baseURL:    parsed,
		apiToken:   apiToken,
		httpClient: &http.Client{Timeout: 45 * time.Second},
	}
	for _, option := range options {
		if option != nil {
			option(client)
		}
	}
	client.httpClient = secureHTTPClient(client.httpClient, parsed)
	return client, nil
}

// MCPEndpoint returns the Streamable HTTP endpoint consumed by an MCP client.
func (c *Client) MCPEndpoint() string {
	return c.endpoint("/mcp")
}

// APIError is the error body returned by Connect-It together with its HTTP
// status code.
type APIError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *APIError) Error() string {
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	if e.Code == "" {
		return fmt.Sprintf("connectit: %s (HTTP %d)", message, e.StatusCode)
	}
	return fmt.Sprintf("connectit: %s: %s (HTTP %d)", e.Code, message, e.StatusCode)
}

func (c *Client) doJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		var encoded bytes.Buffer
		if err := json.NewEncoder(&encoded).Encode(input); err != nil {
			return fmt.Errorf("connectit: encode request: %w", err)
		}
		body = &encoded
	}

	request, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil {
		return fmt.Errorf("connectit: create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiToken)
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := c.httpClient.Do(request) //nolint:gosec // G704: operator-configured origin, redirects remain same-origin
	if err != nil {
		return fmt.Errorf("connectit: request failed: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return decodeAPIError(response)
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(response.Body).Decode(output); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("connectit: decode response: %w", err)
	}
	return nil
}

func decodeAPIError(response *http.Response) error {
	data, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
	if err != nil {
		return fmt.Errorf("connectit: read error response: %w", err)
	}
	var payload struct {
		Code    string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &payload)
	if payload.Message == "" {
		payload.Message = strings.TrimSpace(string(data))
	}
	return &APIError{
		StatusCode: response.StatusCode,
		Code:       payload.Code,
		Message:    payload.Message,
	}
}

func secureHTTPClient(source *http.Client, baseURL *url.URL) *http.Client {
	cloned := *source
	previousCheck := cloned.CheckRedirect
	cloned.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if !strings.EqualFold(request.URL.Scheme, baseURL.Scheme) ||
			!strings.EqualFold(request.URL.Host, baseURL.Host) {
			return http.ErrUseLastResponse
		}
		if previousCheck != nil {
			return previousCheck(request, via)
		}
		if len(via) >= 10 {
			return errors.New("connectit: stopped after 10 redirects")
		}
		return nil
	}
	return &cloned
}

func (c *Client) endpoint(suffix string) string {
	endpoint := *c.baseURL
	escapedPath := strings.TrimRight(endpoint.EscapedPath(), "/") + "/" + strings.TrimLeft(suffix, "/")
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		decodedPath = strings.TrimRight(endpoint.Path, "/") + "/" + strings.TrimLeft(suffix, "/")
		escapedPath = ""
	}
	endpoint.Path = decodedPath
	endpoint.RawPath = escapedPath
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	return endpoint.String()
}
