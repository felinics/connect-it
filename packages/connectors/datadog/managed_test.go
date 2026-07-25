package datadog

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
	"github.com/memohai/connect-it/packages/core/registry"
)

type datadogRecordedRequest struct {
	method string
	path   string
	query  url.Values
	header http.Header
}

type datadogTestRuntime struct {
	source   *datadogClientSource
	requests func() []datadogRecordedRequest
}

func newDatadogTestRuntime(
	t *testing.T,
	handler http.Handler,
	maxResponseBytes int64,
) datadogTestRuntime {
	t.Helper()
	var (
		lock     sync.Mutex
		recorded []datadogRecordedRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		lock.Lock()
		recorded = append(recorded, datadogRecordedRequest{
			method: request.Method,
			path:   request.URL.EscapedPath(),
			query:  request.URL.Query(),
			header: request.Header.Clone(),
		})
		lock.Unlock()
		handler.ServeHTTP(writer, request)
	}))
	t.Cleanup(server.Close)
	target := server.Listener.Addr().String()
	factory := providerkit.NewFactory(
		providerkit.WithInsecureProviderHTTP(true),
		providerkit.WithResolver(testkit.Resolver{}),
		providerkit.WithDialContext(testkit.DialTarget(target)),
	)
	client, err := factory.NewDynamicClient(providerkit.DynamicPolicyInput{
		Provider:          string(Definition.Type),
		BaseURL:           "http://provider.test:80/",
		AllowInsecureHTTP: "true",
		RedirectMode:      providerkit.RedirectDenyAll,
		RequestTimeout:    providerkit.DefaultRequestTimeout,
		MaxResponseBytes:  maxResponseBytes,
		Retry:             providerkit.RetryPolicy{Disabled: true},
	})
	if err != nil {
		t.Fatalf("construct Datadog test client: %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)
	clients := make(map[string]*providerkit.Client, len(datadogSites))
	for site := range datadogSites {
		clients[site] = client
	}
	return datadogTestRuntime{
		source: &datadogClientSource{clients: clients},
		requests: func() []datadogRecordedRequest {
			lock.Lock()
			defer lock.Unlock()
			return append([]datadogRecordedRequest(nil), recorded...)
		},
	}
}

// datadogBodyRuntime replies to every request with one fixed body.
func datadogBodyRuntime(
	t *testing.T,
	body string,
	maxResponseBytes int64,
) datadogTestRuntime {
	t.Helper()
	return newDatadogTestRuntime(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(body))
		}),
		maxResponseBytes,
	)
}

// datadogStatusRuntime replies to every request with one status, an optional
// Retry-After header and one body.
func datadogStatusRuntime(
	t *testing.T,
	status int,
	retryAfter string,
	body string,
) datadogTestRuntime {
	t.Helper()
	return newDatadogTestRuntime(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			if retryAfter != "" {
				writer.Header().Set("Retry-After", retryAfter)
			}
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(body))
		}),
		1024,
	)
}

// datadogRejectingRuntime fails the test if anything reaches the Provider.
func datadogRejectingRuntime(
	t *testing.T,
	reason string,
) datadogTestRuntime {
	t.Helper()
	return newDatadogTestRuntime(
		t,
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error(reason)
		}),
		1024,
	)
}

func datadogCall(
	toolID string,
	arguments map[string]any,
) connector.ToolCallContext {
	return connector.ToolCallContext{
		ConnectorType: Definition.Type,
		ConnectionID:  "connection-1",
		ToolID:        toolID,
		Arguments:     arguments,
		Credential: map[string]any{
			datadogAPIKeyField:         "api-secret",
			datadogApplicationKeyField: "application-secret",
			datadogSiteField:           "us1",
		},
	}
}

// datadogTimeseries runs query_timeseries_points against one fixed body.
func datadogTimeseries(
	t *testing.T,
	body string,
) (connector.ToolResultData, error) {
	t.Helper()
	runtime := datadogBodyRuntime(t, body, 4096)
	return newHandlers(runtime.source)["query_timeseries_points"](
		t.Context(),
		datadogCall("query_timeseries_points", map[string]any{
			"from": float64(1), "to": float64(2), "query": "test",
		}),
	)
}

