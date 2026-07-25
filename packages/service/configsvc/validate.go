package configsvc

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
)

// Validate 是 Put 的试算：复用同一套写入归一化，并且和 Put 一样先合并库中已存的
// secret，因此校验结论与真正落库的结论不会分叉。public 为全量；secrets 为部分
// 合并——出现的 key 覆盖，空串表示删除，未出现的保留库中原值。
//
// 合并已存 secret 是必要的：secrets 是补丁而非全量，若按“提交的就是全部”来归一
// 化，一个管理员只改 public 字段、没有重填已存必填 secret 时，试算会报“必填字段
// 缺失”，而同样的请求体走 PUT 却能成功。
func (s *Service) Validate(
	ctx context.Context,
	t connector.Type,
	public map[string]any,
	secrets map[string]string,
) error {
	def, ok := s.reg.Get(t)
	if !ok {
		return ErrUnknownConnector
	}

	storedSecrets, err := s.storedSecretsFor(ctx, def, t)
	if err != nil {
		return err
	}
	_, _, _, err = s.normalizedWriteValues(def, public, storedSecrets, secrets)
	return err
}

// storedSecretsFor 读取并解密当前已存的 secret，顺带把旧 schema 版本升级到
// Definition 当前版本，与 Put 在事务内所做的一致。配置尚不存在时返回空集合。
func (s *Service) storedSecretsFor(
	ctx context.Context,
	def connector.Definition,
	t connector.Type,
) (map[string]string, error) {
	row, err := s.q.GetConnectorConfig(ctx, string(t))
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if int(row.ConfigSchemaVersion) > def.ConfigSchemaVersion {
		return nil, ErrIncompatible
	}

	storedSecrets, err := s.decryptSecrets(row, t)
	if err != nil {
		return nil, err
	}
	if int(row.ConfigSchemaVersion) == def.ConfigSchemaVersion {
		return storedSecrets, nil
	}

	storedPublic, err := unmarshalPublic(row.PublicConfig)
	if err != nil {
		return nil, err
	}
	_, storedSecrets, err = upgradeStoredConfig(
		def,
		int(row.ConfigSchemaVersion),
		storedPublic,
		storedSecrets,
	)
	if err != nil {
		return nil, err
	}
	return storedSecrets, nil
}
