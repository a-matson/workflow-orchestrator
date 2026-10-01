//go:build integration

package persistence_test

import (
	"context"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"net/url"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/migrations"
)

// newMigrationDB creates a throwaway database so each test starts empty.
func newMigrationDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://workflow:workflow@localhost:5432/workflow_test?sslmode=disable"
	}
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	name := fmt.Sprintf("fluxor_mig_%d", rand.Uint32())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create database: %v", err)
	}

	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatalf("test pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database: %v", err)
		}
		admin.Close()
	})
	return pool
}

func TestMigrations_FreshDB(t *testing.T) {
	ctx := context.Background()
	pool := newMigrationDB(t)

	if err := persistence.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'task_executions' AND column_name = 'artifacts_in'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("task_executions.artifacts_in missing after Migrate")
	}
}

func TestMigrations_Rerun(t *testing.T) {
	ctx := context.Background()
	pool := newMigrationDB(t)

	// Simulates the old initdb mount: files applied with no tracking.
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		b, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("direct apply %s: %v", f, err)
		}
	}

	for i := 0; i < 2; i++ {
		if err := persistence.Migrate(ctx, pool, migrations.FS); err != nil {
			t.Fatalf("Migrate #%d: %v", i+1, err)
		}
	}
}

func TestMigrations_Concurrent(t *testing.T) {
	ctx := context.Background()
	pool := newMigrationDB(t)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = persistence.Migrate(ctx, pool, migrations.FS)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Migrate #%d: %v", i, err)
		}
	}

	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(files) {
		t.Fatalf("schema_migrations rows = %d, want %d", n, len(files))
	}
}

// HYG-5: schema no code uses is dropped, so nobody mistakes it for live data.
func TestMigrations_DropUnusedSchema(t *testing.T) {
	ctx := context.Background()
	pool := newMigrationDB(t)
	if err := persistence.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	for _, rel := range []string{"artifacts", "idempotency_keys", "execution_summaries", "worker_activity"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, rel).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Errorf("%s still exists after Migrate", rel)
		}
	}
	var fn bool
	if err := pool.QueryRow(ctx, `SELECT to_regprocedure('archive_old_executions(integer)') IS NOT NULL`).Scan(&fn); err != nil {
		t.Fatal(err)
	}
	if fn {
		t.Error("archive_old_executions still exists after Migrate")
	}
}
