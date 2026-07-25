package datadog

import (
	"context"
	"encoding/json"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/memohai/connect-it/packages/connectors/internal/restkit"
	"github.com/memohai/connect-it/packages/connectors/internal/toolargs"
	"github.com/memohai/connect-it/packages/connectors/internal/toolfail"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

type managedHandler struct {
	transport restkit.Transport[datadogCredentials]
}

func NewHandlers(
	factory *providerkit.Factory,
) (connector.HandlerMap, error) {
	source, err := newDatadogClientSource(factory)
	if err != nil {
		return nil, err
	}
	return newHandlers(source), nil
}

func newHandlers(source *datadogClientSource) connector.HandlerMap {
	handler := &managedHandler{transport: source.transport()}
	return connector.HandlerMap{
		"list_monitors":           handler.listMonitors,
		"get_monitor":             handler.getMonitor,
		"list_active_metrics":     handler.listActiveMetrics,
		"get_metric_metadata":     handler.getMetricMetadata,
		"query_timeseries_points": handler.queryTimeseriesPoints,
	}
}

// invalidInput is the finished result for every argument rejected before any
// egress happens.
func invalidInput() (connector.ToolResultData, error) {
	return toolfail.Result(connector.FailureInvalidInput, 0, 0), nil
}

// invalidResponse is the finished result for a Provider payload that does not
// match the Tool's declared output shape.
func invalidResponse(
	response *providerkit.Response,
) connector.ToolResultData {
	return toolfail.Result(
		connector.FailureInvalidResponse,
		toolargs.ResponseStatus(response),
		0,
	)
}

// get issues one guarded GET, the only method any Datadog Tool uses.
func (handler *managedHandler) get(
	ctx context.Context,
	call connector.ToolCallContext,
	path string,
	query url.Values,
) (*providerkit.Response, connector.ToolResultData, bool) {
	return handler.transport.Do(ctx, call, restkit.Request{
		Method: http.MethodGet,
		Path:   path,
		Query:  query,
	})
}

type datadogMonitor struct {
	ID           *int64   `json:"id"`
	Name         *string  `json:"name"`
	Type         *string  `json:"type"`
	Query        *string  `json:"query"`
	Message      *string  `json:"message"`
	Tags         []string `json:"tags"`
	OverallState *string  `json:"overall_state"`
}

func (handler *managedHandler) listMonitors(
	ctx context.Context,
	call connector.ToolCallContext,
) (connector.ToolResultData, error) {
	page, ok := toolargs.OptionalInteger(
		call.Arguments,
		"page",
		0,
		0,
		1_000_000,
	)
	if !ok {
		return invalidInput()
	}
	pageSize, ok := toolargs.OptionalInteger(
		call.Arguments,
		"page_size",
		100,
		1,
		100,
	)
	if !ok {
		return invalidInput()
	}
	query := url.Values{
		"page":      {strconv.FormatInt(page, 10)},
		"page_size": {strconv.FormatInt(pageSize, 10)},
	}
	if !optionalGroupStates(query, call.Arguments) ||
		!optionalStringQuery(
			query,
			call.Arguments,
			"name",
			"name",
			256,
		) ||
		!optionalStringListQuery(
			query,
			call.Arguments,
			"tags",
			"tags",
			100,
			200,
		) ||
		!optionalStringListQuery(
			query,
			call.Arguments,
			"monitor_tags",
			"monitor_tags",
			100,
			200,
		) ||
		!toolargs.OptionalBoolQuery(
			query,
			call.Arguments,
			"with_downtimes",
			"with_downtimes",
		) {
		return invalidInput()
	}
	response, result, ok := handler.get(ctx, call, "api/v1/monitor", query)
	if !ok {
		return result, nil
	}
	var items []json.RawMessage
	if err := response.DecodeJSON(&items); err != nil {
		return handler.transport.Fail(err), nil
	}
	if items == nil {
		return invalidResponse(response), nil
	}
	monitors := make([]datadogMonitor, 0, len(items))
	for _, item := range items {
		monitor, valid := decodeMonitor(item)
		if !valid {
			return invalidResponse(response), nil
		}
		monitors = append(monitors, monitor)
	}
	return structuredResult(map[string]any{"monitors": monitors})
}

func (handler *managedHandler) getMonitor(
	ctx context.Context,
	call connector.ToolCallContext,
) (connector.ToolResultData, error) {
	monitorID, ok := toolargs.PositiveInteger(call.Arguments["monitor_id"])
	if !ok {
		return invalidInput()
	}
	query := make(url.Values)
	if !optionalGroupStates(query, call.Arguments) ||
		!toolargs.OptionalBoolQuery(
			query,
			call.Arguments,
			"with_downtimes",
			"with_downtimes",
		) {
		return invalidInput()
	}
	response, result, ok := handler.get(
		ctx,
		call,
		"api/v1/monitor/"+strconv.FormatInt(monitorID, 10),
		query,
	)
	if !ok {
		return result, nil
	}
	monitor, valid := decodeMonitor(response.Body)
	if !valid {
		return invalidResponse(response), nil
	}
	return structuredResult(map[string]any{"monitor": monitor})
}

func decodeMonitor(raw json.RawMessage) (datadogMonitor, bool) {
	if !jsonObject(raw) {
		return datadogMonitor{}, false
	}
	var monitor datadogMonitor
	if err := json.Unmarshal(raw, &monitor); err != nil {
		return datadogMonitor{}, false
	}
	if monitor.Tags == nil {
		monitor.Tags = []string{}
	}
	if monitor.ID != nil &&
		(*monitor.ID < 1 || *monitor.ID > toolargs.MaxSafeInteger) ||
		!toolargs.OptionalOutputString(monitor.Name, 1<<20) ||
		!toolargs.OptionalOutputString(monitor.Type, 1<<16) ||
		!toolargs.OptionalOutputString(monitor.Query, 1<<20) ||
		!toolargs.OptionalOutputString(monitor.Message, 1<<20) ||
		!toolargs.OptionalOutputString(monitor.OverallState, 1<<16) ||
		!validOutputStrings(monitor.Tags, 10000, 1<<16) {
		return datadogMonitor{}, false
	}
	return monitor, true
}

type datadogMetricMetadata struct {
	MetricName     string  `json:"metric_name"`
	Description    *string `json:"description"`
	Integration    *string `json:"integration"`
	PerUnit        *string `json:"per_unit"`
	ShortName      *string `json:"short_name"`
	StatsdInterval *int64  `json:"statsd_interval"`
	Type           *string `json:"type"`
	Unit           *string `json:"unit"`
}

func (handler *managedHandler) listActiveMetrics(
	ctx context.Context,
	call connector.ToolCallContext,
) (connector.ToolResultData, error) {
	from, ok := nonNegativeInteger(call.Arguments["from"])
	if !ok {
		return invalidInput()
	}
	query := url.Values{"from": {strconv.FormatInt(from, 10)}}
	_, hostPresent := call.Arguments["host"]
	_, tagPresent := call.Arguments["tag_filter"]
	if hostPresent && tagPresent ||
		!optionalHostQuery(query, call.Arguments) ||
		!optionalStringQuery(
			query,
			call.Arguments,
			"tag_filter",
			"tag_filter",
			4096,
		) {
		return invalidInput()
	}
	response, result, ok := handler.get(ctx, call, "api/v1/metrics", query)
	if !ok {
		return result, nil
	}
	var payload struct {
		From    string   `json:"from"`
		Metrics []string `json:"metrics"`
	}
	if err := response.DecodeJSON(&payload); err != nil {
		return handler.transport.Fail(err), nil
	}
	if payload.Metrics == nil ||
		!validUnixString(payload.From) ||
		!validOutputStrings(payload.Metrics, 100000, 200) {
		return invalidResponse(response), nil
	}
	return structuredResult(map[string]any{
		"from":    payload.From,
		"metrics": payload.Metrics,
	})
}

func (handler *managedHandler) getMetricMetadata(
	ctx context.Context,
	call connector.ToolCallContext,
) (connector.ToolResultData, error) {
	metricName, ok := requiredMetricName(call.Arguments, "metric_name")
	if !ok {
		return invalidInput()
	}
	response, result, ok := handler.get(
		ctx,
		call,
		"api/v1/metrics/"+providerkit.PathSegment(metricName),
		nil,
	)
	if !ok {
		return result, nil
	}
	if !jsonObject(response.Body) {
		return invalidResponse(response), nil
	}
	var upstream struct {
		Description    *string `json:"description"`
		Integration    *string `json:"integration"`
		PerUnit        *string `json:"per_unit"`
		ShortName      *string `json:"short_name"`
		StatsdInterval *int64  `json:"statsd_interval"`
		Type           *string `json:"type"`
		Unit           *string `json:"unit"`
	}
	if err := json.Unmarshal(response.Body, &upstream); err != nil {
		return handler.transport.Fail(err), nil
	}
	metadata := datadogMetricMetadata{
		MetricName:     metricName,
		Description:    upstream.Description,
		Integration:    upstream.Integration,
		PerUnit:        upstream.PerUnit,
		ShortName:      upstream.ShortName,
		StatsdInterval: upstream.StatsdInterval,
		Type:           upstream.Type,
		Unit:           upstream.Unit,
	}
	if metadata.StatsdInterval != nil && *metadata.StatsdInterval < 0 ||
		!toolargs.OptionalOutputString(metadata.Description, 1<<20) ||
		!toolargs.OptionalOutputString(metadata.Integration, 1<<16) ||
		!toolargs.OptionalOutputString(metadata.PerUnit, 1<<16) ||
		!toolargs.OptionalOutputString(metadata.ShortName, 1<<16) ||
		!toolargs.OptionalOutputString(metadata.Type, 1<<16) ||
		!toolargs.OptionalOutputString(metadata.Unit, 1<<16) {
		return invalidResponse(response), nil
	}
	return structuredResult(map[string]any{"metric": metadata})
}

type datadogUnit struct {
	Family      *string  `json:"family"`
	Name        *string  `json:"name"`
	Plural      *string  `json:"plural"`
	ScaleFactor *float64 `json:"scale_factor"`
	ShortName   *string  `json:"short_name"`
}

type datadogSeries struct {
	Metric      *string        `json:"metric"`
	Scope       *string        `json:"scope"`
	Expression  *string        `json:"expression"`
	DisplayName *string        `json:"display_name"`
	Unit        []*datadogUnit `json:"unit"`
	Pointlist   [][2]any       `json:"pointlist"`
}

func (handler *managedHandler) queryTimeseriesPoints(
	ctx context.Context,
	call connector.ToolCallContext,
) (connector.ToolResultData, error) {
	from, fromOK := nonNegativeInteger(call.Arguments["from"])
	to, toOK := nonNegativeInteger(call.Arguments["to"])
	metricQuery, queryOK := requiredInputString(
		call.Arguments,
		"query",
		8192,
	)
	if !fromOK || !toOK || !queryOK || from > to ||
		to-from > datadogMaxQueryWindowSecs {
		return invalidInput()
	}
	response, result, ok := handler.get(
		ctx,
		call,
		"api/v1/query",
		url.Values{
			"from":  {strconv.FormatInt(from, 10)},
			"to":    {strconv.FormatInt(to, 10)},
			"query": {metricQuery},
		},
	)
	if !ok {
		return result, nil
	}
	if !jsonObject(response.Body) {
		return invalidResponse(response), nil
	}
	var raw struct {
		Status  *string           `json:"status"`
		ResType *string           `json:"res_type"`
		Series  []json.RawMessage `json:"series"`
	}
	if err := response.DecodeJSON(&raw); err != nil {
		return handler.transport.Fail(err), nil
	}
	if raw.Series == nil {
		raw.Series = []json.RawMessage{}
	}
	if raw.Status == nil ||
		!toolargs.OptionalOutputString(raw.Status, 1<<16) ||
		!toolargs.OptionalOutputString(raw.ResType, 1<<16) {
		return invalidResponse(response), nil
	}
	if *raw.Status != "ok" {
		return toolfail.Result(
			connector.FailureProviderError,
			response.StatusCode,
			0,
		), nil
	}
	series := make([]datadogSeries, 0, len(raw.Series))
	for _, item := range raw.Series {
		decoded, valid := decodeSeries(item)
		if !valid {
			return invalidResponse(response), nil
		}
		series = append(series, decoded)
	}
	return structuredResult(map[string]any{
		"status":        raw.Status,
		"response_type": raw.ResType,
		"series":        series,
	})
}

func decodeSeries(raw json.RawMessage) (datadogSeries, bool) {
	if !jsonObject(raw) {
		return datadogSeries{}, false
	}
	var upstream struct {
		Metric      *string           `json:"metric"`
		Scope       *string           `json:"scope"`
		Expression  *string           `json:"expression"`
		DisplayName *string           `json:"display_name"`
		Unit        []*datadogUnit    `json:"unit"`
		Pointlist   []json.RawMessage `json:"pointlist"`
	}
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return datadogSeries{}, false
	}
	if upstream.Unit == nil {
		upstream.Unit = []*datadogUnit{}
	}
	if upstream.Pointlist == nil {
		upstream.Pointlist = []json.RawMessage{}
	}
	series := datadogSeries{
		Metric:      upstream.Metric,
		Scope:       upstream.Scope,
		Expression:  upstream.Expression,
		DisplayName: upstream.DisplayName,
		Unit:        upstream.Unit,
		Pointlist:   make([][2]any, 0, len(upstream.Pointlist)),
	}
	if !toolargs.OptionalOutputString(series.Metric, 1<<16) ||
		!toolargs.OptionalOutputString(series.Scope, 1<<20) ||
		!toolargs.OptionalOutputString(series.Expression, 1<<20) ||
		!toolargs.OptionalOutputString(series.DisplayName, 1<<16) {
		return datadogSeries{}, false
	}
	for _, unit := range series.Unit {
		if unit == nil {
			continue
		}
		if unit.ScaleFactor != nil &&
			(math.IsNaN(*unit.ScaleFactor) ||
				math.IsInf(*unit.ScaleFactor, 0)) ||
			!toolargs.OptionalOutputString(unit.Family, 1<<16) ||
			!toolargs.OptionalOutputString(unit.Name, 1<<16) ||
			!toolargs.OptionalOutputString(unit.Plural, 1<<16) ||
			!toolargs.OptionalOutputString(unit.ShortName, 1<<16) {
			return datadogSeries{}, false
		}
	}
	for _, rawPoint := range upstream.Pointlist {
		var rawValues []json.RawMessage
		if err := json.Unmarshal(rawPoint, &rawValues); err != nil ||
			len(rawValues) != 2 ||
			string(rawValues[0]) == "null" {
			return datadogSeries{}, false
		}
		timestamp, valid := safeJSONTimestamp(rawValues[0])
		if !valid {
			return datadogSeries{}, false
		}
		point := [2]any{json.Number(strconv.FormatInt(timestamp, 10)), nil}
		if string(rawValues[1]) != "null" {
			value, err := strconv.ParseFloat(string(rawValues[1]), 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return datadogSeries{}, false
			}
			point[1] = json.Number(string(rawValues[1]))
		}
		series.Pointlist = append(series.Pointlist, point)
	}
	return series, true
}

