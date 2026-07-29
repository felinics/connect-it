package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrNoTransactions means the underlying DBTX cannot start a transaction.
var ErrNoTransactions = errors.New("store: the underlying DBTX does not support transactions")

// beginner is implemented by *pgxpool.Pool and pgx.Tx.
type beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// BeginTx starts a transaction on the underlying connection and returns both
// the transaction and a Queries bound to it. The caller is responsible for
// Commit and Rollback; a deferred Rollback after Commit is a no-op.
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
