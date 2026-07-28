// Package catalogsvc 把代码 Registry 与配置状态合并成对外 catalog，
// 状态由 status.Compute 实时计算，永不包含 Secret。
package catalogsvc

import (
	"context"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/core/status"
	"github.com/memohai/connect-it/packages/service/configsvc"
)

type Item struct {
	Type        string              `json:"type"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Categories  []string            `json:"categories"`
	HomepageURL string              `json:"homepage_url"`
	IconURL     string              `json:"icon_url"`
	Mode        connector.Mode      `json:"mode,omitempty"`
	Status      status.Status       `json:"status"`
	AuthMethods []AuthMethodSummary `json:"auth_methods"`
}

// AuthMethodSummary 是下游创建 Connection 所需的非敏感认证元数据。
type AuthMethodSummary struct {
	Key              string                   `json:"key"`
	Label            string                   `json:"label"`
	Type             connector.AuthMethodType `json:"type"`
	CredentialFields []CredentialField        `json:"credential_fields"`
}

// CredentialField 是创建 api_key / custom_credential Connection 时所需的
// 非敏感表单元数据。它只描述字段约束，不包含任何用户提交的凭证值。
type CredentialField struct {
	Key          string                    `json:"key"`
	Label        string                    `json:"label"`
	InputType    connector.ConfigInputType `json:"input_type"`
	Required     bool                      `json:"required"`
	Secret       bool                      `json:"secret"`
	DefaultValue *string                   `json:"default_value"`
	Description  string                    `json:"description"`
	Pattern      string                    `json:"pattern"`
	Options      []string                  `json:"options"`
}

type Service struct {
	reg *registry.Registry
	cfg *configsvc.Service
}

func New(reg *registry.Registry, cfg *configsvc.Service) *Service {
	return &Service{reg: reg, cfg: cfg}
}

// List 返回全部已注册 Connector，外加数据库中存在但代码已不认识的
// definition_missing 条目；保留原配置，代码回滚后仍可继续使用。
func (s *Service) List(ctx context.Context) ([]Item, error) {
	states, err := s.cfg.ConfigStates(ctx)
	if err != nil {
		return nil, err
	}
	out := []Item{}
	seen := map[string]bool{}
	for _, def := range s.reg.All() {
		out = append(out, s.item(def, states[def.Type]))
		seen[string(def.Type)] = true
	}
	for connectorType := range states {
		if seen[string(connectorType)] {
			continue
		}
		out = append(out, Item{
			Type: string(connectorType), Categories: []string{}, AuthMethods: []AuthMethodSummary{},
			Status: status.DefinitionMissing,
		})
	}
	sortItems(out)
	return out, nil
}

// Get 返回单个 Connector；未知 type 且无配置行时返回 configsvc.ErrNotFound。
func (s *Service) Get(ctx context.Context, t connector.Type) (Item, error) {
	def, ok := s.reg.Get(t)
	if !ok {
		cfgState, err := s.cfg.ConfigState(ctx, t)
		if err != nil {
			return Item{}, err
		}
		if !cfgState.Exists {
			return Item{}, configsvc.ErrNotFound
		}
		return Item{
			Type: string(t), Categories: []string{}, AuthMethods: []AuthMethodSummary{},
			Status: status.DefinitionMissing,
		}, nil
	}
	cfgState, err := s.cfg.ConfigState(ctx, def.Type)
	if err != nil {
		return Item{}, err
	}
	return s.item(def, cfgState), nil
}

func (s *Service) item(def connector.Definition, cfgState status.ConfigState) Item {
	categories := def.Categories
	if categories == nil {
		categories = []string{}
	}
	authMethods := make([]AuthMethodSummary, 0, len(def.AuthMethods))
	for _, method := range def.AuthMethods {
		fields := make([]CredentialField, 0, len(method.CredentialFields))
		for _, field := range method.CredentialFields {
			options := field.Validation.Options
			if options == nil {
				options = []string{}
			}
			fields = append(fields, CredentialField{
				Key:          field.Key,
				Label:        field.Label,
				InputType:    field.InputType,
				Required:     field.Required,
				Secret:       field.Secret,
				DefaultValue: field.DefaultValue,
				Description:  field.Description,
				Pattern:      field.Validation.Pattern,
				Options:      options,
			})
		}
		authMethods = append(authMethods, AuthMethodSummary{
			Key:              method.Key,
			Label:            method.Label,
			Type:             method.Type,
			CredentialFields: fields,
		})
	}
	return Item{
		Type:        string(def.Type),
		Name:        def.Name,
		Description: def.Description,
		Categories:  categories,
		HomepageURL: def.HomepageURL,
		IconURL:     def.IconURL,
		Mode:        def.Mode(),
		Status:      status.Compute(&def, cfgState),
		AuthMethods: authMethods,
	}
}
