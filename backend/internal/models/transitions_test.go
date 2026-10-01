package models

import (
	"slices"
	"testing"
)

func TestTaskFrom_TerminalHasNoExits(t *testing.T) {
	terminal := []TaskStatus{TaskStatusCompleted, TaskStatusDeadLetter, TaskStatusCancelled, TaskStatusSkipped}
	// Ranges over the table itself so a newly added target is checked too.
	for to, from := range taskFrom {
		if len(from) == 0 {
			t.Errorf("TaskFrom(%s) is empty", to)
		}
		if dup := duplicate(from); dup != "" {
			t.Errorf("TaskFrom(%s) lists %s twice", to, dup)
		}
		for _, term := range terminal {
			if slices.Contains(from, term) {
				t.Errorf("TaskFrom(%s) contains terminal status %s", to, term)
			}
		}
	}
	for _, to := range []TaskStatus{TaskStatusFailed} {
		if from := TaskFrom(to); from != nil {
			t.Errorf("TaskFrom(%s) = %v, want nil", to, from)
		}
	}
}

func TestExecFrom_TerminalHasNoExits(t *testing.T) {
	terminal := []WorkflowStatus{WorkflowStatusCompleted, WorkflowStatusFailed, WorkflowStatusCancelled}
	for to, from := range execFrom {
		if len(from) == 0 {
			t.Errorf("ExecFrom(%s) is empty", to)
		}
		if dup := duplicate(from); dup != "" {
			t.Errorf("ExecFrom(%s) lists %s twice", to, dup)
		}
		for _, term := range terminal {
			if slices.Contains(from, term) {
				t.Errorf("ExecFrom(%s) contains terminal status %s", to, term)
			}
		}
	}
	if from := ExecFrom(WorkflowStatusPaused); from != nil {
		t.Errorf("ExecFrom(paused) = %v, want nil", from)
	}
}

func TestTaskFrom_ReturnsCopy(t *testing.T) {
	TaskFrom(TaskStatusRunning)[0] = TaskStatusCompleted
	if got := TaskFrom(TaskStatusRunning); got[0] != TaskStatusQueued {
		t.Fatalf("mutating the result changed the table: TaskFrom(running) = %v", got)
	}
	ExecFrom(WorkflowStatusRunning)[0] = WorkflowStatusCompleted
	if got := ExecFrom(WorkflowStatusRunning); got[0] != WorkflowStatusPending {
		t.Fatalf("mutating the result changed the table: ExecFrom(running) = %v", got)
	}
}

func duplicate[S ~string](s []S) S {
	for i, v := range s {
		if slices.Contains(s[i+1:], v) {
			return v
		}
	}
	return ""
}
