package persistence

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrateLockID is an arbitrary constant shared by every process that
// migrates this database.
const migrateLockID int64 = 0x666c75786f72 // "fluxor"

// Migrate applies every *.sql file in fsys, in lexical order, that is not yet
// recorded in schema_migrations. Each file runs in its own transaction with
// its bookkeeping row.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) (err error) {
	// A session-level lock must be taken and released on the same connection,
	// so hold one dedicated connection for the whole run. It makes replicas
	// starting together serialize instead of racing on the same DDL.
	pc, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring connection: %w", err)
	}
	conn := pc.Conn()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrateLockID); err != nil {
		pc.Release()
		return fmt.Errorf("taking migration lock: %w", err)
	}
	defer func() {
		// ctx may already be cancelled; the unlock must still be attempted.
		if _, uerr := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrateLockID); uerr != nil {
			// Releasing would hand a lock-holding session back to the pool.
			// Closing the connection ends the session and drops the lock.
			_ = pc.Hijack().Close(context.WithoutCancel(ctx))
			err = errors.Join(err, fmt.Errorf("releasing migration lock: %w", uerr))
			return
		}
		pc.Release()
	}()

	return applyMigrations(ctx, conn, fsys)
}

func applyMigrations(ctx context.Context, conn *pgx.Conn, fsys fs.FS) error {
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return fmt.Errorf("listing migrations: %w", err)
	}
	sort.Strings(files)

	for _, name := range files {
		var applied bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("checking %s: %w", name, err)
		}
		if applied {
			continue
		}

		sqlBytes, err := fs.ReadFile(fsys, name)
		if err != nil {
			return fmt.Errorf("reading %s: %w", name, err)
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("beginning %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			return errors.Join(fmt.Errorf("applying %s: %w", name, err), tx.Rollback(ctx))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			return errors.Join(fmt.Errorf("recording %s: %w", name, err), tx.Rollback(ctx))
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("committing %s: %w", name, err)
		}
	}
	return nil
}
