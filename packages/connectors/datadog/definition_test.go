package datadog

import (
	"slices"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

func TestDatadogDefinitionCredentialsAndSites(t *testing.T) {
	t.Parallel()

	if len(Definition.ConfigFields) != 0 {
		t.Fatalf("Datadog 不应有 connector-global ConfigFields: %+v", Definition.ConfigFields)
	}
	if len(Definition.AuthMethods) != 1 {
		t.Fatalf("AuthMethods 数量 = %d, want 1", len(Definition.AuthMethods))
	}
	auth := Definition.AuthMethods[0]
	if auth.Key != datadogAuthMethod || auth.Type != connector.AuthCustomCredential || auth.OAuth != nil {
		t.Fatalf("AuthMethod 不正确: %+v", auth)
	}
	if len(auth.CredentialFields) != 3 {
		t.Fatalf("CredentialFields 数量 = %d, want 3", len(auth.CredentialFields))
	}

	apiKey := auth.CredentialFields[0]
	applicationKey := auth.CredentialFields[1]
	site := auth.CredentialFields[2]
	if apiKey.Key != datadogAPIKeyField || apiKey.InputType != connector.InputText ||
		!apiKey.Required || !apiKey.Secret || apiKey.DefaultValue != nil ||
		apiKey.Validation.Pattern == "" {
		t.Fatalf("api_key field 不正确: %+v", apiKey)
	}
	if applicationKey.Key != datadogApplicationKeyField || applicationKey.InputType != connector.InputText ||
		!applicationKey.Required || !applicationKey.Secret || applicationKey.DefaultValue != nil ||
		applicationKey.Validation.Pattern == "" {
		t.Fatalf("application_key field 不正确: %+v", applicationKey)
	}
	expectedSites := []string{
		"us1",
		"us3",
		"us5",
		"eu",
		"ap1",
		"ap2",
		"uk1",
		"us1_fed",
		"us2_fed",
	}
	if site.Key != datadogSiteField || site.InputType != connector.InputSelect ||
		!site.Required || site.Secret || site.DefaultValue == nil ||
		*site.DefaultValue != "us1" ||
		!slices.Equal(site.Validation.Options, expectedSites) {
		t.Fatalf("site field 不正确: %+v", site)
	}

	expectedOrigins := map[string]string{
		"us1":     "https://api.datadoghq.com:443/",
		"us3":     "https://api.us3.datadoghq.com:443/",
		"us5":     "https://api.us5.datadoghq.com:443/",
		"eu":      "https://api.datadoghq.eu:443/",
		"ap1":     "https://api.ap1.datadoghq.com:443/",
		"ap2":     "https://api.ap2.datadoghq.com:443/",
		"uk1":     "https://api.uk1.datadoghq.com:443/",
		"us1_fed": "https://api.ddog-gov.com:443/",
		"us2_fed": "https://api.us2.ddog-gov.com:443/",
	}
	if len(datadogSites) != len(expectedOrigins) {
		t.Fatalf("datadogSites 数量 = %d, want %d", len(datadogSites), len(expectedOrigins))
	}
	for siteValue, want := range expectedOrigins {
		if got := datadogSites[siteValue]; got != want {
			t.Errorf("datadogSites[%q] = %q, want %q", siteValue, got, want)
		}
	}
}

func TestDatadogDefinitionToolsAndScopes(t *testing.T) {
	t.Parallel()

	expected := []struct {
		id    string
		scope string
	}{
		{id: "list_monitors", scope: "monitors_read"},
		{id: "get_monitor", scope: "monitors_read"},
		{id: "list_active_metrics", scope: "metrics_read"},
		{id: "get_metric_metadata", scope: "metrics_read"},
		{id: "query_timeseries_points", scope: "timeseries_query"},
	}
	if len(Definition.Tools) != len(expected) {
		t.Fatalf("Tools 数量 = %d, want %d", len(Definition.Tools), len(expected))
	}
	for index, want := range expected {
		tool := Definition.Tools[index]
		if tool.ID != want.id || tool.Risk != connector.RiskRead ||
			!slices.Equal(tool.RequiredScopes, []string{want.scope}) {
			t.Fatalf("Tools[%d] = id=%q risk=%q scopes=%v", index, tool.ID, tool.Risk, tool.RequiredScopes)
		}
		backend, ok := tool.Backend.(connector.ManagedBackend)
		if !ok || backend.HandlerKey != tool.ID {
			t.Fatalf("Tool %q backend = %#v, want same-name ManagedBackend", tool.ID, tool.Backend)
		}
		if len(tool.InputSchema) == 0 || len(tool.OutputSchema) == 0 {
			t.Fatalf("Tool %q 必须声明 input/output schema", tool.ID)
		}
	}
}

func TestDatadogDefinitionRegistersAndSchemasAreStrict(t *testing.T) {
	t.Parallel()

	reg := registry.New()
	if err := reg.Register(Definition, datadogDefinitionHandlerKeys()...); err != nil {
		t.Fatalf("Datadog Definition 注册失败: %v", err)
	}

	minimalInputs := map[string]map[string]any{
		"list_monitors":           {},
		"get_monitor":             {"monitor_id": int64(1)},
		"list_active_metrics":     {"from": int64(0)},
		"get_metric_metadata":     {"metric_name": "system.cpu.user"},
		"query_timeseries_points": {"from": int64(0), "to": int64(1), "query": "avg:system.cpu.user{*}"},
	}
	for _, tool := range Definition.Tools {
		schemas, exists := reg.ToolSchemas(Definition.Type, tool.ID)
		if !exists || schemas.Input == nil || schemas.Output == nil {
			t.Fatalf("Tool %q 缺 compiled input/output schema", tool.ID)
		}
		valid := minimalInputs[tool.ID]
		if err := schemas.Input.Validate(valid); err != nil {
			t.Fatalf("Tool %q 最小合法 input 被拒绝: %v", tool.ID, err)
		}
		withUnknown := make(map[string]any, len(valid)+1)
		for key, value := range valid {
			withUnknown[key] = value
		}
		withUnknown["unexpected"] = true
		if err := schemas.Input.Validate(withUnknown); err == nil {
			t.Fatalf("Tool %q input schema 应拒绝未知字段", tool.ID)
		}
	}
}

func TestDatadogMonitorInputBoundsAndDefaults(t *testing.T) {
	t.Parallel()

	reg := registeredDatadogDefinition(t)
	listSchemas, _ := reg.ToolSchemas(Definition.Type, "list_monitors")
	input := map[string]any{}
	if err := listSchemas.Input.ApplyDefaultsAndValidate(input); err != nil {
		t.Fatalf("list_monitors defaults 不合法: %v", err)
	}
	if input["page"] != float64(0) || input["page_size"] != float64(100) {
		t.Fatalf("list_monitors defaults = %#v", input)
	}
	if err := listSchemas.Input.Validate(map[string]any{
		"page":           int64(2),
		"page_size":      int64(100),
		"group_states":   []any{"alert", "no data"},
		"tags":           []any{"env:prod"},
		"monitor_tags":   []any{"service:web"},
		"with_downtimes": true,
	}); err != nil {
		t.Fatalf("合法 list_monitors input 被拒绝: %v", err)
	}
	for _, invalid := range []map[string]any{
		{"page": int64(-1)},
		{"page_size": int64(101)},
		{"group_states": []any{"ok"}},
		{"group_states": []any{"alert", "alert"}},
		{"tags": []any{"env:prod,service:web"}},
		{"monitor_tags": []any{"line\nbreak"}},
	} {
		if err := listSchemas.Input.Validate(invalid); err == nil {
			t.Errorf("非法 list_monitors input 应被拒绝: %#v", invalid)
		}
	}

	getSchemas, _ := reg.ToolSchemas(Definition.Type, "get_monitor")
	if err := getSchemas.Input.Validate(map[string]any{"monitor_id": int64(0)}); err == nil {
		t.Fatal("monitor_id=0 应被拒绝")
	}
}

func TestDatadogMetricInputConstraints(t *testing.T) {
	t.Parallel()

	reg := registeredDatadogDefinition(t)
	activeSchemas, _ := reg.ToolSchemas(Definition.Type, "list_active_metrics")
	if err := activeSchemas.Input.Validate(map[string]any{
		"from":       int64(1),
		"host":       "host.example",
		"tag_filter": "env:prod",
	}); err == nil {
		t.Fatal("host 和 tag_filter 同时出现应被拒绝")
	}
	if err := activeSchemas.Input.Validate(map[string]any{
		"from":       int64(1),
		"tag_filter": "env IN (prod,staging)",
	}); err != nil {
		t.Fatalf("合法 tag_filter 被拒绝: %v", err)
	}

	metadataSchemas, _ := reg.ToolSchemas(Definition.Type, "get_metric_metadata")
	for _, metricName := range []string{"system.cpu.user", "custom_metric.v2", "A"} {
		if err := metadataSchemas.Input.Validate(map[string]any{"metric_name": metricName}); err != nil {
			t.Errorf("metric_name %q 应合法: %v", metricName, err)
		}
	}
	for _, metricName := range []string{"1metric", "system/cpu", "system%2Ecpu", "system cpu"} {
		if err := metadataSchemas.Input.Validate(map[string]any{"metric_name": metricName}); err == nil {
			t.Errorf("metric_name %q 应被拒绝", metricName)
		}
	}
}

func TestDatadogStrictOutputShapes(t *testing.T) {
	t.Parallel()

	reg := registeredDatadogDefinition(t)
	monitor := map[string]any{
		"id":            int64(42),
		"name":          "CPU",
		"type":          "metric alert",
		"query":         "avg(last_5m):avg:system.cpu.user{*} > 90",
		"message":       nil,
		"tags":          []any{"env:prod"},
		"overall_state": "OK",
	}
	listSchemas, _ := reg.ToolSchemas(Definition.Type, "list_monitors")
	if err := listSchemas.Output.Validate(map[string]any{"monitors": []any{monitor}}); err != nil {
		t.Fatalf("合法 list_monitors output 被拒绝: %v", err)
	}
	monitor["unexpected"] = true
	if err := listSchemas.Output.Validate(map[string]any{"monitors": []any{monitor}}); err == nil {
		t.Fatal("monitor output 未拒绝未知字段")
	}
	delete(monitor, "unexpected")

	activeSchemas, _ := reg.ToolSchemas(Definition.Type, "list_active_metrics")
	if err := activeSchemas.Output.Validate(map[string]any{
		"from":    "1720000000",
		"metrics": []any{"system.cpu.user"},
	}); err != nil {
		t.Fatalf("合法 list_active_metrics output 被拒绝: %v", err)
	}

	metadataSchemas, _ := reg.ToolSchemas(Definition.Type, "get_metric_metadata")
	if err := metadataSchemas.Output.Validate(map[string]any{
		"metric": map[string]any{
			"metric_name":     "system.cpu.user",
			"description":     nil,
			"integration":     "system",
			"per_unit":        nil,
			"short_name":      "cpu user",
			"statsd_interval": nil,
			"type":            "gauge",
			"unit":            "percent",
		},
	}); err != nil {
		t.Fatalf("合法 get_metric_metadata output 被拒绝: %v", err)
	}

	querySchemas, _ := reg.ToolSchemas(Definition.Type, "query_timeseries_points")
	validQueryOutput := map[string]any{
		"status":        "ok",
		"response_type": "time_series",
		"series": []any{
			map[string]any{
				"metric":       "system.cpu.user",
				"scope":        "*",
				"expression":   "avg:system.cpu.user{*}",
				"display_name": "system.cpu.user",
				"unit": []any{
					map[string]any{
						"family":       "percentage",
						"name":         "percent",
						"plural":       "percent",
						"scale_factor": float64(1),
						"short_name":   "%",
					},
					nil,
				},
				"pointlist": []any{
					[]any{float64(1720000000000), float64(7.5)},
					[]any{float64(1720000060000), nil},
				},
			},
		},
	}
	if err := querySchemas.Output.Validate(validQueryOutput); err != nil {
		t.Fatalf("合法 query_timeseries_points output 被拒绝: %v", err)
	}
	for _, invalidTimestamp := range []any{nil, float64(1720000000000.5)} {
		invalidPointOutput := map[string]any{
			"status":        "ok",
			"response_type": "time_series",
			"series": []any{
				map[string]any{
					"metric":       "system.cpu.user",
					"scope":        "*",
					"expression":   "avg:system.cpu.user{*}",
					"display_name": "system.cpu.user",
					"unit":         []any{},
					"pointlist": []any{
						[]any{invalidTimestamp, float64(7.5)},
					},
				},
			},
		}
		if err := querySchemas.Output.Validate(invalidPointOutput); err == nil {
			t.Errorf(
				"query output 应拒绝非法 timestamp %#v",
				invalidTimestamp,
			)
		}
	}
	for _, invalidPoint := range []any{
		[]any{float64(1720000000000)},
		[]any{
			float64(1720000000000),
			float64(7.5),
			float64(8.5),
		},
		[]any{float64(1720000000000), "not-a-number"},
	} {
		invalidPointOutput := map[string]any{
			"status":        "ok",
			"response_type": "time_series",
			"series": []any{
				map[string]any{
					"metric":       "system.cpu.user",
					"scope":        "*",
					"expression":   "avg:system.cpu.user{*}",
					"display_name": "system.cpu.user",
					"unit":         []any{},
					"pointlist":    []any{invalidPoint},
				},
			},
		}
		if err := querySchemas.Output.Validate(invalidPointOutput); err == nil {
			t.Errorf("query output 应拒绝非法 point %#v", invalidPoint)
		}
	}
	validQueryOutput["raw"] = map[string]any{}
	if err := querySchemas.Output.Validate(validQueryOutput); err == nil {
		t.Fatal("query output 未拒绝 raw/未知字段")
	}
}

func registeredDatadogDefinition(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New()
	if err := reg.Register(Definition, datadogDefinitionHandlerKeys()...); err != nil {
		t.Fatalf("Datadog Definition 注册失败: %v", err)
	}
	return reg
}

func datadogDefinitionHandlerKeys() []string {
	keys := make([]string, 0, len(Definition.Tools))
	for _, tool := range Definition.Tools {
		keys = append(keys, tool.ID)
	}
	return keys
}
