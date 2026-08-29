// Package catalogsvc merges the code registry with stored config state into
// the catalog exposed to clients. Status is computed on the fly by
// status.Compute, and secrets are never included.
package catalogsvc

import (
	"context"

	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/felinics/connect-it/packages/core/registry"
	"github.com/felinics/connect-it/packages/core/status"
	"github.com/felinics/connect-it/packages/service/configsvc"
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
	Enabled     bool                `json:"enabled"`
	AuthMethods []AuthMethodSummary `json:"auth_methods"`
}

// AuthMethodSummary is the non-sensitive auth metadata a downstream service
// needs in order to create a connection.
type AuthMethodSummary struct {
	Key              string                   `json:"key"`
	Label            string                   `json:"label"`
	Type             connector.AuthMethodType `json:"type"`
	CredentialFields []CredentialField        `json:"credential_fields"`
}

// CredentialField is the non-sensitive form metadata needed to create an
// api_key or custom_credential connection. It describes field constraints
// only and never carries a user-submitted credential value.
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

// List returns every registered connector plus definition_missing entries for
// rows that exist in the database but are no longer known to the code. Their
// config is preserved so a code rollback can keep using them.
func (s *Service) List(ctx context.Context) ([]Item, error) {
	states, err := s.cfg.ConfigStates(ctx)
	if err != nil {
		return nil, err
	}
	enabledStates, err := s.cfg.EnabledStates(ctx)
	if err != nil {
		return nil, err
	}
	out := []Item{}
	seen := map[string]bool{}
	for _, def := range s.reg.All() {
		enabled, exists := enabledStates[def.Type]
		if !exists {
			enabled = true
		}
		out = append(out, s.item(def, states[def.Type], enabled))
		seen[string(def.Type)] = true
	}
	for connectorType := range states {
		if seen[string(connectorType)] {
			continue
		}
		out = append(out, Item{
			Type: string(connectorType), Categories: []string{}, AuthMethods: []AuthMethodSummary{},
			Status: status.DefinitionMissing, Enabled: false,
		})
	}
	sortItems(out)
	return out, nil
}

// ListEnabled returns only connectors that downstream clients may use.
func (s *Service) ListEnabled(ctx context.Context) ([]Item, error) {
	items, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(items))
	for _, item := range items {
		if item.Enabled {
			out = append(out, item)
		}
	}
	return out, nil
}

// Get returns a single connector. It returns configsvc.ErrNotFound when the
// type is unknown and no config row exists.
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
			Status: status.DefinitionMissing, Enabled: false,
		}, nil
	}
	cfgState, err := s.cfg.ConfigState(ctx, def.Type)
	if err != nil {
		return Item{}, err
	}
	enabled, err := s.cfg.Enabled(ctx, def.Type)
	if err != nil {
		return Item{}, err
	}
	return s.item(def, cfgState, enabled), nil
}

// GetEnabled returns a connector only when downstream clients may use it.
func (s *Service) GetEnabled(ctx context.Context, t connector.Type) (Item, error) {
	item, err := s.Get(ctx, t)
	if err != nil {
		return Item{}, err
	}
	if !item.Enabled {
		return Item{}, configsvc.ErrNotFound
	}
	return item, nil
}

func (s *Service) item(def connector.Definition, cfgState status.ConfigState, enabled bool) Item {
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
	connectorStatus := status.Compute(&def, cfgState)
	if !enabled {
		connectorStatus = status.Disabled
	}
	return Item{
		Type:        string(def.Type),
		Name:        def.Name,
		Description: def.Description,
		Categories:  categories,
		HomepageURL: def.HomepageURL,
		IconURL:     def.IconURL,
		Mode:        def.Mode(),
		Status:      connectorStatus,
		Enabled:     enabled,
		AuthMethods: authMethods,
	}
}
