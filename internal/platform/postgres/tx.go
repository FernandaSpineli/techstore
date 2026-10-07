package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx, so stores can run
// the same queries inside or outside a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// WithTx runs fn in a transaction, committing if fn returns nil and rolling
// back otherwise.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			return errors.Join(err, fmt.Errorf("postgres: rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	return nil
}

// PostgreSQL error codes the stores translate into domain errors.
const (
	codeRestrictViolation   = "23001"
	codeForeignKeyViolation = "23503"
	codeUniqueViolation     = "23505"
	codeCheckViolation      = "23514"
)

func hasCode(err error, code, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code &&
		(constraint == "" || pgErr.ConstraintName == constraint)
}

// IsUniqueViolation reports whether err violates a unique constraint. An
// empty constraint matches any.
func IsUniqueViolation(err error, constraint string) bool {
	return hasCode(err, codeUniqueViolation, constraint)
}

// IsForeignKeyViolation reports whether err references a missing row.
func IsForeignKeyViolation(err error, constraint string) bool {
	return hasCode(err, codeForeignKeyViolation, constraint)
}

// IsRestrictViolation reports whether a delete was blocked by an
// ON DELETE RESTRICT foreign key.
func IsRestrictViolation(err error, constraint string) bool {
	return hasCode(err, codeRestrictViolation, constraint)
}

// IsCheckViolation reports whether err violates a CHECK constraint.
func IsCheckViolation(err error, constraint string) bool {
	return hasCode(err, codeCheckViolation, constraint)
}