func safeJSONTimestamp(raw json.RawMessage) (int64, bool) {
	literal := strings.TrimSpace(string(raw))
	if len(literal) == 0 || len(literal) > 128 ||
		(literal[0] != '-' &&
			(literal[0] < '0' || literal[0] > '9')) {
		return 0, false
	}
	if exponentAt := strings.IndexAny(literal, "eE"); exponentAt >= 0 {
		exponent, err := strconv.ParseInt(
			literal[exponentAt+1:],
			10,
			16,
		)
		if err != nil || exponent < -128 || exponent > 128 {
			return 0, false
		}
	}
	var number json.Number
	if err := json.Unmarshal([]byte(literal), &number); err != nil {
		return 0, false
	}
	rational, ok := new(big.Rat).SetString(number.String())
	if !ok || !rational.IsInt() || rational.Sign() < 0 ||
		!rational.Num().IsInt64() {
		return 0, false
	}
	timestamp := rational.Num().Int64()
	return timestamp, timestamp <= toolargs.MaxSafeInteger
}

func structuredResult(value any) (connector.ToolResultData, error) {
	structured, err := json.Marshal(value)
	if err != nil {
		return toolfail.Result(connector.FailureInternalError, 0, 0), nil
	}
	return connector.ToolResultData{Structured: structured}, nil
}

