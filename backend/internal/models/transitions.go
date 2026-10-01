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
