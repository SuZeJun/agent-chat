package persistence

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrateAgainstPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open admin database: %v", err)
	}
	defer adminPool.Close()

	if err := ensureVectorExtension(ctx, adminPool); err != nil {
		t.Fatalf("create vector extension: %v", err)
	}

	schemaName := fmt.Sprintf("migration_test_%d", time.Now().UnixNano())
	schemaIdentifier := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+schemaIdentifier); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = adminPool.Exec(cleanupContext, "DROP SCHEMA "+schemaIdentifier+" CASCADE")
	}()

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schemaName + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer pool.Close()

	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, 2)
	for range 2 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			errorsChannel <- Migrate(ctx, pool)
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent migration failed: %v", err)
		}
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("repeat migration failed: %v", err)
	}

	var migrationCount int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&migrationCount); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrationCount != 1 {
		t.Fatalf("unexpected migration count: %d", migrationCount)
	}

	var constraintCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_constraint
		WHERE conrelid = 'jobs'::regclass
		  AND conname IN (
			'jobs_status_valid',
			'jobs_attempts_within_limit',
			'jobs_lock_state_consistent'
		  )
	`).Scan(&constraintCount); err != nil {
		t.Fatalf("count job constraints: %v", err)
	}
	if constraintCount != 3 {
		t.Fatalf("unexpected job constraint count: %d", constraintCount)
	}

	var indexCount int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND indexname IN (
			'jobs_available_poll_index',
			'jobs_running_lock_recovery_index'
		  )
	`).Scan(&indexCount); err != nil {
		t.Fatalf("count job indexes: %v", err)
	}
	if indexCount != 2 {
		t.Fatalf("unexpected job index count: %d", indexCount)
	}

	badSQL := `
		CREATE TABLE migration_rollback_probe (id integer);
		SELECT migration_function_that_does_not_exist();
	`
	badChecksum := sha256.Sum256([]byte(badSQL))
	err = applyMigrations(ctx, pool, []migration{{
		version:  999999,
		name:     "999999_rollback_probe.sql",
		sql:      badSQL,
		checksum: fmt.Sprintf("%x", badChecksum),
	}})
	if err == nil {
		t.Fatal("expected invalid migration to fail")
	}

	var rollbackTable *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('migration_rollback_probe')::text").Scan(&rollbackTable); err != nil {
		t.Fatalf("check migration rollback: %v", err)
	}
	if rollbackTable != nil {
		t.Fatalf("expected rollback probe table to be absent, got %q", *rollbackTable)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE schema_migrations
		SET checksum = repeat('0', 64)
		WHERE version = 1
	`); err != nil {
		t.Fatalf("change migration checksum: %v", err)
	}
	if err := Migrate(ctx, pool); err == nil {
		t.Fatal("expected changed migration checksum to fail")
	}
}

func ensureVectorExtension(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