func nonNegativeInteger(value any) (int64, bool) {
	integer, ok := toolargs.Integer(value)
	return integer, ok && integer >= 0
}

// requiredInputString reads a mandatory argument that Datadog transmits
// verbatim, so a padded or empty value is rejected rather than trimmed.
func requiredInputString(
	arguments map[string]any,
	key string,
	maxRunes int,
) (string, bool) {
	text, ok := arguments[key].(string)
	if !ok || !toolargs.SimpleString(text, maxRunes) {
		return "", false
	}
	return text, true
}

// optionalStringQuery is toolargs.OptionalStringQuery plus Datadog's trimmed
// value requirement.
func optionalStringQuery(
	query url.Values,
	arguments map[string]any,
	argumentKey string,
	queryKey string,
	maxRunes int,
) bool {
	if _, present := arguments[argumentKey]; !present {
		return true
	}
	text, ok := requiredInputString(arguments, argumentKey, maxRunes)
	if !ok {
		return false
	}
	query.Set(queryKey, text)
	return true
}

// optionalHostQuery additionally refuses an embedded space, which a hostname
// can never contain and which would otherwise split the query parameter.
func optionalHostQuery(
	query url.Values,
	arguments map[string]any,
) bool {
	if _, present := arguments["host"]; !present {
		return true
	}
	host, ok := requiredInputString(arguments, "host", 253)
	if !ok || strings.ContainsRune(host, ' ') {
		return false
	}
	query.Set("host", host)
	return true
}

