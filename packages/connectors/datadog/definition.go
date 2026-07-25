// Package datadog implements the Datadog API connector.
package datadog

import (
	"encoding/json"

	"github.com/memohai/connect-it/packages/core/connector"
)

func datadogStringPtr(value string) *string { return &value }

const monitorOutputSchema = `{
  "type": "object",
  "properties": {
    "id": {"type": ["integer", "null"], "minimum": 1},
    "name": {"type": ["string", "null"]},
    "type": {"type": ["string", "null"]},
    "query": {"type": ["string", "null"]},
    "message": {"type": ["string", "null"]},
    "tags": {
      "type": "array",
      "items": {"type": "string"}
    },
    "overall_state": {"type": ["string", "null"]}
  },
  "required": ["id", "name", "type", "query", "message", "tags", "overall_state"],
  "additionalProperties": false
}`

const metricMetadataOutputSchema = `{
  "type": "object",
  "properties": {
    "metric_name": {"type": "string"},
    "description": {"type": ["string", "null"]},
    "integration": {"type": ["string", "null"]},
    "per_unit": {"type": ["string", "null"]},
    "short_name": {"type": ["string", "null"]},
    "statsd_interval": {"type": ["integer", "null"], "minimum": 0},
    "type": {"type": ["string", "null"]},
    "unit": {"type": ["string", "null"]}
  },
  "required": [
    "metric_name",
    "description",
    "integration",
    "per_unit",
    "short_name",
    "statsd_interval",
    "type",
    "unit"
  ],
  "additionalProperties": false
}`

const timeseriesUnitOutputSchema = `{
  "anyOf": [
    {"type": "null"},
    {
      "type": "object",
      "properties": {
        "family": {"type": ["string", "null"]},
        "name": {"type": ["string", "null"]},
        "plural": {"type": ["string", "null"]},
        "scale_factor": {"type": ["number", "null"]},
        "short_name": {"type": ["string", "null"]}
      },
      "required": ["family", "name", "plural", "scale_factor", "short_name"],
      "additionalProperties": false
    }
  ]
}`

const timeseriesOutputSchema = `{
  "type": "object",
  "properties": {
    "metric": {"type": ["string", "null"]},
    "scope": {"type": ["string", "null"]},
    "expression": {"type": ["string", "null"]},
    "display_name": {"type": ["string", "null"]},
    "unit": {
      "type": "array",
      "items": ` + timeseriesUnitOutputSchema + `
    },
    "pointlist": {
      "type": "array",
      "items": {
        "type": "array",
        "prefixItems": [
          {
            "type": "integer",
            "minimum": 0,
            "maximum": 9007199254740991
          },
          {"type": ["number", "null"]}
        ],
        "items": false,
        "minItems": 2,
        "maxItems": 2
      }
    }
  },
  "required": ["metric", "scope", "expression", "display_name", "unit", "pointlist"],
  "additionalProperties": false
}`