func TestDatadogStaticPoliciesAndAuthorizerFootprint(t *testing.T) {
	source, err := newDatadogClientSource(providerkit.NewFactory())
	if err != nil {
		t.Fatal(err)
	}
	if len(source.clients) != len(datadogSites) || len(source.clients) != 9 {
		t.Fatalf("static clients = %d", len(source.clients))
	}
	for site, baseURL := range datadogSites {
		policy := source.clients[site].Policy()
		if policy.BaseURL != baseURL ||
			policy.NetworkMode != providerkit.PublicOnly ||
			policy.RedirectMode != providerkit.RedirectDenyAll ||
			policy.MaxResponseBytes != datadogMaxResponseBytes ||
			len(policy.AllowedOrigins) != 1 ||
			policy.AllowedOrigins[0] != strings.TrimSuffix(baseURL, "/") {
			t.Fatalf("%s policy = %+v", site, policy)
		}
	}
	authorizer := datadogAuthorizer{
		apiKey:         "api-secret",
		applicationKey: "application-secret",
	}
	footprint := authorizer.Footprint()
	if len(footprint.HeaderNames) != 2 ||
		footprint.HeaderNames[0] != "DD-API-KEY" ||
		footprint.HeaderNames[1] != "DD-APPLICATION-KEY" ||
		footprint.Body || len(footprint.QueryParamNames) != 0 {
		t.Fatalf("credential footprint = %+v", footprint)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	request.Header.Set("DD-API-KEY", "attacker")
	request.Header.Set("DD-APPLICATION-KEY", "attacker")
	if err := authorizer.Apply(request); err != nil {
		t.Fatal(err)
	}
	testkit.AssertHeader(t, request, "DD-API-KEY", "api-secret")
	testkit.AssertHeader(
		t,
		request,
		"DD-APPLICATION-KEY",
		"application-secret",
	)
}

func TestDatadogAllHandlersRequestsAndSchemas(t *testing.T) {
	const (
		monitor = `{"id":42,"name":"CPU high","type":"metric alert","query":"avg:system.cpu.user{*}>90","message":"Investigate","tags":["team:core"],"overall_state":"Alert"}`
		series  = `{"metric":"system.cpu.user","scope":"host:test","expression":"avg:system.cpu.user{*}","display_name":"cpu","unit":[{"family":"time","name":"second","plural":"seconds","scale_factor":1,"short_name":"s"},null],"pointlist":[[1720000000000,1.25],[1720000060000,null]]}`
	)
	runtime := newDatadogTestRuntime(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			testkit.AssertHeader(t, request, "DD-API-KEY", "api-secret")
			testkit.AssertHeader(
				t,
				request,
				"DD-APPLICATION-KEY",
				"application-secret",
			)
			testkit.AssertNoHeader(t, request, "Authorization")
			testkit.AssertNoHeader(t, request, "Referer")
			writer.Header().Set("Content-Type", "application/json")
			switch request.URL.Path {
			case "/api/v1/monitor":
				_, _ = writer.Write([]byte(`[` + monitor + `]`))
			case "/api/v1/monitor/42":
				_, _ = writer.Write([]byte(monitor))
			case "/api/v1/metrics":
				_, _ = writer.Write([]byte(
					`{"from":"1720000000","metrics":["system.cpu.user"]}`,
				))
			case "/api/v1/metrics/custom.metric":
				_, _ = writer.Write([]byte(
					`{"description":"desc","integration":"custom",` +
						`"per_unit":"second","short_name":"metric",` +
						`"statsd_interval":10,"type":"gauge","unit":"byte"}`,
				))
			case "/api/v1/query":
				_, _ = writer.Write([]byte(
					`{"status":"ok","res_type":"time_series","series":[` +
						series + `]}`,
				))
			default:
				http.Error(writer, "unexpected", http.StatusBadRequest)
			}
		}),
		datadogMaxResponseBytes,
	)
	handlers := newHandlers(runtime.source)
	calls := []connector.ToolCallContext{
		datadogCall("list_monitors", map[string]any{
			"page":           float64(2),
			"page_size":      float64(25),
			"group_states":   []any{"alert", "warn"},
			"name":           "CPU",
			"tags":           []any{"env:prod"},
			"monitor_tags":   []any{"team:core"},
			"with_downtimes": true,
		}),
		datadogCall(
			"get_monitor",
			map[string]any{
				"monitor_id":     float64(42),
				"group_states":   []any{"all"},
				"with_downtimes": false,
			},
		),
		datadogCall(
			"list_active_metrics",
			map[string]any{
				"from": float64(1720000000),
				"host": "host.example",
			},
		),
		datadogCall(
			"get_metric_metadata",
			map[string]any{"metric_name": "custom.metric"},
		),
		datadogCall(
			"query_timeseries_points",
			map[string]any{
				"from":  float64(1720000000),
				"to":    float64(1720003600),
				"query": "avg:system.cpu.user{*}",
			},
		),
	}
	results := make(map[string]json.RawMessage, len(calls))
	for _, call := range calls {
		result, err := handlers[call.ToolID](t.Context(), call)
		if err != nil || result.Failed() {
			t.Fatalf("%s result=%+v err=%v", call.ToolID, result, err)
		}
		results[call.ToolID] = result.Structured
	}
	requests := runtime.requests()
	if len(requests) != 5 {
		t.Fatalf("request count = %d", len(requests))
	}
	for index, request := range requests {
		if request.method != http.MethodGet {
			t.Errorf(
				"request %d method = %q, want GET",
				index,
				request.method,
			)
		}
	}
	if got := requests[0].query; got.Get("page") != "2" ||
		got.Get("page_size") != "25" ||
		got.Get("group_states") != "alert,warn" ||
		got.Get("tags") != "env:prod" ||
		got.Get("monitor_tags") != "team:core" ||
		got.Get("with_downtimes") != "true" {
		t.Fatalf("list monitors query = %v", got)
	}
	if got := requests[1].query; got.Get("group_states") != "all" ||
		got.Get("with_downtimes") != "false" {
		t.Fatalf("get monitor query = %v", got)
	}
	if got := requests[2].query; got.Get("from") != "1720000000" ||
		got.Get("host") != "host.example" {
		t.Fatalf("active metrics query = %v", got)
	}
	if got := requests[4].query; got.Get("from") != "1720000000" ||
		got.Get("to") != "1720003600" ||
		got.Get("query") != "avg:system.cpu.user{*}" {
		t.Fatalf("timeseries query = %v", got)
	}
	var metadata struct {
		Metric struct {
			MetricName string `json:"metric_name"`
		} `json:"metric"`
	}
	if err := json.Unmarshal(
		results["get_metric_metadata"],
		&metadata,
	); err != nil || metadata.Metric.MetricName != "custom.metric" {
		t.Fatalf("metadata output = %s err=%v", results["get_metric_metadata"], err)
	}

	definitionRegistry := registry.New()
	if err := definitionRegistry.Register(
		Definition,
		datadogDefinitionHandlerKeys()...,
	); err != nil {
		t.Fatal(err)
	}
	for toolID, structured := range results {
		schemas, exists := definitionRegistry.ToolSchemas(
			Definition.Type,
			toolID,
		)
		if !exists {
			t.Fatalf("missing schemas for %s", toolID)
		}
		var output any
		if err := json.Unmarshal(structured, &output); err != nil {
			t.Fatalf("%s output JSON: %v", toolID, err)
		}
		if err := schemas.Output.Validate(output); err != nil {
			t.Fatalf(
				"%s output violates schema: %v\n%s",
				toolID,
				err,
				structured,
			)
		}
	}
}

