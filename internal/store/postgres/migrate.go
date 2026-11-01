// Package postgres implements the store on PostgreSQL.
package postgres

import (
	"cmp"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLock is the advisory lock that serialises migration runs on one database.
const migrationLock = 7316402958

//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrationName = regexp.MustCompile(`^(\d+)_[a-z0-9_]+\.(up|down)\.sql$`)

type migration struct {
	version int
	up      string
	down    string
}

// loadMigrations reads migrations/NNNN_name.{up,down}.sql, sorted by version. Every version must
// have exactly one of each.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	paths, err := fs.Glob(fsys, "migrations/*")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	byVersion := make(map[int]*migration)
	for _, p := range paths {
		match := migrationName.FindStringSubmatch(path.Base(p))
		if match == nil {
			return nil, fmt.Errorf("unexpected migration file %s", path.Base(p))
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("migration %s: %w", path.Base(p), err)
		}
		sql, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, fmt.Errorf("read migration: %w", err)
		}
		m := byVersion[version]
		if m == nil {
			m = &migration{version: version}
			byVersion[version] = m
		}
		side := &m.up
		if match[2] == "down" {
			side = &m.down
		}
		if *side != "" {
			return nil, fmt.Errorf("migration version %d: duplicate %s file", version, match[2])
		}
		*side = string(sql)
	}

	migrations := make([]migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.up == "" || m.down == "" {
			return nil, fmt.Errorf("migration version %d: needs both up and down files", m.version)
		}
		migrations = append(migrations, *m)
	}
	slices.SortFunc(migrations, func(a, b migration) int { return cmp.Compare(a.version, b.version) })
	return migrations, nil
}

// Migrate applies every migration not yet applied, all of them or none.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return err
	}
	return migrateUp(ctx, pool, migrations)
}

// MigrateDown reverts every applied migration, newest first, all of them or none.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool) error {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return err
	}
	return migrateDown(ctx, pool, migrations)
}

func migrateUp(ctx context.Context, pool *pgxpool.Pool, migrations []migration) error {
	return inMigrationTx(ctx, pool, func(tx pgx.Tx, applied []int) error {
		for _, m := range migrations {
			if slices.Contains(applied, m.version) {
				continue
			}
			if _, err := tx.Exec(ctx, m.up); err != nil {
				return fmt.Errorf("apply migration %d: %w", m.version, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.version); err != nil {
				return fmt.Errorf("mark migration %d: %w", m.version, err)
			}
		}
		return nil
	})
}

func migrateDown(ctx context.Context, pool *pgxpool.Pool, migrations []migration) error {
	return inMigrationTx(ctx, pool, func(tx pgx.Tx, applied []int) error {
		for _, m := range slices.Backward(migrations) {
			if !slices.Contains(applied, m.version) {
				continue
			}
			if _, err := tx.Exec(ctx, m.down); err != nil {
				return fmt.Errorf("revert migration %d: %w", m.version, err)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, m.version); err != nil {
				return fmt.Errorf("unmark migration %d: %w", m.version, err)
			}
		}
		return nil
	})
}

func inMigrationTx(ctx context.Context, pool *pgxpool.Pool, run func(tx pgx.Tx, applied []int) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(migrationLock)); err != nil {
			return fmt.Errorf("lock migrations: %w", err)
		}
		_, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
			version    integer PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`)
		if err != nil {
			return fmt.Errorf("create schema_migrations: %w", err)
		}
		rows, err := tx.Query(ctx, `SELECT version FROM schema_migrations`)
		if err != nil {
			return fmt.Errorf("read schema_migrations: %w", err)
		}
		applied, err := pgx.CollectRows(rows, pgx.RowTo[int])
		if err != nil {
			return fmt.Errorf("read schema_migrations: %w", err)
		}
		return run(tx, applied)
	})
}
