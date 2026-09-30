package orchestrator

import (
	"testing"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// A metrics call waiting on a busy execution must not hold activeMu, or a
// queued activeMu writer (StartWorkflow, completeWorkflow) would block every
// other execution's results behind that one dispatch.
func TestGetMetrics_WaitsOutsideActiveMu(t *testing.T) {
	ec := &ExecutionContext{TaskMap: map[string]*models.TaskExecution{
		"a": {Status: models.TaskStatusRetrying},
	}}
	o := &Orchestrator{active: map[string]*ExecutionContext{"e": ec}, metrics: &Metrics{}}

	ec.mu.Lock() // a dispatch in progress
	got := make(chan map[string]int64)
	go func() { got <- o.GetMetrics() }()
	// Gives GetMetrics time to reach its wait on ec.mu; without it the test
	// could pass vacuously, never fail spuriously.
	time.Sleep(50 * time.Millisecond)

	locked := make(chan struct{})
	go func() {
		o.activeMu.Lock() // as StartWorkflow registers an execution
		o.active["f"] = &ExecutionContext{}
		o.activeMu.Unlock()
		close(locked)
	}()
	select {
	case <-locked:
	case <-time.After(time.Second):
		ec.mu.Unlock()
		<-got
		<-locked
		t.Fatal("activeMu writer blocked while GetMetrics waited on an execution lock")
	}

	ec.mu.Unlock()
	// "f" may or may not be counted; it has no rows either way.
	if m := <-got; m["retry_queue_depth"] != 1 {
		t.Errorf("retry_queue_depth = %d, want 1", m["retry_queue_depth"])
	}
}
