package datadog

import (
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/smoketest"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	datadogSmokeSiteEnv           = "CONNECT_IT_DATADOG_SMOKE_SITE"
	datadogSmokeAPIKeyEnv         = "CONNECT_IT_DATADOG_SMOKE_API_KEY"
	datadogSmokeApplicationKeyEnv = "CONNECT_IT_DATADOG_SMOKE_APPLICATION_KEY"
	datadogSmokeMonitorIDEnv      = "CONNECT_IT_DATADOG_SMOKE_MONITOR_ID"
	datadogSmokeMetricNameEnv     = "CONNECT_IT_DATADOG_SMOKE_METRIC_NAME"

	datadogSmokeConnection = "datadog-real-smoke"
	datadogSmokeWindow     = 5 * time.Minute
)

var datadogSmokeMetricNamePattern = regexp.MustCompile(
	`^[A-Za-z][A-Za-z0-9_.]{0,199}$`,
)

// TestDatadogRealSmoke exercises every Datadog Tool against an explicitly
// configured real organization. Every operation is read-only, so there is no
// write gate and nothing to clean up. It never logs keys, the metric query, or
// Provider response bodies.
func TestDatadogRealSmoke(t *testing.T) {
	config := smoketest.Config(t, "Datadog", parseDatadogSmokeConfig)

	factory, err := providerkit.NewFactoryFromEnv()
	if err != nil {
		t.Fatal("construct Datadog provider client factory")
	}
	validators, err := NewCredentialValidators(factory)
	if err != nil {
		t.Fatal("construct Datadog credential validators")
	}
	handlers, err := NewHandlers(factory)
	if err != nil {
		t.Fatal("construct Datadog handlers")
	}

	fields := map[string]string{
		datadogAPIKeyField:         config.apiKey,
		datadogApplicationKeyField: config.applicationKey,
		datadogSiteField:           config.site,
	}
	validation, err := validators[datadogAuthMethod](
		t.Context(),
		connector.CredentialValidationInput{
			ConnectorType: Definition.Type,
			AuthMethodKey: datadogAuthMethod,
			AuthType:      connector.AuthCustomCredential,
			ConnectionID:  datadogSmokeConnection,
			Fields:        fields,
		},
	)
	if err != nil {
		t.Fatal("validate Datadog credentials")
	}
	if validation.ScopesKnown ||
		len(validation.GrantedScopes) != 0 ||
		validation.Profile.AccountID != "" ||
		validation.Profile.DisplayName == "" {
		t.Fatal("Datadog validator returned an invalid profile or scope snapshot")
	}

	definitionRegistry := smoketest.Registry(
		t,
		Definition,
		smoketest.ToolIDs(Definition)...,
	)
	to := time.Now().UTC().Unix()
	from := to - int64(datadogSmokeWindow/time.Second)
	calls := []struct {
		toolID    string
		arguments map[string]any
	}{
		{"list_monitors", map[string]any{
			"page":      int64(0),
			"page_size": int64(1),
		}},
		{"get_monitor", map[string]any{"monitor_id": config.monitorID}},
		{"list_active_metrics", map[string]any{"from": from}},
		{"get_metric_metadata", map[string]any{
			"metric_name": config.metricName,
		}},
		{"query_timeseries_points", map[string]any{
			"from":  from,
			"to":    to,
			"query": "avg:" + config.metricName + "{*}",
		}},
	}
	for _, call := range calls {
		smoketest.Run(t, definitionRegistry, handlers, smoketest.FieldCall(
			Definition,
			datadogSmokeConnection,
			call.toolID,
			call.arguments,
			fields,
		))
	}
}

type datadogSmokeConfig struct {
	site           string
	apiKey         string
	applicationKey string
	monitorID      int64
	metricName     string
}

func parseDatadogSmokeConfig(
	lookup smoketest.Lookup,
) (datadogSmokeConfig, bool, error) {
	values, configured, err := smoketest.Values(
		"Datadog",
		lookup,
		datadogSmokeSiteEnv,
		datadogSmokeAPIKeyEnv,
		datadogSmokeApplicationKeyEnv,
		datadogSmokeMonitorIDEnv,
		datadogSmokeMetricNameEnv,
	)
	if !configured || err != nil {
		return datadogSmokeConfig{}, configured, err
	}
	monitorID, parseErr := strconv.ParseInt(
		values[datadogSmokeMonitorIDEnv],
		10,
		64,
	)
	if parseErr != nil || monitorID < 1 || monitorID > 1<<53-1 {
		return datadogSmokeConfig{}, true, fmt.Errorf(
			"%s must be a positive JSON-safe integer",
			datadogSmokeMonitorIDEnv,
		)
	}
	metricName := values[datadogSmokeMetricNameEnv]
	if !datadogSmokeMetricNamePattern.MatchString(metricName) {
		return datadogSmokeConfig{}, true, fmt.Errorf(
			"%s must be a schema-safe metric name",
			datadogSmokeMetricNameEnv,
		)
	}
	return datadogSmokeConfig{
		site:           values[datadogSmokeSiteEnv],
		apiKey:         values[datadogSmokeAPIKeyEnv],
		applicationKey: values[datadogSmokeApplicationKeyEnv],
		monitorID:      monitorID,
		metricName:     metricName,
	}, true, nil
}

// The shared harness covers the env gate itself; this covers only the two
// Datadog values that reach a request target.
func TestDatadogSmokeConfigRejectsUnsafeResourceIdentifiers(t *testing.T) {
	t.Parallel()
	complete := map[string]string{
		datadogSmokeSiteEnv:           "us1",
		datadogSmokeAPIKeyEnv:         "api-key",
		datadogSmokeApplicationKeyEnv: "application-key",
		datadogSmokeMonitorIDEnv:      "42",
		datadogSmokeMetricNameEnv:     "connect_it.smoke",
	}
	for name, value := range map[string]string{
		datadogSmokeMonitorIDEnv:  "0",
		datadogSmokeMetricNameEnv: "connect it smoke",
	} {
		values := maps.Clone(complete)
		values[name] = value
		if _, _, err := parseDatadogSmokeConfig(
			smoketest.MapLookup(values),
		); err == nil {
			t.Errorf("%s accepted an unsafe value", name)
		}
	}
	config, configured, err := parseDatadogSmokeConfig(
		smoketest.MapLookup(complete),
	)
	if !configured || err != nil ||
		config.monitorID != 42 ||
		config.metricName != "connect_it.smoke" {
		t.Fatalf("complete Datadog smoke config = %+v err %v", config, err)
	}
}
