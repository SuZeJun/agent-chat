package persistence

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"agent-chat/migrations"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationLockID int64 = 763521904

type migration struct {
	version  int64
	name     string
	sql      string
	checksum string
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	items, err := loadMigrations()
	if err != nil {
		return err
	}
	return applyMigrations(ctx, pool, items)
}

func applyMigrations(ctx context.Context, pool *pgxpool.Pool, items []migration) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version bigint PRIMARY KEY,
			name varchar(255) NOT NULL,
			checksum char(64) NOT NULL,
			applied_at timestamptz NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		ALTER TABLE schema_migrations
		ADD COLUMN IF NOT EXISTS checksum char(64)
	`); err != nil {
		return fmt.Errorf("upgrade schema migrations table: %w", err)
	}

	for _, item := range items {
		var appliedName string
		var appliedChecksum string
		err := tx.QueryRow(ctx,
			`SELECT name, COALESCE(checksum, '') FROM schema_migrations WHERE version = $1`,
			item.version,
		).Scan(&appliedName, &appliedChecksum)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check migration %s: %w", item.name, err)
		}
		if err == nil {
			if appliedName != item.name {
				return fmt.Errorf("migration version %d was applied as %s, not %s", item.version, appliedName, item.name)
			}
			if appliedChecksum == "" {
				return fmt.Errorf("migration %s was applied without a checksum; reset or repair the development database", item.name)
			}
			if appliedChecksum != item.checksum {
				return fmt.Errorf("migration %s checksum changed after it was applied", item.name)
			}
			continue
		}
		if _, err := tx.Exec(ctx, item.sql); err != nil {
			return fmt.Errorf("apply migration %s: %w", item.name, err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_migrations(version, name, checksum) VALUES ($1, $2, $3)",
			item.version,
			item.name,
			item.checksum,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", item.name, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	return loadMigrationsFromFS(migrations.Files)
}

func loadMigrationsFromFS(source fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	items := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		item, err := parseMigration(entry.Name())
		if err != nil {
			return nil, err
		}
		content, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if strings.TrimSpace(string(content)) == "" {
			return nil, fmt.Errorf("migration %s must not be empty", entry.Name())
		}
		item.sql = string(content)
		item.checksum = fmt.Sprintf("%x", sha256.Sum256(content))
		items = append(items, item)
	}
	sort.Slice(items, func(left, right int) bool {
		return items[left].version < items[right].version
	})
	for index := 1; index < len(items); index++ {
		if items[index-1].version == items[index].version {
			return nil, fmt.Errorf("duplicate migration version %d", items[index].version)
		}
	}
	return items, nil
}

func parseMigration(name string) (migration, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok || prefix == "" {
		return migration{}, fmt.Errorf("migration %s must start with a numeric version and underscore", name)
	}
	version, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil || version <= 0 {
		return migration{}, fmt.Errorf("migration %s has an invalid version", name)
	}
	return migration{version: version, name: name}, nil
}
