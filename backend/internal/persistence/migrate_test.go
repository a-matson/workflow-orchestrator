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

// F10: logs stored in the old JSONB column move to task_logs in order, the
// column is emptied, its GIN index dropped, and deleting a run cascades.
func TestMigrations_MoveLogsToTable(t *testing.T) {
	ctx := context.Background()
	pool := newMigrationDB(t)
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f >= "011" {
			break
		}
		b, err := fs.ReadFile(migrations.FS, f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO workflow_definitions (id, name, tasks) VALUES ('wf', 'wf', '[]');
		INSERT INTO workflow_executions (id, workflow_id, workflow_name, status) VALUES ('e', 'wf', 'wf', 'completed');
		INSERT INTO task_executions (id, workflow_exec_id, task_definition_id, task_name, task_type, logs)
		VALUES ('t', 'e', 'a', 'A', 'generic',
			'[{"timestamp":"2026-01-01T00:00:00Z","level":"info","attempt":0,"message":"first"},
			  {"timestamp":"2026-01-01T00:00:01Z","level":"error","attempt":1,"message":"second","fields":{"k":1}}]');
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := persistence.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT message, level, attempt FROM task_logs WHERE task_exec_id = 't' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var msg, level string
		var attempt int
		if err := rows.Scan(&msg, &level, &attempt); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s/%s/%d", msg, level, attempt))
	}
	rows.Close()
	if want := []string{"first/info/0", "second/error/1"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("task_logs = %v, want %v", got, want)
	}
	var leftover, gin int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM task_executions WHERE logs IS NOT NULL`).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_task_execs_logs'`).Scan(&gin); err != nil {
		t.Fatal(err)
	}
	if leftover != 0 || gin != 0 {
		t.Errorf("old logs column still has %d rows, GIN index count %d; want 0 and 0", leftover, gin)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM workflow_executions WHERE id = 'e'`); err != nil {
		t.Fatalf("deleting a run must cascade: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM task_executions) + (SELECT count(*) FROM task_logs)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d task or log rows left after deleting their run", n)
	}
}
