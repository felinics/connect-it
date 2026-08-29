package googleads

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/felinics/connect-it/packages/connectors/internal/managedapi"
	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxAPIResponseBytes int64 = 16 << 20

var (
	apiBaseURL    = "https://googleads.googleapis.com/v25"
	apiHTTPClient = managedapi.NewHTTPClient(60 * time.Second)
)

func listAccessibleCustomers(ctx context.Context, call connector.ManagedCall) (*mcp.CallToolResult, error) {
	return callGoogleAds(ctx, http.MethodGet,
		apiBaseURL+"/customers:listAccessibleCustomers", nil, "", call)
}

func search(ctx context.Context, call connector.ManagedCall) (*mcp.CallToolResult, error) {
	var args struct {
		CustomerID      string `json:"customer_id"`
		Query           string `json:"query"`
		PageToken       string `json:"page_token"`
		LoginCustomerID string `json:"login_customer_id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return googleAdsToolError("arguments must be a JSON object"), nil
	}
	customerID, ok := normalizeCustomerID(args.CustomerID)
	if !ok {
		return googleAdsToolError("customer_id must be 10 digits"), nil
	}
	if strings.TrimSpace(args.Query) == "" {
		return googleAdsToolError("query must not be empty"), nil
	}
	loginCustomerID := ""
	if args.LoginCustomerID != "" {
		if loginCustomerID, ok = normalizeCustomerID(args.LoginCustomerID); !ok {
			return googleAdsToolError("login_customer_id must be 10 digits"), nil
		}
	}
	payload := map[string]string{"query": args.Query}
	if args.PageToken != "" {
		payload["pageToken"] = args.PageToken
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return callGoogleAds(ctx, http.MethodPost,
		apiBaseURL+"/customers/"+customerID+"/googleAds:search",
		bytes.NewReader(body), loginCustomerID, call)
}

func normalizeCustomerID(value string) (string, bool) {
	value = strings.ReplaceAll(strings.TrimSpace(value), "-", "")
	if len(value) != 10 {
		return "", false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return value, true
}

func callGoogleAds(
	ctx context.Context,
	method string,
	url string,
	body io.Reader,
	loginCustomerID string,
	call connector.ManagedCall,
) (*mcp.CallToolResult, error) {
	developerToken, _ := call.Config["developer_token"].(string)
	if developerToken == "" {
		return nil, fmt.Errorf("google ads: developer_token is missing")
	}
	if call.AccessToken == "" {
		return nil, fmt.Errorf("google ads: OAuth access token is missing")
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+call.AccessToken)
	req.Header.Set("developer-token", developerToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if loginCustomerID != "" {
		req.Header.Set("login-customer-id", loginCustomerID)
	}
	resp, err := apiHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxAPIResponseBytes {
		return nil, fmt.Errorf("google ads: response exceeds %d bytes", maxAPIResponseBytes)
	}
	if resp.StatusCode >= 400 {
		message := fmt.Sprintf("Google Ads API returned %d", resp.StatusCode)
		var payload struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &payload) == nil && payload.Error.Message != "" {
			message += ": " + payload.Error.Message
		}
		return googleAdsToolError(message), nil
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}, nil
}

func googleAdsToolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: message}},
		StructuredContent: map[string]any{"error": message},
		IsError:           true,
	}
}