func TestDatadogListMonitorsAlwaysBoundsPagination(t *testing.T) {
	runtime := newDatadogTestRuntime(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			testkit.AssertQuery(t, request, "page", "0")
			testkit.AssertQuery(t, request, "page_size", "100")
			_, _ = writer.Write([]byte(`[]`))
		}),
		1024,
	)
	result, err := newHandlers(runtime.source)["list_monitors"](
		t.Context(),
		datadogCall("list_monitors", nil),
	)
	if err != nil || result.Failed() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestDatadogRedirectPolicyNeverForwardsEitherCredential(t *testing.T) {
	var (
		startRequests  int
		targetRequests int
	)
	runtime := newDatadogTestRuntime(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			switch request.URL.Path {
			case "/api/v1/monitor/42":
				startRequests++
				testkit.AssertHeader(t, request, "DD-API-KEY", "api-secret")
				testkit.AssertHeader(
					t,
					request,
					"DD-APPLICATION-KEY",
					"application-secret",
				)
				http.Redirect(
					writer,
					request,
					"/redirect-target",
					http.StatusFound,
				)
			case "/redirect-target":
				targetRequests++
				testkit.AssertNoHeader(t, request, "DD-API-KEY")
				testkit.AssertNoHeader(t, request, "DD-APPLICATION-KEY")
				_, _ = writer.Write([]byte(`{}`))
			}
		}),
		1024,
	)
	result, err := newHandlers(runtime.source)["get_monitor"](
		t.Context(),
		datadogCall(
			"get_monitor",
			map[string]any{"monitor_id": float64(42)},
		),
	)
	if err != nil || result.Failure == nil ||
		result.Failure.Code != connector.FailurePolicyDenied ||
		startRequests != 1 || targetRequests != 0 {
		t.Fatalf(
			"redirect result=%+v err=%v start=%d target=%d",
			result,
			err,
			startRequests,
			targetRequests,
		)
	}
}

