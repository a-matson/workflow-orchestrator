// Command migrate applies the embedded SQL migrations and exits.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/migrations"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://workflow:workflow@localhost:5432/workflow?sslmode=disable"
	}
	ctx := context.Background()
	store, err := persistence.NewStore(ctx, dsn)
	if err != nil {
		return err
	}
	defer store.Close()
	return persistence.Migrate(ctx, store.Pool(), migrations.FS)
}
