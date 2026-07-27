// Package catalogsvc 把代码 Registry 与配置状态合并成对外 catalog，
// 状态由 status.Compute 实时计算，永不包含 Secret。
package catalogsvc

import (
	"context"
	"errors"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/core/status"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/store"
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
	Key   string                   `json:"key"`
	Label string                   `json:"label"`
	Type  connector.AuthMethodType `json:"type"`
}

type Service struct {
	q   *store.Queries
	reg *registry.Registry
	cfg *configsvc.Service
}

func New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service) *Service {
	return &Service{q: q, reg: reg, cfg: cfg}
}

// List 返回全部已注册 Connector，外加数据库中存在但代码已不认识的
// definition_missing 条目；保留原配置，代码回滚后仍可继续使用。
func (s *Service) List(ctx context.Context) ([]Item, error) {
	out := []Item{}
	seen := map[string]bool{}
	for _, def := range s.reg.All() {
		item, err := s.item(ctx, def)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
		seen[string(def.Type)] = true
	}
	rows, err := s.q.ListConnectorConfigs(ctx)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if seen[row.ConnectorType] {
			continue
		}
		out = append(out, Item{
			Type: row.ConnectorType, Categories: []string{}, AuthMethods: []AuthMethodSummary{},
			Status: status.DefinitionMissing,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

// Get 返回单个 Connector；未知 type 且无配置行时返回 configsvc.ErrNotFound。
func (s *Service) Get(ctx context.Context, t connector.Type) (Item, error) {
	def, ok := s.reg.Get(t)
	if !ok {
		if _, err := s.q.GetConnectorConfig(ctx, string(t)); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Item{}, configsvc.ErrNotFound
			}
			return Item{}, err
		}
		return Item{
			Type: string(t), Categories: []string{}, AuthMethods: []AuthMethodSummary{},
			Status: status.DefinitionMissing,
		}, nil
	}
	return s.item(ctx, def)
}

func (s *Service) item(ctx context.Context, def connector.Definition) (Item, error) {
	cfgState, err := s.cfg.ConfigState(ctx, def.Type)
	if err != nil {
		return Item{}, err
	}
	categories := def.Categories
	if categories == nil {
		categories = []string{}
	}
	authMethods := make([]AuthMethodSummary, 0, len(def.AuthMethods))
	for _, method := range def.AuthMethods {
		authMethods = append(authMethods, AuthMethodSummary{
			Key: method.Key, Label: method.Label, Type: method.Type,
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
	}, nil
}
