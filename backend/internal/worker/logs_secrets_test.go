package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/egress"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// stubTransport answers every request itself so the test needs no network and
// PagerDuty's fixed URL can be exercised.
type stubTransport struct{}

func (stubTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	code := http.StatusOK
	if r.URL.Host == "events.pagerduty.com" {
		code = http.StatusAccepted
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}, Request: r}, nil
}

// Task logs are shown by the UI and API, so a secret in either sink is a leak
// to everyone who can read the run.
func TestLogs_NoSecrets(t *testing.T) {
	var zlog bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&zlog)
	t.Cleanup(func() { log.Logger = prev })

	g, err := egress.New("127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{httpClient: &http.Client{Transport: stubTransport{}, Timeout: 5 * time.Second}, guard: g}

	var stream []models.LogEntry
	addLog := func(level, message string, fields map[string]any) {
		stream = append(stream, models.LogEntry{Level: level, Message: message, Fields: fields})
	}
	var errs []string

	tasks := []models.TaskMessage{
		{TaskType: "http_request", Config: map[string]any{"url": "https://user:s3cret@api.example.com/v1/items?token=abc"}},
		{TaskType: "notification", Config: map[string]any{"notify_type": "slack", "channel": "https://hooks.slack.com/services/T000/B000/slacktok123", "message": "hi"}},
		{TaskType: "notification", Config: map[string]any{"notify_type": "pagerduty", "channel": "pdroutingkey456", "message": "hi"}},
		{TaskType: "notification", Config: map[string]any{"notify_type": "webhook", "channel": "https://discord.example.com/api/webhooks/1/disctok789", "message": "hi"}},
		{TaskType: "database_query", Config: map[string]any{"connection_string": "postgres://app:dbpass321@127.0.0.1:1/x?sslmode=disable", "query": "select 1"}},
	}
	for i := range tasks {
		if _, _, err := w.dispatch(context.Background(), &tasks[i], addLog); err != nil {
			errs = append(errs, err.Error())
		}
	}

	sink, err := json.Marshal(stream)
	if err != nil {
		t.Fatal(err)
	}
	all := zlog.String() + string(sink) + strings.Join(errs, "\n")
	for _, secret := range []string{"s3cret", "abc", "slacktok123", "pdroutingkey456", "disctok789", "dbpass321"} {
		if strings.Contains(all, secret) {
			t.Errorf("secret %q appears in logs or task log stream", secret)
		}
	}
}
