package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
)

type pickupNotifier struct{ err error }

func (n pickupNotifier) MarkTaskRunning(context.Context, string, string, int) error { return n.err }
func (pickupNotifier) StreamLog(string, string, string, models.LogEntry)            {}

// Only a pickup conflict stops the task; any other pickup error still runs it
// (the orchestrator recovers the lost pickup from the result).
func TestPickUpAndDispatch_ConflictDoesNotRun(t *testing.T) {
	tests := []struct {
		name     string
		pickup   error
		wantRuns int32
	}{
		{"conflict", fmt.Errorf("%w: task t -> running (attempt 0)", persistence.ErrConflict), 0},
		{"store outage", errors.New("connection refused"), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			defer srv.Close()
			w := &Worker{notifier: pickupNotifier{tt.pickup}, httpClient: srv.Client()}
			msg := &models.TaskMessage{TaskExecID: "t", TaskType: "http_request", Config: map[string]any{"url": srv.URL}}
			ctx := context.Background()

			_, _, ran, err := w.pickUpAndDispatch(ctx, ctx, msg, func(string, string, map[string]any) {})

			if got := hits.Load(); got != tt.wantRuns {
				t.Errorf("task ran %d times, want %d", got, tt.wantRuns)
			}
			if ran != (tt.wantRuns == 1) {
				t.Errorf("ran = %v (err %v), want %v", ran, err, tt.wantRuns == 1)
			}
		})
	}
}
