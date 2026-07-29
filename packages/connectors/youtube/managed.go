package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/managedapi"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxAPIResponseBytes int64 = 4 << 20

var (
	apiBaseURL    = "https://www.googleapis.com/youtube/v3"
	apiHTTPClient = managedapi.NewHTTPClient(30 * time.Second)
)

func getMyChannel(ctx context.Context, call connector.ManagedCall) (*mcp.CallToolResult, error) {
	return callYouTube(ctx, "/channels", url.Values{
		"part": {"id,snippet,contentDetails,statistics"},
		"mine": {"true"},
	}, call.AccessToken)
}

func listMyPlaylists(ctx context.Context, call connector.ManagedCall) (*mcp.CallToolResult, error) {
	args, err := decodeArguments(call.Arguments)
	if err != nil {
		return youtubeToolError("arguments must be a JSON object"), nil
	}
	query := url.Values{
		"part":       {"id,snippet,contentDetails,status"},
		"mine":       {"true"},
		"maxResults": {"25"},
	}
	addPageToken(query, args.PageToken)
	return callYouTube(ctx, "/playlists", query, call.AccessToken)
}

func listSubscriptions(ctx context.Context, call connector.ManagedCall) (*mcp.CallToolResult, error) {
	args, err := decodeArguments(call.Arguments)
	if err != nil {
		return youtubeToolError("arguments must be a JSON object"), nil
	}
	query := url.Values{
		"part":       {"id,snippet,contentDetails"},
		"mine":       {"true"},
		"maxResults": {"25"},
	}
	addPageToken(query, args.PageToken)
	return callYouTube(ctx, "/subscriptions", query, call.AccessToken)
}

func searchVideos(ctx context.Context, call connector.ManagedCall) (*mcp.CallToolResult, error) {
	args, err := decodeArguments(call.Arguments)
	if err != nil {
		return youtubeToolError("arguments must be a JSON object"), nil
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return youtubeToolError("query must not be empty"), nil
	}
	query := url.Values{
		"part":       {"snippet"},
		"type":       {"video"},
		"q":          {args.Query},
		"maxResults": {"25"},
	}
	addPageToken(query, args.PageToken)
	return callYouTube(ctx, "/search", query, call.AccessToken)
}

type arguments struct {
	Query     string `json:"query"`
	PageToken string `json:"page_token"`
}

func decodeArguments(raw json.RawMessage) (arguments, error) {
	if len(raw) == 0 {
		return arguments{}, nil
	}
	var args arguments
	if err := json.Unmarshal(raw, &args); err != nil {
		return arguments{}, err
	}
	return args, nil
}

func addPageToken(query url.Values, pageToken string) {
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
}

func callYouTube(
	ctx context.Context,
	path string,
	query url.Values,
	accessToken string,
) (*mcp.CallToolResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = query.Encode()
	req.Header.Set("Authorization", "Bearer "+accessToken)

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
		return nil, fmt.Errorf("youtube: response exceeds %d bytes", maxAPIResponseBytes)
	}
	if resp.StatusCode >= 400 {
		return youtubeAPIError(resp.StatusCode, data), nil
	}

	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}
	var structured map[string]any
	if json.Unmarshal(data, &structured) == nil && structured != nil {
		result.StructuredContent = structured
	}
	return result, nil
}

// youtubeAPIError exposes only Google's standard reason. It never forwards
// the full error body, which can carry details such as project numbers.
func youtubeAPIError(statusCode int, data []byte) *mcp.CallToolResult {
	var payload struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &payload)

	reason := ""
	if len(payload.Error.Errors) > 0 {
		reason = payload.Error.Errors[0].Reason
	}
	if reason == "" && len(payload.Error.Details) > 0 {
		reason = payload.Error.Details[0].Reason
	}

	message := fmt.Sprintf("YouTube API returned %d", statusCode)
	structured := map[string]any{
		"error":           "upstream_error",
		"upstream_status": statusCode,
	}
	if reason != "" {
		message += " (" + reason + ")"
		structured["reason"] = reason
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: message}},
		StructuredContent: structured,
		IsError:           true,
	}
}

func youtubeToolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: message}},
		StructuredContent: map[string]any{"error": message},
		IsError:           true,
	}
}
