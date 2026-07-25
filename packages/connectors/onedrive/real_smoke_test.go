package onedrive

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/smoketest"
	"github.com/memohai/connect-it/packages/connectors/internal/toolfail"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

const (
	oneDriveSmokeAccessTokenEnv = "CONNECT_IT_ONEDRIVE_SMOKE_ACCESS_TOKEN"
	oneDriveSmokeFolderEnv      = "CONNECT_IT_ONEDRIVE_SMOKE_FOLDER"
	oneDriveSmokeAllowWritesEnv = "CONNECT_IT_ONEDRIVE_SMOKE_ALLOW_WRITES"

	oneDriveSmokeConnection = "onedrive-real-smoke"
	oneDriveSmokeFilePrefix = "connect-it-onedrive-real-smoke-"

	oneDriveSmokeCleanupLookupAttempts = 10
)

// TestOneDriveRealSmoke is an opt-in real-account harness for a dedicated
// Microsoft test account or tenant. It validates the OAuth credential, calls
// both published Tools, uploads one uniquely named non-sensitive text file,
// and removes that file through a fixed test-only Graph DELETE.
//
// The cleanup is registered before the upload and is restricted to the
// generated smoke filename. The harness never logs the access token, account
// profile, request/response bodies, drive listings, file content, or web URL.
func TestOneDriveRealSmoke(t *testing.T) {
	config := smoketest.Config(t, "OneDrive", parseOneDriveSmokeConfig)

	factory, err := providerkit.NewFactoryFromEnv()
	if err != nil {
		t.Fatal("construct OneDrive provider client factory")
	}
	validators, err := NewCredentialValidators(factory)
	if err != nil {
		t.Fatal("construct OneDrive credential validators")
	}
	handlers, err := NewHandlers(factory)
	if err != nil {
		t.Fatal("construct OneDrive handlers")
	}
	cleanupClient, err := factory.NewStaticClient(providerkit.Policy{
		Provider:         string(Definition.Type),
		BaseURL:          managedBaseURL,
		AllowedOrigins:   []string{managedBaseURL},
		RedirectMode:     providerkit.RedirectDenyAll,
		NetworkMode:      providerkit.PublicOnly,
		RequestTimeout:   providerkit.DefaultRequestTimeout,
		MaxResponseBytes: 64 << 10,
		Retry:            providerkit.RetryPolicy{Disabled: true},
	})
	if err != nil {
		t.Fatal("construct OneDrive cleanup client")
	}
	t.Cleanup(cleanupClient.CloseIdleConnections)

	validator, exists := validators["oauth"]
	if !exists {
		t.Fatal("OneDrive OAuth credential validator is not registered")
	}
	validation, err := validator(
		t.Context(),
		connector.CredentialValidationInput{
			ConnectorType: Definition.Type,
			AuthMethodKey: "oauth",
			AuthType:      connector.AuthOAuth2,
			ConnectionID:  oneDriveSmokeConnection,
			AccessToken:   config.accessToken,
			TokenType:     "Bearer",
		},
	)
	if err != nil {
		t.Fatal("validate OneDrive credential")
	}
	if validation.Profile.AccountID == "" ||
		validation.Profile.DisplayName == "" ||
		validation.ScopesKnown ||
		len(validation.GrantedScopes) != 0 {
		t.Fatal("OneDrive validator returned an invalid profile or scope snapshot")
	}
	smoketest.RequireRejectedCredential(
		t,
		validator,
		connector.CredentialValidationInput{
			ConnectorType: Definition.Type,
			AuthMethodKey: "oauth",
			AuthType:      connector.AuthOAuth2,
			ConnectionID:  oneDriveSmokeConnection + "-invalid-auth",
			AccessToken:   "connect-it-deliberately-invalid-graph-token",
			TokenType:     "Bearer",
		},
	)

	definitionRegistry := smoketest.Registry(
		t,
		Definition,
		"list_drive_items",
		"upload_file",
	)
	run := func(toolID string, arguments map[string]any) json.RawMessage {
		t.Helper()
		return smoketest.Run(t, definitionRegistry, handlers, smoketest.TokenCall(
			Definition,
			oneDriveSmokeConnection,
			toolID,
			arguments,
			config.accessToken,
		))
	}

	listed := run("list_drive_items", map[string]any{"path": config.folder})
	var listOutput struct {
		Value []json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(listed, &listOutput); err != nil ||
		listOutput.Value == nil {
		t.Fatal("list_drive_items returned an invalid item collection")
	}

	marker := smoketest.Marker(t)
	filename := oneDriveSmokeFilePrefix + marker + ".txt"
	targetPath := strings.Trim(config.folder, "/") + "/" + filename
	content := "connect-it OneDrive opt-in real smoke " + marker
	var itemID string
	t.Cleanup(func() {
		oneDriveCleanupRealSmokeFile(
			t,
			cleanupClient,
			config.accessToken,
			targetPath,
			itemID,
			int64(len(content)),
			smoketest.WaitSecond,
		)
	})

	created := run("upload_file", map[string]any{
		"path":    targetPath,
		"content": content,
	})
	var output struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(created, &output); err != nil ||
		!validOneDriveSmokeItemID(output.ID) ||
		output.Name != filename ||
		output.Size != int64(len(content)) {
		t.Fatal("upload_file returned an invalid smoke-file identity")
	}
	itemID = output.ID
}

type oneDriveSmokeConfiguration struct {
	accessToken string
	folder      string
}

func parseOneDriveSmokeConfig(
	lookup smoketest.Lookup,
) (oneDriveSmokeConfiguration, bool, error) {
	values, configured, err := smoketest.Values(
		"OneDrive",
		lookup,
		oneDriveSmokeAccessTokenEnv,
		oneDriveSmokeFolderEnv,
		oneDriveSmokeAllowWritesEnv,
	)
	if !configured || err != nil {
		return oneDriveSmokeConfiguration{}, configured, err
	}
	if err := smoketest.Exactly(
		values,
		oneDriveSmokeAllowWritesEnv,
		"true",
	); err != nil {
		return oneDriveSmokeConfiguration{}, true, err
	}
	folder := values[oneDriveSmokeFolderEnv]
	if !validOneDriveSmokeFolder(folder) {
		return oneDriveSmokeConfiguration{}, true, fmt.Errorf(
			"%s must name a dedicated drive-relative smoke folder",
			oneDriveSmokeFolderEnv,
		)
	}
	return oneDriveSmokeConfiguration{
		accessToken: values[oneDriveSmokeAccessTokenEnv],
		folder:      folder,
	}, true, nil
}

func validOneDriveSmokeFolder(value string) bool {
	if value == "" ||
		value != strings.TrimSpace(value) ||
		strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") ||
		len(value) > 512 {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, character := range segment {
			if character < 0x20 || character == 0x7f {
				return false
			}
		}
	}
	return true
}

func validOneDriveSmokeItemID(value string) bool {
	if value == "" ||
		value != strings.TrimSpace(value) ||
		value == "." ||
		value == ".." ||
		len(value) > 2048 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

// The shared harness covers the env gate itself; this covers the write gate and
// the folder, which is the operator value that decides what an upload can reach.
func TestOneDriveSmokeConfigRejectsUnsafeFolders(t *testing.T) {
	t.Parallel()
	complete := map[string]string{
		oneDriveSmokeAccessTokenEnv: "redacted-token",
		oneDriveSmokeFolderEnv:      "connect-it-smoke",
		oneDriveSmokeAllowWritesEnv: "true",
	}
	for name, value := range map[string]string{
		oneDriveSmokeAllowWritesEnv: "TRUE",
		oneDriveSmokeFolderEnv:      "../production",
	} {
		values := maps.Clone(complete)
		values[name] = value
		if _, _, err := parseOneDriveSmokeConfig(
			smoketest.MapLookup(values),
		); err == nil {
			t.Errorf("%s accepted an unsafe value", name)
		}
	}
	config, configured, err := parseOneDriveSmokeConfig(
		smoketest.MapLookup(complete),
	)
	if !configured || err != nil || config.folder != "connect-it-smoke" {
		t.Fatalf("complete OneDrive smoke config = %+v err %v", config, err)
	}
}

func TestOneDriveSmokeItemIDSafety(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", ".", "..", " item", "item ", "item\n"} {
		if validOneDriveSmokeItemID(value) {
			t.Errorf("invalid OneDrive item ID was accepted")
		}
	}
	for _, value := range []string{"01ABCDEF", "b!drive-item_42", "opaque.id"} {
		if !validOneDriveSmokeItemID(value) {
			t.Errorf("valid OneDrive item ID was rejected")
		}
	}
}

// wait is the backoff between two bounded lookup attempts; the real harness
// passes smoketest.WaitSecond and tests inject their own.
func oneDriveCleanupRealSmokeFile(
	t *testing.T,
	client *providerkit.Client,
	accessToken string,
	targetPath string,
	itemID string,
	expectedSize int64,
	wait smoketest.Wait,
) {
	t.Helper()
	filename := path.Base(targetPath)
	if !validOneDriveSmokeTargetPath(targetPath) ||
		expectedSize <= 0 ||
		expectedSize > maxUploadContentBytes ||
		(itemID != "" && !validOneDriveSmokeItemID(itemID)) ||
		client == nil ||
		wait == nil {
		t.Error("refuse to clean an invalid OneDrive smoke-file identity")
		return
	}
	authorizer, err := providerkit.Bearer(accessToken)
	if err != nil {
		t.Error("construct OneDrive smoke cleanup authorization")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	identity, failure := oneDriveResolveExactSmokeFile(
		ctx,
		client,
		authorizer,
		targetPath,
		itemID,
		expectedSize,
		wait,
	)
	if failure != nil {
		t.Errorf(
			"cleanup OneDrive smoke file %s could not resolve one exact resource: code=%s upstream_status=%d",
			filename,
			failure.Code,
			failure.UpstreamStatus,
		)
		return
	}
	response, err := client.Do(ctx, providerkit.Request{
		Method: http.MethodDelete,
		URL: "/v1.0/me/drive/items/" +
			providerkit.PathSegment(identity.ID),
		Authorizer: authorizer,
		Labels: providerkit.RequestLabels{
			ConnectorType:   string(Definition.Type),
			Operation:       "real_smoke_cleanup",
			AuthorizationID: oneDriveSmokeConnection,
		},
	})
	if err != nil {
		failure := connector.NormalizeToolFailure(
			providerkit.AsToolFailure(err),
		)
		t.Errorf(
			"cleanup OneDrive smoke file %s failed: code=%s upstream_status=%d",
			filename,
			failure.Code,
			failure.UpstreamStatus,
		)
		return
	}
	if response == nil ||
		response.StatusCode != http.StatusNoContent ||
		!response.NoContent() {
		t.Errorf(
			"cleanup OneDrive smoke file %s returned an unexpected status",
			filename,
		)
	}
}

type oneDriveSmokeFileIdentity struct {
	ID         string
	Name       string
	Size       int64
	ParentPath string
}

// oneDriveResolveExactSmokeFile keeps rechecking the exact upload path until it
// finds an item that matches the smoke identity in every field. Graph can hide
// a just-uploaded item for a moment, so a 404 here is uncertainty, not proof
// that nothing was uploaded.
func oneDriveResolveExactSmokeFile(
	ctx context.Context,
	client *providerkit.Client,
	authorizer providerkit.Authorizer,
	targetPath string,
	expectedID string,
	expectedSize int64,
	wait smoketest.Wait,
) (oneDriveSmokeFileIdentity, *connector.ToolFailure) {
	var identity oneDriveSmokeFileIdentity
	failure := smoketest.Retry(
		ctx,
		oneDriveSmokeCleanupLookupAttempts,
		wait,
		func(ctx context.Context) (bool, bool, *connector.ToolFailure) {
			found, failure := oneDriveLookupExactSmokeFile(
				ctx,
				client,
				authorizer,
				targetPath,
				expectedID,
				expectedSize,
			)
			if failure == nil {
				identity = found
				return true, false, nil
			}
			return false, smoketest.Retryable(failure), failure
		},
	)
	if failure != nil {
		return oneDriveSmokeFileIdentity{}, failure
	}
	return identity, nil
}

func oneDriveLookupExactSmokeFile(
	ctx context.Context,
	client *providerkit.Client,
	authorizer providerkit.Authorizer,
	targetPath string,
	expectedID string,
	expectedSize int64,
) (oneDriveSmokeFileIdentity, *connector.ToolFailure) {
	if client == nil || authorizer == nil ||
		!validOneDriveSmokeTargetPath(targetPath) ||
		expectedSize <= 0 ||
		expectedSize > maxUploadContentBytes ||
		(expectedID != "" && !validOneDriveSmokeItemID(expectedID)) {
		return oneDriveSmokeFileIdentity{}, toolfail.New(
			connector.FailureConfigurationError,
			0,
			0,
		)
	}
	response, err := client.Do(ctx, providerkit.Request{
		Method: http.MethodGet,
		URL:    "/v1.0/me/drive/root:/" + escapePath(targetPath),
		Query: url.Values{
			"$select": {"id,name,size,parentReference,file,folder"},
		},
		Authorizer: authorizer,
		Headers: http.Header{
			"Accept": {"application/json"},
		},
		Labels: providerkit.RequestLabels{
			ConnectorType:   string(Definition.Type),
			Operation:       "real_smoke_cleanup_lookup",
			AuthorizationID: oneDriveSmokeConnection,
		},
	})
	if err != nil {
		return oneDriveSmokeFileIdentity{}, providerkit.AsToolFailure(err)
	}
	if response == nil {
		return oneDriveSmokeFileIdentity{}, toolfail.New(
			connector.FailureInvalidResponse,
			0,
			0,
		)
	}
	var item struct {
		ID              string          `json:"id"`
		Name            string          `json:"name"`
		Size            *int64          `json:"size"`
		ParentReference json.RawMessage `json:"parentReference"`
		File            json.RawMessage `json:"file"`
		Folder          json.RawMessage `json:"folder"`
	}
	if err := response.DecodeJSON(&item); err != nil {
		return oneDriveSmokeFileIdentity{}, providerkit.AsToolFailure(err)
	}
	var parent struct {
		Path string `json:"path"`
	}
	filename := path.Base(targetPath)
	folder := path.Dir(targetPath)
	if !validOneDriveSmokeItemID(item.ID) ||
		(expectedID != "" && item.ID != expectedID) ||
		item.Name != filename ||
		item.Size == nil ||
		*item.Size != expectedSize ||
		json.Unmarshal(item.ParentReference, &parent) != nil ||
		!oneDriveSmokeParentPathMatches(parent.Path, folder) ||
		!oneDriveSmokeRawObject(item.File) ||
		!oneDriveSmokeRawNullOrMissing(item.Folder) {
		return oneDriveSmokeFileIdentity{}, toolfail.New(
			connector.FailureInvalidResponse,
			response.StatusCode,
			0,
		)
	}
	return oneDriveSmokeFileIdentity{
		ID:         item.ID,
		Name:       item.Name,
		Size:       *item.Size,
		ParentPath: parent.Path,
	}, nil
}

func validOneDriveSmokeTargetPath(targetPath string) bool {
	if targetPath == "" ||
		targetPath != strings.TrimSpace(targetPath) ||
		strings.HasPrefix(targetPath, "/") ||
		path.Clean(targetPath) != targetPath {
		return false
	}
	filename := path.Base(targetPath)
	folder := path.Dir(targetPath)
	return folder != "." &&
		validOneDriveSmokeFolder(folder) &&
		strings.HasPrefix(filename, oneDriveSmokeFilePrefix) &&
		strings.HasSuffix(filename, ".txt") &&
		len(filename) > len(oneDriveSmokeFilePrefix)+len(".txt")
}

func oneDriveSmokeParentPathMatches(
	parentPath string,
	folder string,
) bool {
	decoded, err := url.PathUnescape(parentPath)
	if err != nil || decoded == "" ||
		decoded != strings.TrimSpace(decoded) {
		return false
	}
	const marker = "/root:/"
	index := strings.Index(decoded, marker)
	if index < 0 ||
		!strings.EqualFold(decoded[index+len(marker):], folder) {
		return false
	}
	prefix := decoded[:index]
	if prefix == "/drive" {
		return true
	}
	if !strings.HasPrefix(prefix, "/drives/") {
		return false
	}
	driveID := strings.TrimPrefix(prefix, "/drives/")
	return driveID != "" &&
		!strings.Contains(driveID, "/") &&
		validOneDriveSmokeItemID(driveID)
}

func oneDriveSmokeRawObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return len(raw) != 0 &&
		json.Unmarshal(raw, &value) == nil &&
		value != nil
}

func oneDriveSmokeRawNullOrMissing(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed == "" || trimmed == "null"
}

// Cleanup always resolves exactly one item at the exact upload path and then
// deletes only that item, requiring 204 No Content. The two rows are the two
// states it can start from: the upload response carried an item ID, or the
// upload response was lost and Graph has not made the item visible yet — which
// is uncertainty, not proof that nothing was uploaded.
func TestOneDriveSmokeCleanupResolvesExactItemThenDeletes(t *testing.T) {
	t.Parallel()
	const expectedSize int64 = 42
	tests := []struct {
		name          string
		filename      string
		expectedID    string
		itemID        string
		notFoundFirst bool
		wantCalls     int
	}{
		{
			name:       "known identity",
			filename:   "fixture.txt",
			expectedID: "01ABCDEF",
			itemID:     "01ABCDEF",
			wantCalls:  2,
		},
		{
			name:          "lost upload response",
			filename:      "lost-response.txt",
			itemID:        "01RECOVERED",
			notFoundFirst: true,
			wantCalls:     3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			targetPath := "connect-it-smoke/" +
				oneDriveSmokeFilePrefix + test.filename
			var calls int
			server := testkit.NewServerWithOptions(
				t,
				http.HandlerFunc(func(
					writer http.ResponseWriter,
					request *http.Request,
				) {
					calls++
					testkit.AssertHeader(
						t,
						request,
						"Authorization",
						"Bearer redacted-token",
					)
					switch request.Method {
					case http.MethodGet:
						if request.URL.Path !=
							"/v1.0/me/drive/root:/"+targetPath ||
							request.URL.Query().Get("$select") !=
								"id,name,size,parentReference,file,folder" {
							t.Error("OneDrive cleanup lookup mapping changed")
						}
						if test.notFoundFirst && calls == 1 {
							http.Error(
								writer,
								"not found",
								http.StatusNotFound,
							)
							return
						}
						writer.Header().Set("Content-Type", "application/json")
						_ = json.NewEncoder(writer).Encode(
							oneDriveSmokeCleanupItemFixture(
								test.itemID,
								path.Base(targetPath),
								"/drive/root:/connect-it-smoke",
								expectedSize,
							),
						)
					case http.MethodDelete:
						if request.URL.Path !=
							"/v1.0/me/drive/items/"+test.itemID {
							t.Error(
								"OneDrive cleanup did not delete the verified item",
							)
						}
						writer.WriteHeader(http.StatusNoContent)
					default:
						t.Error("OneDrive cleanup made an unexpected request")
					}
				}),
				testkit.ServerOptions{Provider: string(Definition.Type)},
			)
			oneDriveCleanupRealSmokeFile(
				t,
				server.Client,
				"redacted-token",
				targetPath,
				test.expectedID,
				expectedSize,
				func(context.Context) bool { return true },
			)
			if calls != test.wantCalls {
				t.Fatalf(
					"OneDrive cleanup calls = %d, want %d",
					calls,
					test.wantCalls,
				)
			}
		})
	}
}

