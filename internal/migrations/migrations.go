// Package migrations embeds the SQL schema migrations and runs them.
//
// Two migration sets live in the database: ponnoKhoj's own (run with goose
// from the embedded files) and River's job-queue tables (run with River's
// migrator). Both are applied by the `ponnokhoj migrate` subcommand, never on
// application startup.
package migrations

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:embed sql/*.sql
var embedded embed.FS

// FS returns the embedded migration files (the sql directory's contents).
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "sql")
	if err != nil {
		panic(err) // unreachable: the directory is embedded at build time
	}
	return sub
}

// ExpectedVersion is the newest schema version this binary knows about.
func ExpectedVersion() int64 {
	var maxVersion int64
	entries, err := fs.ReadDir(FS(), ".")
	if err != nil {
		panic(err) // unreachable: the directory is embedded at build time
	}
	for _, e := range entries {
		if v, err := goose.NumericComponent(e.Name()); err == nil && v > maxVersion {
			maxVersion = v
		}
	}
	return maxVersion
}

// advisoryLockID serializes concurrent migrators (e.g. two migration Jobs).
const advisoryLockID int64 = 0x706b6d6967 // "pkmig"

// lockTimeout bounds how long a migrator waits for another one to finish.
const lockTimeout = 5 * time.Minute

// withLock runs fn while holding a session-level Postgres advisory lock on a
// dedicated connection. The lock covers BOTH migration sets (River's and
// ours); goose's own locker would cover only ours. The lock is released when
// the connection closes, so a crashed migrator cannot leave it stuck.
func withLock(ctx context.Context, databaseURL string, fn func() error) error {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return errors.New("migrations: cannot connect to the database")
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	lockCtx, cancel := context.WithTimeout(ctx, lockTimeout)
	defer cancel()
	if _, err := conn.Exec(lockCtx, "SELECT pg_advisory_lock($1)", advisoryLockID); err != nil {
		return fmt.Errorf("migrations: acquire advisory lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", advisoryLockID)
	}()
	return fn()
}

func provider(databaseURL string) (*goose.Provider, func(), error) {
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, nil, errors.New("migrations: DATABASE_URL is not a valid Postgres connection string")
	}
	sqlDB := stdlib.OpenDB(*cfg)
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, FS())
	if err != nil {
		_ = sqlDB.Close()
		return nil, nil, fmt.Errorf("migrations: provider: %w", err)
	}
	return p, func() { _ = sqlDB.Close() }, nil
}

func riverMigrate(ctx context.Context, databaseURL string, dir rivermigrate.Direction, opts *rivermigrate.MigrateOpts) error {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("migrations: river pool: %w", err)
	}
	defer pool.Close()
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("migrations: river migrator: %w", err)
	}
	if _, err := m.Migrate(ctx, dir, opts); err != nil {
		return fmt.Errorf("migrations: river migrate %s: %w", dir, err)
	}
	return nil
}

// Result describes one applied migration.
type Result struct {
	Version  int64
	Name     string
	Duration time.Duration
}

func toResults(in []*goose.MigrationResult) []Result {
	out := make([]Result, 0, len(in))
	for _, r := range in {
		out = append(out, Result{Version: r.Source.Version, Name: r.Source.Path, Duration: r.Duration})
	}
	return out
}

// Up applies every pending migration: River's queue tables first, then the
// application schema. It holds a Postgres advisory lock while it runs, so
// concurrent migrators are serialized.
func Up(ctx context.Context, databaseURL string) ([]Result, error) {
	var out []Result
	err := withLock(ctx, databaseURL, func() error {
		if err := riverMigrate(ctx, databaseURL, rivermigrate.DirectionUp, nil); err != nil {
			return err
		}
		p, closeDB, err := provider(databaseURL)
		if err != nil {
			return err
		}
		defer closeDB()
		res, err := p.Up(ctx)
		out = toResults(res)
		if err != nil {
			return fmt.Errorf("migrations: up: %w", err)
		}
		return nil
	})
	return out, err
}

// Down rolls back the application schema: one migration, or all of them (and
// River's tables) when all is true.
func Down(ctx context.Context, databaseURL string, all bool) ([]Result, error) {
	var out []Result
	err := withLock(ctx, databaseURL, func() error {
		p, closeDB, err := provider(databaseURL)
		if err != nil {
			return err
		}
		defer closeDB()
		if !all {
			res, err := p.Down(ctx)
			if errors.Is(err, goose.ErrNoNextVersion) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("migrations: down: %w", err)
			}
			out = toResults([]*goose.MigrationResult{res})
			return nil
		}
		res, err := p.DownTo(ctx, 0)
		out = toResults(res)
		if err != nil {
			return fmt.Errorf("migrations: down to 0: %w", err)
		}
		return riverMigrate(ctx, databaseURL, rivermigrate.DirectionDown, &rivermigrate.MigrateOpts{TargetVersion: -1})
	})
	return out, err
}

// Version reports the applied application schema version.
func Version(ctx context.Context, databaseURL string) (int64, error) {
	p, closeDB, err := provider(databaseURL)
	if err != nil {
		return 0, err
	}
	defer closeDB()
	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("migrations: version: %w", err)
	}
	return v, nil
}
