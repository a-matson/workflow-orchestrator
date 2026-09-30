package worker

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/egress"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

func runDBTask(t *testing.T, allow, dsn string) error {
	t.Helper()
	g, err := egress.New(allow)
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{guard: g}
	msg := &models.TaskMessage{TaskType: "database_query", Config: map[string]any{
		"connection_string": dsn, "query": "select 1",
	}}
	_, _, err = w.dispatch(context.Background(), msg, func(string, string, map[string]any) {})
	return err
}

func TestExecDBQuery_Denied(t *testing.T) {
	dsns := map[string]string{
		"loopback v4":       "postgres://u:s3cretpw@127.0.0.1:5432/db",
		"localhost":         "postgres://u:s3cretpw@localhost:5432/db",
		"private":           "postgres://u:s3cretpw@10.0.0.5:5432/db",
		"loopback v6":       "postgres://u:s3cretpw@[::1]:5432/db",
		"unix socket":       "host=/tmp user=u password=s3cretpw dbname=db",
		"multi-host":        "postgres://u:s3cretpw@203.0.113.7:5432,10.0.0.5:5432/db",
		"key/value private": "host=10.0.0.5 port=5432 user=u password=s3cretpw dbname=db",
	}
	for name, dsn := range dsns {
		t.Run(name, func(t *testing.T) {
			err := runDBTask(t, "", dsn)
			if !errors.Is(err, egress.ErrEgressDenied) {
				t.Fatalf("err = %v, want ErrEgressDenied", err)
			}
			if strings.Contains(err.Error(), "s3cretpw") {
				t.Errorf("error leaks the password: %v", err)
			}
		})
	}
}

func TestExecDBQuery_AllowlistedHostPasses(t *testing.T) {
	err := runDBTask(t, "10.0.0.5:5432", "postgres://u:s3cretpw@10.0.0.5:5432/db")
	if errors.Is(err, egress.ErrEgressDenied) {
		t.Fatalf("allowlisted host was denied: %v", err)
	}
}
