package orchestrator

import "testing"

func TestRunSafe_RecoversPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic escaped runSafe: %v", r)
		}
	}()
	var m map[string]*int
	runSafe("dispatch", "exec-1", func() { _ = *m["missing"] })
}