func TestDatadogRejectsInvalidInputsBeforeRequest(t *testing.T) {
	runtime := datadogRejectingRuntime(t, "invalid input reached Provider")
	handlers := newHandlers(runtime.source)
	tests := []connector.ToolCallContext{
		datadogCall(
			"list_monitors",
			map[string]any{"page_size": float64(101)},
		),
		datadogCall(
			"list_monitors",
			map[string]any{"group_states": []any{"warn", "warn"}},
		),
		datadogCall(
			"get_monitor",
			map[string]any{"monitor_id": float64(0)},
		),
		datadogCall(
			"get_monitor",
			map[string]any{"monitor_id": int64(1 << 53)},
		),
		datadogCall(
			"list_active_metrics",
			map[string]any{
				"from":       float64(1),
				"host":       "host",
				"tag_filter": "env:prod",
			},
		),
		datadogCall(
			"list_active_metrics",
			map[string]any{"from": float64(1), "host": "bad host"},
		),
		datadogCall(
			"get_metric_metadata",
			map[string]any{"metric_name": "../secret"},
		),
		datadogCall(
			"query_timeseries_points",
			map[string]any{
				"from":  float64(2),
				"to":    float64(1),
				"query": "avg:test{*}",
			},
		),
		datadogCall(
			"query_timeseries_points",
			map[string]any{
				"from":  float64(0),
				"to":    float64(datadogMaxQueryWindowSecs + 1),
				"query": "avg:test{*}",
			},
		),
	}
	for _, call := range tests {
		result, err := handlers[call.ToolID](t.Context(), call)
		if err != nil || result.Failure == nil ||
			result.Failure.Code != connector.FailureInvalidInput {
			t.Fatalf("%s result=%+v err=%v", call.ToolID, result, err)
		}
	}
	if len(runtime.requests()) != 0 {
		t.Fatalf("invalid inputs made %d requests", len(runtime.requests()))
	}
}

