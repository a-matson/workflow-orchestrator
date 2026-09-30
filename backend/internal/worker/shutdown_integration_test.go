//go:build integration

package worker

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/a-matson/workflow-orchestrator/backend/internal/egress"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/testutil"
)

// REL-21: a shutdown must let a task that finishes within the grace period
// complete and publish its result, not cancel it and lose the result.
func TestPool_ShutdownDrains(t *testing.T) {
	_, redis := testutil.Env(t)
	started := make(chan struct{}, 1)
	srv, _ := countingServer(t, func(http.ResponseWriter, *http.Request) {
		started <- struct{}{}
		time.Sleep(300 * time.Millisecond)
	})
	g, err := egress.New(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{
		id: "worker-test", redis: redis, notifier: pickupNotifier{},
		httpClient: g.HTTPClient(5 * time.Second), running: newRunningTasks(),
		concurrency: 1, semaphore: make(chan struct{}, 1),
	}
	pool := &Pool{workers: []*Worker{w}, redis: redis, running: w.running}
	id := uuid.NewString()
	if err := redis.EnqueueTask(context.Background(), &models.TaskMessage{
		TaskExecID: id, WorkflowExecID: uuid.NewString(), TaskType: "http_request",
		Config: map[string]any{"url": srv.URL},
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pool.Start(ctx, 5*time.Second)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("task never started")
	}
	stop()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pool did not return after the shutdown")
	}

	res, err := redis.DequeueResult(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("DequeueResult: %v", err)
	}
	if res == nil || res.TaskExecID != id || !res.Success {
		t.Fatalf("result = %+v, want a success for the drained task", res)
	}
}

// A task still running when the grace ends is stopped and publishes nothing,
// so its row stays running for the next start's recovery to re-queue
// without spending a retry.
func TestPool_ShutdownStopsAfterGrace(t *testing.T) {
	_, redis := testutil.Env(t)
	started := make(chan struct{}, 1)
	srv, _ := countingServer(t, func(_ http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	g, err := egress.New(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{
		id: "worker-test", redis: redis, notifier: pickupNotifier{},
		httpClient: g.HTTPClient(30 * time.Second), running: newRunningTasks(),
		concurrency: 1, semaphore: make(chan struct{}, 1),
	}
	pool := &Pool{workers: []*Worker{w}, redis: redis, running: w.running}
	if err := redis.EnqueueTask(context.Background(), &models.TaskMessage{
		TaskExecID: uuid.NewString(), WorkflowExecID: uuid.NewString(), TaskType: "http_request",
		Config: map[string]any{"url": srv.URL},
	}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		pool.Start(ctx, 200*time.Millisecond)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("task never started")
	}
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pool did not stop the task after its grace")
	}

	if res, err := redis.DequeueResult(context.Background(), 500*time.Millisecond); err != nil || res != nil {
		t.Fatalf("DequeueResult = %+v, %v; want no result", res, err)
	}
}
