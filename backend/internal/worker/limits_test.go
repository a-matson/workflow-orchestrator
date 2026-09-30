package worker

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/egress"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

const testCap = 1 << 20

func loopbackWorker(t *testing.T, srvURL string) *Worker {
	t.Helper()
	g, err := egress.New(strings.TrimPrefix(srvURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return &Worker{httpClient: g.HTTPClient(30 * time.Second), guard: g}
}

func TestNotifySlack_ErrorBounded(t *testing.T) {
	srv, _ := countingServer(t, func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusInternalServerError)
		chunk := []byte(strings.Repeat("x", 64<<10))
		for i := 0; i < 160; i++ { // 10 MiB
			if _, err := rw.Write(chunk); err != nil {
				return
			}
		}
	})
	w := loopbackWorker(t, srv.URL)
	msg := &models.TaskMessage{TaskType: "notification", Config: map[string]any{"notify_type": "slack", "channel": srv.URL, "message": "hi"}}

	_, _, err := w.dispatch(context.Background(), msg, noLog)

	if err == nil {
		t.Fatal("want an error for a 500 reply")
	}
	if n := len(err.Error()); n > 4<<10+64 {
		t.Errorf("error is %d bytes, want <= 4 KiB", n)
	}
}

func TestExecHTTP_ResponseBounded(t *testing.T) {
	srv, _ := countingServer(t, func(rw http.ResponseWriter, _ *http.Request) {
		chunk := []byte(strings.Repeat("y", 64<<10))
		for i := 0; i < 800; i++ { // 50 MiB
			if _, err := rw.Write(chunk); err != nil {
				return
			}
		}
	})
	w := loopbackWorker(t, srv.URL)
	msg := &models.TaskMessage{TaskType: "http_request", Config: map[string]any{"url": srv.URL}}

	out, _, err := w.dispatch(context.Background(), msg, noLog)

	if err != nil {
		t.Fatal(err)
	}
	body, _ := out["body"].(string)
	if len(body) > testCap {
		t.Errorf("body is %d bytes, want <= %d", len(body), testCap)
	}
	if out["truncated"] != true {
		t.Errorf("truncated = %v, want true", out["truncated"])
	}
}

func TestReadContainerLogs_Bounded(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		// Docker frame: stream type (1 = stdout), 3 zero bytes, big-endian length.
		frame := append([]byte{1, 0, 0, 0, 0, 1, 0, 0}, []byte(strings.Repeat("z", 64<<10))...)
		for i := 0; i < 800; i++ { // 50 MiB
			if _, err := pw.Write(frame); err != nil {
				return
			}
		}
		_ = pw.Close() // test goroutine: the reader side is gone or done
	}()
	defer func() { _ = pr.Close() }()

	got, err := readContainerLogs(pr, testCap)

	if err != nil {
		t.Fatal(err)
	}
	if len(got) > testCap {
		t.Errorf("logs are %d bytes, want <= %d", len(got), testCap)
	}
	if !strings.Contains(got, "truncated") {
		t.Error("want a truncation line")
	}
}

func TestBoundedLogs_CapPerAttempt(t *testing.T) {
	b := newBoundedLogs(testCap)
	msg := strings.Repeat("l", 1<<10)
	for i := 0; i < 3000; i++ {
		b.add(models.LogEntry{Level: "info", Message: msg})
	}

	var size int
	for _, e := range b.entries[:len(b.entries)-1] {
		size += len(e.Message)
	}
	if size > testCap {
		t.Errorf("kept %d bytes of log messages, want <= %d", size, testCap)
	}
	if last := b.entries[len(b.entries)-1]; !strings.Contains(last.Message, "truncated") {
		t.Errorf("last entry = %q, want a truncation notice", last.Message)
	}
}

func TestLimitsFromEnv(t *testing.T) {
	t.Setenv("FLUXOR_MAX_TASK_OUTPUT_BYTES", "")
	t.Setenv("FLUXOR_MAX_TASK_LOG_BYTES", "")
	if l, err := LimitsFromEnv(); err != nil || l.OutputBytes != DefaultMaxTaskOutputBytes || l.LogBytes != DefaultMaxTaskLogBytes {
		t.Errorf("defaults = %+v, %v", l, err)
	}
	for _, bad := range []string{"abc", "0", "-5", "1MiB"} {
		t.Setenv("FLUXOR_MAX_TASK_LOG_BYTES", bad)
		if _, err := LimitsFromEnv(); err == nil {
			t.Errorf("FLUXOR_MAX_TASK_LOG_BYTES=%q accepted", bad)
		}
	}
}