func TestOneDriveSmokeCleanupRejectsAmbiguousIdentity(t *testing.T) {
	t.Parallel()
	const expectedSize int64 = 44
	targetPath := "connect-it-smoke/" + oneDriveSmokeFilePrefix + "ambiguous.txt"
	server := testkit.NewServerWithOptions(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet {
				t.Error("ambiguous OneDrive identity reached DELETE")
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(
				oneDriveSmokeCleanupItemFixture(
					"01DIFFERENT",
					path.Base(targetPath),
					"/drive/root:/connect-it-smoke",
					expectedSize,
				),
			)
		}),
		testkit.ServerOptions{Provider: string(Definition.Type)},
	)
	authorizer, err := providerkit.Bearer("redacted-token")
	if err != nil {
		t.Fatal("construct OneDrive test authorization")
	}
	_, failure := oneDriveLookupExactSmokeFile(
		t.Context(),
		server.Client,
		authorizer,
		targetPath,
		"01EXPECTED",
		expectedSize,
	)
	if failure == nil || failure.Code != connector.FailureInvalidResponse {
		t.Fatalf("ambiguous identity failure = %+v", failure)
	}
}

func oneDriveSmokeCleanupItemFixture(
	itemID string,
	filename string,
	parentPath string,
	size int64,
) map[string]any {
	return map[string]any{
		"id":   itemID,
		"name": filename,
		"size": size,
		"parentReference": map[string]any{
			"path": parentPath,
		},
		"file": map[string]any{
			"mimeType": "text/plain",
		},
	}
}
