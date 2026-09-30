package worker

import (
	"context"
	"errors"
	"sync"
)

// errTaskCancelled is the cause of a run's context when its task was
// cancelled, telling that apart from a timeout or a shutdown.
var errTaskCancelled = errors.New("task cancelled")

// errShutdown is the cause of a run's context when the pool's drain grace ran
// out before the run finished.
var errShutdown = errors.New("worker pool shut down")

// runningTasks maps task executions to the contexts of their runs in this
// process. A task can have more than one run: a redelivered message registers
// before its pickup is refused, and Cancel must reach the real run either way.
type runningTasks struct {
	mu   sync.Mutex
	next uint64
	runs map[string]map[uint64]context.CancelCauseFunc
}

func newRunningTasks() *runningTasks {
	return &runningTasks{runs: make(map[string]map[uint64]context.CancelCauseFunc)}
}

// track returns a context for one run of taskExecID that Cancel ends, and a
// release func the run must call when it finishes.
func (r *runningTasks) track(parent context.Context, taskExecID string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	r.mu.Lock()
	id := r.next
	r.next++
	if r.runs[taskExecID] == nil {
		r.runs[taskExecID] = make(map[uint64]context.CancelCauseFunc)
	}
	r.runs[taskExecID][id] = cancel
	r.mu.Unlock()

	return ctx, func() {
		r.mu.Lock()
		delete(r.runs[taskExecID], id)
		if len(r.runs[taskExecID]) == 0 {
			delete(r.runs, taskExecID)
		}
		r.mu.Unlock()
		cancel(nil)
	}
}

// Cancel ends every run of taskExecID in this process; a task with no run
// here is a no-op.
func (r *runningTasks) Cancel(taskExecID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, cancel := range r.runs[taskExecID] {
		cancel(errTaskCancelled)
	}
}

// Cancel stops taskExecID's run on any worker of the pool. A container task
// has its container killed and removed.
func (p *Pool) Cancel(taskExecID string) {
	p.running.Cancel(taskExecID)
}