func TestDatadogTimeseriesStrictPointShapeAndNullableUnit(t *testing.T) {
	t.Run("missing series normalizes to empty array", func(t *testing.T) {
		result, err := datadogTimeseries(
			t,
			`{"status":"ok","res_type":"time_series"}`,
		)
		if err != nil || result.Failed() ||
			!strings.Contains(string(result.Structured), `"series":[]`) {
			t.Fatalf("result=%s failure=%+v err=%v", result.Structured, result.Failure, err)
		}
	})
	t.Run("nullable unit normalizes to empty array", func(t *testing.T) {
		result, err := datadogTimeseries(
			t,
			`{"status":"ok","res_type":"time_series","series":[{`+
				`"metric":"test.metric","scope":"*","expression":"x",`+
				`"display_name":"x","unit":null,`+
				`"pointlist":null}]}`,
		)
		if err != nil || result.Failed() ||
			!strings.Contains(string(result.Structured), `"unit":[]`) ||
			!strings.Contains(string(result.Structured), `"pointlist":[]`) {
			t.Fatalf("result=%s failure=%+v err=%v", result.Structured, result.Failure, err)
		}
	})
	for _, test := range []struct {
		name      string
		timestamp string
		want      string
	}{
		{"decimal integer", `1720000000000.0`, `1720000000000`},
		{"exponent integer", `1.72e12`, `1720000000000`},
		{"zero with negative exponent", `0e-100`, `0`},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := datadogTimeseries(t, datadogSeriesBody(
				`[[`+test.timestamp+`,1]]`,
			))
			if err != nil || result.Failed() ||
				!strings.Contains(
					string(result.Structured),
					`"pointlist":[[`+test.want+`,1]]`,
				) {
				t.Fatalf(
					"result=%s failure=%+v err=%v",
					result.Structured,
					result.Failure,
					err,
				)
			}
		})
	}
	for _, test := range []struct {
		name      string
		pointlist string
	}{
		{"one item", `[[1720000000000]]`},
		{"three items", `[[1720000000000,1,2]]`},
		{"null timestamp", `[[null,1]]`},
		{"fractional timestamp", `[[1720000000000.5,1]]`},
		{"negative timestamp", `[[-1,1]]`},
		{"unsafe integer", `[[9007199254740992,1]]`},
		{"fraction rounded by float", `[[9007199254740991.1,1]]`},
		{"huge positive exponent", `[[1e9999999,1]]`},
		{"huge negative exponent", `[[1e-9999999,1]]`},
		{
			"overlong numeric token",
			`[[0.` + strings.Repeat("0", 256) + `,1]]`,
		},
		{"string timestamp", `[[ "1720000000000",1]]`},
		{"NaN timestamp", `[[NaN,1]]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := datadogTimeseries(
				t,
				datadogSeriesBody(test.pointlist),
			)
			if err != nil || result.Failure == nil ||
				result.Failure.Code != connector.FailureInvalidResponse {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

// datadogSeriesBody wraps one pointlist literal in an otherwise valid
// timeseries payload.
func datadogSeriesBody(pointlist string) string {
	return `{"status":"ok","res_type":"time_series","series":[{` +
		`"metric":"test.metric","scope":"*","expression":"x",` +
		`"display_name":"x","unit":[],` +
		`"pointlist":` + pointlist + `}]}`
}

func TestDatadogTimeseriesRequiresObjectAndSuccessfulStatus(t *testing.T) {
	tests := []struct {
		name string
		body string
		code connector.FailureCode
	}{
		{"null top level", `null`, connector.FailureInvalidResponse},
		{"array top level", `[]`, connector.FailureInvalidResponse},
		{"empty object", `{}`, connector.FailureInvalidResponse},
		{
			"missing status",
			`{"series":[]}`,
			connector.FailureInvalidResponse,
		},
		{
			"null status",
			`{"status":null,"series":[]}`,
			connector.FailureInvalidResponse,
		},
		{
			"wrong status type",
			`{"status":true,"series":[]}`,
			connector.FailureInvalidResponse,
		},
		{
			"logical provider failure",
			`{"status":"provider-secret api-secret application-secret",` +
				`"series":[]}`,
			connector.FailureProviderError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := datadogTimeseries(t, test.body)
			if err != nil || result.Failure == nil ||
				result.Failure.Code != test.code ||
				result.Failure.UpstreamStatus != http.StatusOK {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			for _, secret := range []string{
				"provider-secret",
				"api-secret",
				"application-secret",
			} {
				if strings.Contains(result.Failure.Message, secret) {
					t.Fatalf("failure leaked %q", secret)
				}
			}
		})
	}
}

// Datadog's five Tools all reach the Provider through the one
// managedHandler.get / restkit.Transport path, so this pins that connector's
// complete status contract — including its own 400/408/422 overrides — once.
func TestDatadogMapsProviderFailuresWithoutSecretLeaks(t *testing.T) {
	call := datadogCall(
		"get_monitor",
		map[string]any{"monitor_id": float64(42)},
	)
	tests := []struct {
		status            int
		retryAfter        string
		code              connector.FailureCode
		retrySeconds      int
		credentialInvalid bool
	}{
		{400, "", connector.FailureInvalidInput, 0, false},
		{401, "", connector.FailureAuthorizationFailed, 0, true},
		{403, "", connector.FailurePermissionDenied, 0, false},
		{404, "", connector.FailureNotFound, 0, false},
		{408, "", connector.FailureTimeout, 0, false},
		{422, "", connector.FailureInvalidInput, 0, false},
		{429, "9", connector.FailureRateLimited, 9, false},
		{500, "", connector.FailureUpstreamUnavailable, 0, false},
		{503, "", connector.FailureUpstreamUnavailable, 0, false},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			runtime := datadogStatusRuntime(
				t,
				test.status,
				test.retryAfter,
				"provider-body api-secret application-secret",
			)
			result, err := newHandlers(runtime.source)[call.ToolID](
				t.Context(),
				call,
			)
			if err != nil || result.Failure == nil ||
				result.Failure.Code != test.code ||
				result.Failure.UpstreamStatus != test.status ||
				result.Failure.RetryAfterSeconds != test.retrySeconds ||
				result.Failure.IndicatesCredentialInvalid() !=
					test.credentialInvalid {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			message := result.Failure.Message
			if strings.Contains(message, "provider-body") ||
				strings.Contains(message, "api-secret") ||
				strings.Contains(message, "application-secret") {
				t.Fatal("failure leaked Provider body or credential")
			}
		})
	}
}

func TestDatadogRejectsMalformedOversizeAndPrivateDNS(t *testing.T) {
	t.Run("malformed response", func(t *testing.T) {
		runtime := datadogBodyRuntime(t, `{"not":"an array"}`, 1024)
		result, _ := newHandlers(runtime.source)["list_monitors"](
			t.Context(),
			datadogCall("list_monitors", nil),
		)
		if result.Failure == nil ||
			result.Failure.Code != connector.FailureInvalidResponse {
			t.Fatalf("malformed result = %+v", result)
		}
	})
	t.Run("oversize response", func(t *testing.T) {
		runtime := datadogBodyRuntime(
			t,
			`[`+strings.Repeat(" ", 128)+`]`,
			32,
		)
		result, _ := newHandlers(runtime.source)["list_monitors"](
			t.Context(),
			datadogCall("list_monitors", nil),
		)
		if result.Failure == nil ||
			result.Failure.Code != connector.FailureResponseTooLarge {
			t.Fatalf("oversize result = %+v", result)
		}
	})
	t.Run("private DNS denied", func(t *testing.T) {
		dialed := false
		factory := providerkit.NewFactory(
			providerkit.WithResolver(testkit.Resolver{
				Addresses: []netip.Addr{
					netip.MustParseAddr("127.0.0.1"),
				},
			}),
			providerkit.WithDialContext(func(
				context.Context,
				string,
				string,
			) (net.Conn, error) {
				dialed = true
				return nil, errors.New("must not dial")
			}),
		)
		source, err := newDatadogClientSource(factory)
		if err != nil {
			t.Fatal(err)
		}
		result, err := newHandlers(source)["get_monitor"](
			t.Context(),
			datadogCall(
				"get_monitor",
				map[string]any{"monitor_id": float64(42)},
			),
		)
		if err != nil || result.Failure == nil ||
			result.Failure.Code != connector.FailurePolicyDenied ||
			dialed {
			t.Fatalf("private DNS result=%+v err=%v dialed=%v", result, err, dialed)
		}
	})
}
