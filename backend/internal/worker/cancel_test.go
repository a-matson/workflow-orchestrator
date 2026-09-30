package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestRunningTasks_CancelEndsEveryRunOfTheTask(t *testing.T) {
	r := newRunningTasks()
	a1, releaseA1 := r.track(context.Background(), "a")
	defer releaseA1()
	a2, releaseA2 := r.track(context.Background(), "a")
	defer releaseA2()
	b, releaseB := r.track(context.Background(), "b")
	defer releaseB()

	r.Cancel("a")

	for i, ctx := range []context.Context{a1, a2} {
		if !errors.Is(context.Cause(ctx), errTaskCancelled) {
			t.Errorf("run %d of a: cause = %v, want errTaskCancelled", i, context.Cause(ctx))
		}
	}
	if b.Err() != nil {
		t.Errorf("b was cancelled with %v, want it running", context.Cause(b))
	}
}

// A released run must not keep its entry, or the map grows with every task,
// and its cause must not read as a cancel.
func TestRunningTasks_ReleaseRemovesTheRun(t *testing.T) {
	r := newRunningTasks()
	ctx, release := r.track(context.Background(), "a")
	release()

	if len(r.runs) != 0 {
		t.Errorf("runs = %v after release, want empty", r.runs)
	}
	if errors.Is(context.Cause(ctx), errTaskCancelled) {
		t.Error("a released run reports errTaskCancelled")
	}
	r.Cancel("a") // no run left: a no-op, not a panic
}

func TestRunningTasks_ConcurrentUse(t *testing.T) {
	r := newRunningTasks()
	var wg sync.WaitGroup
	for i := range 50 {
		id := fmt.Sprint(i % 5)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, release := r.track(context.Background(), id)
			release()
		}()
		go func() {
			defer wg.Done()
			r.Cancel(id)
		}()
	}
	wg.Wait()
	if len(r.runs) != 0 {
		t.Errorf("runs = %v after every run released, want empty", r.runs)
	}
}
