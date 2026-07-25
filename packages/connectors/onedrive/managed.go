package onedrive

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/memohai/connect-it/packages/connectors/internal/toolargs"
	"github.com/memohai/connect-it/packages/connectors/internal/toolfail"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	managedBaseURL         = "https://graph.microsoft.com/"
	maxUploadContentBytes  = 4 << 20
	uploadContentMediaType = "application/octet-stream"
)

type managedHandler struct {
	client *providerkit.Client
}

// NewHandlers constructs OneDrive's Managed handlers from the process-wide
// providerkit Factory. The static Graph origin cannot be changed by Tool input.
func NewHandlers(
	factory *providerkit.Factory,
) (connector.HandlerMap, error) {
	client, err := factory.NewStaticClient(providerkit.Policy{
		Provider:         string(Definition.Type),
		BaseURL:          managedBaseURL,
		AllowedOrigins:   []string{managedBaseURL},
		RedirectMode:     providerkit.RedirectDenyAll,
		NetworkMode:      providerkit.PublicOnly,
		RequestTimeout:   providerkit.DefaultRequestTimeout,
		MaxResponseBytes: providerkit.DefaultMaxResponseBytes,
		Retry:            providerkit.DefaultRetryPolicy(),
	})
	if err != nil {
		return nil, err
	}
	return newHandlers(client), nil
}

func newHandlers(client *providerkit.Client) connector.HandlerMap {
	handler := &managedHandler{client: client}
	return connector.HandlerMap{
		"list_drive_items": handler.listDriveItems,
		"upload_file":      handler.uploadFile,
	}
}

// invalidInput is the finished result for every argument rejected before any
// egress happens.
func invalidInput() (connector.ToolResultData, error) {
	return toolfail.Result(connector.FailureInvalidInput, 0, 0), nil
}

// listDriveItems lists either the drive root or one explicitly escaped folder.
func (handler *managedHandler) listDriveItems(
	ctx context.Context,
	call connector.ToolCallContext,
) (connector.ToolResultData, error) {
	path := "/v1.0/me/drive/root/children"
	if value, present := call.Arguments["path"]; present {
		folder, ok := toolargs.String(value, 1024, false)
		if !ok {
			return invalidInput()
		}
		if folder != "" {
			path = "/v1.0/me/drive/root:/" + escapePath(folder) + ":/children"
		}
	}
	return handler.call(ctx, call, http.MethodGet, path, nil, "")
}

// uploadFile performs Graph's simple upload. The Tool envelope has a larger
// temporary limit, so the decoded UTF-8 content must be checked here, before
// any Provider request is constructed.
func (handler *managedHandler) uploadFile(
	ctx context.Context,
	call connector.ToolCallContext,
) (connector.ToolResultData, error) {
	path, okPath := toolargs.String(call.Arguments["path"], 1024, false)
	// content is arbitrary file text, so it is bounded and UTF-8 checked here
	// rather than filtered through the control-character-free text helpers.
	content, okContent := call.Arguments["content"].(string)
	if !okPath || !okContent || path == "" || content == "" ||
		!utf8.ValidString(content) {
		return invalidInput()
	}
	if len(content) > maxUploadContentBytes {
		return toolfail.Result(connector.FailureInputTooLarge, 0, 0), nil
	}
	return handler.call(
		ctx,
		call,
		http.MethodPut,
		"/v1.0/me/drive/root:/"+escapePath(path)+":/content",
		[]byte(content),
		uploadContentMediaType,
	)
}

// escapePath encodes path segments independently while preserving separators.
func escapePath(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for index, segment := range segments {
		segments[index] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// call issues one guarded request. Graph's simple upload sends a raw
// octet-stream body, which restkit.Request cannot describe, so this connector
// keeps its own request construction and reuses only the failure mapping.
func (handler *managedHandler) call(
	ctx context.Context,
	call connector.ToolCallContext,
	method string,
	path string,
	body []byte,
	contentType string,
) (connector.ToolResultData, error) {
	// The OAuth boundary normalizes the scheme; anything else is a credential
	// this handler must not present upstream.
	if call.TokenType != "Bearer" {
		return toolfail.Result(connector.FailureAuthorizationFailed, 0, 0), nil
	}
	authorizer, err := providerkit.Bearer(call.AccessToken)
	if err != nil {
		return toolfail.Result(connector.FailureAuthorizationFailed, 0, 0), nil
	}
	request := providerkit.Request{
		Method:     method,
		URL:        path,
		Authorizer: authorizer,
		Labels: providerkit.RequestLabels{
			ConnectorType: string(Definition.Type),
			ToolID:        call.ToolID,
			ConnectionID:  call.ConnectionID,
		},
	}
	if body != nil {
		request.Body = body
		request.ContentType = contentType
	}
	response, err := handler.client.Do(ctx, request)
	if err != nil {
		return toolfail.FromProvider(err, nil), nil
	}

	var object map[string]json.RawMessage
	if err := response.DecodeJSON(&object); err != nil {
		return toolfail.FromProvider(err, nil), nil
	}
	if object == nil {
		return toolfail.Result(
			connector.FailureInvalidResponse,
			response.StatusCode,
			0,
		), nil
	}
	return connector.ToolResultData{
		Structured: append(json.RawMessage(nil), response.Body...),
	}, nil
}
