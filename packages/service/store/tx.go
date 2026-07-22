package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrNoTransactions 表示底层 DBTX 不支持开启事务。
var ErrNoTransactions = errors.New("store: 底层 DBTX 不支持事务")

// beginner 由 *pgxpool.Pool 与 pgx.Tx 实现。
type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// BeginTx 在底层连接上开启事务，返回事务本身与绑定该事务的 Queries。
// 调用方负责 Commit / Rollback（Commit 之后的 defer Rollback 是 no-op）。
func (q *Queries) BeginTx(ctx context.Context) (pgx.Tx, *Queries, error) {
	b, ok := q.db.(beginner)
	if !ok {
		return nil, nil, ErrNoTransactions
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	return tx, q.WithTx(tx), nil
}
