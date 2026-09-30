package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/api"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/migrations"
)

// defaultPostgresURL names no password: pgx then reads PGPASSWORD or
// ~/.pgpass, so no credential lives in the binary. Compose, the Makefile and
// CI all set POSTGRES_URL.
const defaultPostgresURL = "postgres://workflow@localhost:5432/workflow?sslmode=disable"

const keyCommandUsage = `usage:
  workflow-server apikey create --name NAME --role admin|operator|viewer
  workflow-server apikey list
  workflow-server apikey revoke ID`

// bootstrapAPIKeys installs FLUXOR_BOOTSTRAP_ADMIN_KEY, if set, and warns when
// no usable key exists, since every non-probe endpoint would then answer 401.
func bootstrapAPIKeys(ctx context.Context, store *persistence.Store) error {
	if key := os.Getenv("FLUXOR_BOOTSTRAP_ADMIN_KEY"); key != "" {
		if err := store.EnsureAPIKey(ctx, "bootstrap-admin", string(api.RoleAdmin), key); err != nil {
			return fmt.Errorf("FLUXOR_BOOTSTRAP_ADMIN_KEY: %w", err)
		}
	}
	n, err := store.CountActiveAPIKeys(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		log.Warn().Msg("no API keys exist, so the API rejects every request except health probes; " +
			"create one with `workflow-server apikey create --name admin --role admin` " +
			"or set FLUXOR_BOOTSTRAP_ADMIN_KEY")
	}
	return nil
}

// runAPIKey implements the `apikey` subcommands and returns the exit code.
func runAPIKey(args []string, stdout, stderr io.Writer) int {
	fail := func(code int, msg ...any) int {
		_, _ = fmt.Fprintln(stderr, msg...) // nothing useful to do if stderr is gone
		return code
	}
	if len(args) == 0 {
		return fail(2, keyCommandUsage)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var run func(*persistence.Store) error
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("apikey create", flag.ContinueOnError)
		fs.SetOutput(stderr)
		name := fs.String("name", "", "key name, shown in logs and listings")
		role := fs.String("role", "", "admin, operator or viewer")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *name == "" || !api.ValidRole(*role) {
			return fail(2, keyCommandUsage)
		}
		run = func(s *persistence.Store) error {
			plaintext, key, err := s.CreateAPIKey(ctx, *name, *role)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(stdout, "id:   %s\nrole: %s\nkey:  %s\n\nThe key is shown only once.\n", key.ID, key.Role, plaintext)
			return err
		}
	case "list":
		run = func(s *persistence.Store) error {
			keys, err := s.ListAPIKeys(ctx)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			if _, err := fmt.Fprintln(tw, "ID\tNAME\tROLE\tCREATED\tLAST USED\tREVOKED"); err != nil {
				return err
			}
			for _, k := range keys {
				if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", k.ID, k.Name, k.Role,
					k.CreatedAt.Format(time.RFC3339), fmtTime(k.LastUsedAt), fmtTime(k.RevokedAt)); err != nil {
					return err
				}
			}
			return tw.Flush()
		}
	case "revoke":
		if len(args) != 2 {
			return fail(2, keyCommandUsage)
		}
		run = func(s *persistence.Store) error {
			if err := s.RevokeAPIKey(ctx, args[1]); err != nil {
				if errors.Is(err, persistence.ErrNotFound) {
					return fmt.Errorf("no unrevoked key with id %q", args[1])
				}
				return err
			}
			_, err := fmt.Fprintln(stdout, "revoked", args[1])
			return err
		}
	default:
		return fail(2, keyCommandUsage)
	}

	store, err := persistence.NewStore(ctx, getEnv("POSTGRES_URL", defaultPostgresURL))
	if err != nil {
		return fail(1, "connecting to PostgreSQL:", err)
	}
	defer store.Close()
	// The CLI may run before the server ever has, so the table may not exist yet.
	if err := persistence.Migrate(ctx, store.Pool(), migrations.FS); err != nil {
		return fail(1, "migrating database:", err)
	}
	if err := run(store); err != nil {
		return fail(1, "apikey:", err)
	}
	return 0
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format(time.RFC3339)
}
