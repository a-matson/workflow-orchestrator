package models

import "slices"

// taskFrom maps each reachable task status to the statuses it may be entered
// from. A status missing as a key cannot be transitioned to.
var taskFrom = map[TaskStatus][]TaskStatus{
	TaskStatusQueued:     {TaskStatusPending, TaskStatusRetrying}, // dispatch (caller checks retry due)
	TaskStatusRunning:    {TaskStatusQueued},                      // worker pickup
	TaskStatusCompleted:  {TaskStatusRunning},
	TaskStatusRetrying:   {TaskStatusRunning}, // failure with retries left; timeout reaper
	TaskStatusDeadLetter: {TaskStatusRunning},
	TaskStatusPending:    {TaskStatusQueued, TaskStatusRunning}, // enqueue rollback; startup requeue (single node)
	TaskStatusCancelled:  {TaskStatusPending, TaskStatusQueued, TaskStatusRetrying, TaskStatusRunning},
}

var execFrom = map[WorkflowStatus][]WorkflowStatus{
	WorkflowStatusRunning:   {WorkflowStatusPending},
	WorkflowStatusCompleted: {WorkflowStatusRunning},
	WorkflowStatusFailed:    {WorkflowStatusPending, WorkflowStatusRunning},
	WorkflowStatusCancelled: {WorkflowStatusPending, WorkflowStatusRunning},
}

// Resume reopens a finished execution, the only way out of a terminal status,
// and only Store.ResumeExecution takes it: the execution goes back to running
// and every task that did not complete goes back to pending, while completed
// tasks keep their results.
var (
	execResumeFrom = []WorkflowStatus{WorkflowStatusFailed, WorkflowStatusCancelled}
	taskResumeFrom = []TaskStatus{TaskStatusDeadLetter, TaskStatusCancelled, TaskStatusFailed}
)

// ExecResumeFrom returns the execution statuses resume accepts. The slice is a copy.
func ExecResumeFrom() []WorkflowStatus { return slices.Clone(execResumeFrom) }

// TaskResumeFrom returns the task statuses resume moves back to pending. The slice is a copy.
func TaskResumeFrom() []TaskStatus { return slices.Clone(taskResumeFrom) }

// TaskFrom returns the statuses a task may move to `to` from, or nil when
// `to` is not a valid transition target. The slice is a copy.
func TaskFrom(to TaskStatus) []TaskStatus {
	return slices.Clone(taskFrom[to])
}

// ExecFrom returns the statuses an execution may move to `to` from, or nil
// when `to` is not a valid transition target. The slice is a copy.
func ExecFrom(to WorkflowStatus) []WorkflowStatus {
	return slices.Clone(execFrom[to])
}