var Definition = connector.Definition{
	Type:                "datadog",
	Name:                "Datadog",
	Description:         "Datadog 监控、指标与可观测性平台",
	Categories:          []string{"developer_tools", "data"},
	HomepageURL:         "https://www.datadoghq.com",
	IconURL:             "https://cdn.simpleicons.org/datadog",
	ConfigSchemaVersion: 1,

	AuthMethods: []connector.AuthMethod{
		{
			Key:   datadogAuthMethod,
			Type:  connector.AuthCustomCredential,
			Label: "API and Application Keys",
			CredentialFields: []connector.ConfigField{
				{
					Key:         datadogAPIKeyField,
					Label:       "API Key",
					InputType:   connector.InputText,
					Required:    true,
					Secret:      true,
					Description: "Datadog API Key；仅通过 DD-API-KEY header 发送。",
					Validation: connector.FieldValidation{
						Pattern: `^\S{1,1000}\S{0,1000}\S{0,1000}\S{0,1000}\S{0,96}$`,
					},
				},
				{
					Key:         datadogApplicationKeyField,
					Label:       "Application Key",
					InputType:   connector.InputText,
					Required:    true,
					Secret:      true,
					Description: "Datadog Application Key；仅通过 DD-APPLICATION-KEY header 发送。",
					Validation: connector.FieldValidation{
						Pattern: `^\S{1,1000}\S{0,1000}\S{0,1000}\S{0,1000}\S{0,96}$`,
					},
				},
				{
					Key:          datadogSiteField,
					Label:        "Datadog Site",
					InputType:    connector.InputSelect,
					Required:     true,
					DefaultValue: datadogStringPtr("us1"),
					Description:  "Datadog account 所属的固定官方站点；决定唯一 API origin。",
					Validation: connector.FieldValidation{
						Options: []string{
							"us1",
							"us3",
							"us5",
							"eu",
							"ap1",
							"ap2",
							"uk1",
							"us1_fed",
							"us2_fed",
						},
					},
				},
			},
		},
	},

	Tools: []connector.Tool{
		{
			ID:          "list_monitors",
			Name:        "List monitors",
			Description: "分页列出当前 Datadog organization 的 monitors。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "page": {
      "type": "integer",
      "minimum": 0,
      "maximum": 1000000,
      "default": 0,
      "description": "从 0 开始的 Datadog monitor 页码。"
    },
    "page_size": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100,
      "default": 100,
      "description": "每页 monitor 数量；首版最大 100。"
    },
    "group_states": {
      "type": "array",
      "minItems": 1,
      "maxItems": 4,
      "uniqueItems": true,
      "items": {"type": "string", "enum": ["all", "alert", "warn", "no data"]},
      "description": "要包含的 monitor group states。"
    },
    "name": {
      "type": "string",
      "minLength": 1,
      "maxLength": 256,
      "description": "按 monitor name 过滤。"
    },
    "tags": {
      "type": "array",
      "minItems": 1,
      "maxItems": 100,
      "items": {
        "type": "string",
        "minLength": 1,
        "maxLength": 200,
        "pattern": "^[^,\\x00-\\x1F\\x7F]{1,200}$"
      },
      "description": "按 scope tags 过滤；单个 tag 不得含逗号或控制字符。"
    },
    "monitor_tags": {
      "type": "array",
      "minItems": 1,
      "maxItems": 100,
      "items": {
        "type": "string",
        "minLength": 1,
        "maxLength": 200,
        "pattern": "^[^,\\x00-\\x1F\\x7F]{1,200}$"
      },
      "description": "按 monitor tags 过滤；单个 tag 不得含逗号或控制字符。"
    },
    "with_downtimes": {
      "type": "boolean",
      "description": "是否在结果中包含当前 active downtimes。"
    }
  },
  "additionalProperties": false
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "monitors": {
      "type": "array",
      "items": ` + monitorOutputSchema + `
    }
  },
  "required": ["monitors"],
  "additionalProperties": false
}`),
			RequiredScopes: []string{"monitors_read"},
			Risk:           connector.RiskRead,
			Backend:        connector.ManagedBackend{HandlerKey: "list_monitors"},
		},
		{
			ID:          "get_monitor",
			Name:        "Get monitor",
			Description: "按正整数 monitor ID 获取一个 Datadog monitor。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "monitor_id": {
      "type": "integer",
      "minimum": 1,
      "maximum": 9007199254740991,
      "description": "Datadog monitor ID。"
    },
    "group_states": {
      "type": "array",
      "minItems": 1,
      "maxItems": 4,
      "uniqueItems": true,
      "items": {"type": "string", "enum": ["all", "alert", "warn", "no data"]},
      "description": "要包含的 monitor group states。"
    },
    "with_downtimes": {
      "type": "boolean",
      "description": "是否在结果中包含当前 active downtimes。"
    }
  },
  "required": ["monitor_id"],
  "additionalProperties": false
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "monitor": ` + monitorOutputSchema + `
  },
  "required": ["monitor"],
  "additionalProperties": false
}`),
			RequiredScopes: []string{"monitors_read"},
			Risk:           connector.RiskRead,
			Backend:        connector.ManagedBackend{HandlerKey: "get_monitor"},
		},
		{
			ID:          "list_active_metrics",
			Name:        "List active metrics",
			Description: "列出指定 Unix 时间以来仍在上报的 Datadog metric names。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "from": {
      "type": "integer",
      "minimum": 0,
      "description": "查询起点，Unix timestamp（秒）。"
    },
    "host": {
      "type": "string",
      "minLength": 1,
      "maxLength": 253,
      "pattern": "^[^\\x00-\\x20\\x7F]{1,253}$",
      "description": "按 hostname tag 过滤。"
    },
    "tag_filter": {
      "type": "string",
      "minLength": 1,
      "maxLength": 4096,
      "description": "Datadog tag boolean/wildcard expression；不能与 host 同时使用。"
    }
  },
  "required": ["from"],
  "not": {"required": ["host", "tag_filter"]},
  "additionalProperties": false
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "from": {
      "type": "string",
      "pattern": "^(0|[1-9][0-9]{0,18})$"
    },
    "metrics": {
      "type": "array",
      "items": {"type": "string"}
    }
  },
  "required": ["from", "metrics"],
  "additionalProperties": false
}`),
			RequiredScopes: []string{"metrics_read"},
			Risk:           connector.RiskRead,
			Backend:        connector.ManagedBackend{HandlerKey: "list_active_metrics"},
		},
		{
			ID:          "get_metric_metadata",
			Name:        "Get metric metadata",
			Description: "获取一个 Datadog metric 的 v1 metadata。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "metric_name": {
      "type": "string",
      "minLength": 1,
      "maxLength": 200,
      "pattern": "^[A-Za-z][A-Za-z0-9_.]{0,199}$",
      "description": "Datadog metric name。"
    }
  },
  "required": ["metric_name"],
  "additionalProperties": false
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "metric": ` + metricMetadataOutputSchema + `
  },
  "required": ["metric"],
  "additionalProperties": false
}`),
			RequiredScopes: []string{"metrics_read"},
			Risk:           connector.RiskRead,
			Backend:        connector.ManagedBackend{HandlerKey: "get_metric_metadata"},
		},
		{
			ID:          "query_timeseries_points",
			Name:        "Query timeseries points",
			Description: "按 Datadog metric query 和 Unix 时间窗口查询 v1 timeseries points。",
			InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "from": {
      "type": "integer",
      "minimum": 0,
      "description": "查询窗口起点，Unix timestamp（秒）。"
    },
    "to": {
      "type": "integer",
      "minimum": 0,
      "description": "查询窗口终点，Unix timestamp（秒）；必须不早于 from，且 connect-it 首版窗口最长 31 天。"
    },
    "query": {
      "type": "string",
      "minLength": 1,
      "maxLength": 8192,
      "description": "Datadog timeseries metric query expression。"
    }
  },
  "required": ["from", "to", "query"],
  "additionalProperties": false
}`),
			OutputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "status": {"type": ["string", "null"]},
    "response_type": {"type": ["string", "null"]},
    "series": {
      "type": "array",
      "items": ` + timeseriesOutputSchema + `
    }
  },
  "required": ["status", "response_type", "series"],
  "additionalProperties": false
}`),
			RequiredScopes: []string{"timeseries_query"},
			Risk:           connector.RiskRead,
			Backend:        connector.ManagedBackend{HandlerKey: "query_timeseries_points"},
		},
	},
}
