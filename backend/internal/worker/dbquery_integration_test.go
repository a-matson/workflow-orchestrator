//go:build integration

package worker

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/egress"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

func TestExecDBQuery_SelectOne(t *testing.T) {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		t.Skip("POSTGRES_URL not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	g, err := egress.New(u.Host) // exactly the test database, as written in the DSN
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{guard: g}
	msg := &models.TaskMessage{TaskType: "database_query", Config: map[string]any{
		"connection_string": dsn, "query": "SELECT 1 AS one",
	}}

	out, _, err := w.dispatch(context.Background(), msg, func(string, string, map[string]any) {})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	rows, _ := out["rows"].([]map[string]any)
	if len(rows) != 1 || fmt.Sprint(rows[0]["one"]) != "1" {
		t.Fatalf("rows = %v, want [{one:1}]", out["rows"])
	}
}

func TestExecDBQuery_NonAllowlistedHostStillDenied(t *testing.T) {
	err := runDBTask(t, "127.0.0.1:1", "postgres://u:s3cretpw@10.0.0.5:5432/db")
	if !errors.Is(err, egress.ErrEgressDenied) {
		t.Fatalf("err = %v, want ErrEgressDenied", err)
	}
}
