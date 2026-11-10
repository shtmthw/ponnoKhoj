// Package store is the data-access layer: a pgx pool plus sqlc-generated
// queries (package db) and transaction helpers.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shtmthw/ponnoKhoj/internal/store/db"
)

// WordSimilarityThreshold is the pg_trgm threshold for the "<%" operator used
// by product search. The extension's default (0.6) drops multi-word queries
// whose words are not adjacent in the product key; 0.45 keeps them while
// still using the trigram index. Ranking happens afterwards in Go.
const WordSimilarityThreshold = "0.45"

// Store bundles the connection pool with the generated queries.
type Store struct {
	Pool *pgxpool.Pool
	*db.Queries
}

// Open connects to Postgres and verifies the connection.
func Open(ctx context.Context, databaseURL string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// pgx includes the URL in parse errors; never surface it (password).
		return nil, errors.New("store: DATABASE_URL is not a valid Postgres connection string")
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.RuntimeParams["application_name"] = "ponnokhoj"
	cfg.ConnConfig.RuntimeParams["pg_trgm.word_similarity_threshold"] = WordSimilarityThreshold
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{Pool: pool, Queries: db.New(pool)}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.Pool.Close() }

// InTx runs fn in a transaction, committing when fn returns nil and rolling
// back otherwise.
func (s *Store) InTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful commit
	if err := fn(s.WithTx(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	return nil
}

// TrigramCostApplied reports whether the pg_trgm operator functions carry the
// realistic planner cost set by migration 00003 (or by the CloudNativePG
// bootstrap SQL). Without it the planner scans every product instead of
// using the trigram indexes; search still works, only slower.
func (s *Store) TrigramCostApplied(ctx context.Context) (bool, error) {
	var cost float64
	err := s.Pool.QueryRow(ctx, `SELECT procost FROM pg_proc WHERE oid = 'word_similarity_op(text,text)'::regprocedure`).Scan(&cost)
	if err != nil {
		return false, fmt.Errorf("store: read trigram operator cost: %w", err)
	}
	return cost >= TrigramOperatorCost, nil
}

// TrigramOperatorCost is the planner cost given to pg_trgm's similarity
// operator functions (see migration 00003 and docs/architecture.md).
const TrigramOperatorCost = 100

// SchemaVersion returns the highest applied goose migration version. It
// doubles as the readiness ping. A database that has never been migrated
// reports version 0.
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) {
	var v int64
	err := s.Pool.QueryRow(ctx, `SELECT COALESCE(max(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&v)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42P01" { // undefined_table
			return 0, nil
		}
		return 0, fmt.Errorf("store: schema version: %w", err)
	}
	return v, nil
}

// IsNotFound reports whether err is pgx's "no rows" error.
func IsNotFound(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// IsUniqueViolation reports whether err is a unique-constraint violation.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
