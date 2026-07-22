// Package catalogsvc 把代码 Registry 与配置、健康状态合并成对外 catalog，
// 状态由 status.Compute 实时计算，永不包含 Secret。
package catalogsvc

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/core/status"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/store"
)

type Item struct {
	Type        string        `json:"type"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Categories  []string      `json:"categories"`
	HomepageURL string        `json:"homepage_url"`
	IconURL     string        `json:"icon_url"`
	Status      status.Status `json:"status"`
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
// definition_missing 条目（保留配置以支持回滚，spec §9）。
func (s *Service) List(ctx context.Context) ([]Item, error) {
	now := time.Now()
	out := []Item{}
	seen := map[string]bool{}
	for _, def := range s.reg.All() {
		item, err := s.item(ctx, def, now)
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
		out = append(out, Item{Type: row.ConnectorType, Categories: []string{}, Status: status.DefinitionMissing})
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
		return Item{Type: string(t), Categories: []string{}, Status: status.DefinitionMissing}, nil
	}
	return s.item(ctx, def, time.Now())
}

func (s *Service) item(ctx context.Context, def connector.Definition, now time.Time) (Item, error) {
	cfgState, err := s.cfg.ConfigState(ctx, def.Type)
	if err != nil {
		return Item{}, err
	}
	var health status.Health
	hrow, err := s.q.GetConnectorHealth(ctx, string(def.Type))
	if err == nil {
		health.ConsecutiveFailures = int(hrow.ConsecutiveFailures)
		if hrow.LastErrorAt != nil {
			health.LastErrorAt = *hrow.LastErrorAt
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Item{}, err
	}
	categories := def.Categories
	if categories == nil {
		categories = []string{}
	}
	return Item{
		Type:        string(def.Type),
		Name:        def.Name,
		Description: def.Description,
		Categories:  categories,
		HomepageURL: def.HomepageURL,
		IconURL:     def.IconURL,
		Status:      status.Compute(&def, cfgState, health, now),
	}, nil
}