func optionalGroupStates(
	query url.Values,
	arguments map[string]any,
) bool {
	value, present := arguments["group_states"]
	if !present {
		return true
	}
	values, ok := stringList(value, 4, 16)
	if !ok {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, state := range values {
		switch state {
		case "all", "alert", "warn", "no data":
		default:
			return false
		}
		if _, duplicate := seen[state]; duplicate {
			return false
		}
		seen[state] = struct{}{}
	}
	query.Set("group_states", strings.Join(values, ","))
	return true
}

func optionalStringListQuery(
	query url.Values,
	arguments map[string]any,
	argumentKey string,
	queryKey string,
	maxItems int,
	maxRunes int,
) bool {
	value, present := arguments[argumentKey]
	if !present {
		return true
	}
	values, ok := stringList(value, maxItems, maxRunes)
	if !ok {
		return false
	}
	query.Set(queryKey, strings.Join(values, ","))
	return true
}

// stringList is toolargs.StringList narrowed to what Datadog joins into one
// comma-separated query parameter: at least one item, every item trimmed.
func stringList(
	value any,
	maxItems int,
	maxRunes int,
) ([]string, bool) {
	values, ok := toolargs.StringList(value, maxItems, maxRunes, true)
	if !ok || len(values) == 0 {
		return nil, false
	}
	for _, item := range values {
		if !toolargs.SimpleString(item, maxRunes) {
			return nil, false
		}
	}
	return values, true
}

func requiredMetricName(
	arguments map[string]any,
	key string,
) (string, bool) {
	metric, ok := requiredInputString(arguments, key, 200)
	if !ok || metric[0] < 'A' ||
		(metric[0] > 'Z' && metric[0] < 'a') ||
		metric[0] > 'z' {
		return "", false
	}
	for index := 1; index < len(metric); index++ {
		character := metric[index]
		if character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' ||
			character == '_' || character == '.' {
			continue
		}
		return "", false
	}
	return metric, true
}

func jsonObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return len(trimmed) >= 2 &&
		trimmed[0] == '{' &&
		trimmed[len(trimmed)-1] == '}'
}

func validUnixString(value string) bool {
	if value == "" {
		return false
	}
	integer, err := strconv.ParseInt(value, 10, 64)
	return err == nil && integer >= 0 &&
		strconv.FormatInt(integer, 10) == value
}

func validOutputStrings(
	values []string,
	maxItems int,
	maxRunes int,
) bool {
	if len(values) > maxItems {
		return false
	}
	for _, value := range values {
		// Same predicate as OptionalOutputString, without the pointer these
		// always-present strings do not need.
		if !toolargs.SafeText(value, maxRunes, true) {
			return false
		}
	}
	return true
}
