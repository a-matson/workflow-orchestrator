package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds all Prometheus counters/gauges for the orchestrator
type Metrics struct {
	WorkflowsStarted   prometheus.Counter
	WorkflowsCompleted prometheus.Counter
	WorkflowsFailed    prometheus.Counter

	TasksDispatched   prometheus.Counter
	TasksCompleted    prometheus.Counter
	TasksFailed       prometheus.Counter
	TasksRetried      prometheus.Counter
	TasksDeadLettered prometheus.Counter

	ActiveWorkflows prometheus.Gauge
	QueueDepth      prometheus.Gauge
	RetryQueueDepth prometheus.Gauge
	WSClients       prometheus.Gauge

	TaskDuration prometheus.Histogram
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		WorkflowsStarted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "workflow_executions_started_total",
			Help: "Total workflow executions started",
		}),
		WorkflowsCompleted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "workflow_executions_completed_total",
			Help: "Total workflow executions completed successfully",
		}),
		WorkflowsFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "workflow_executions_failed_total",
			Help: "Total workflow executions that failed",
		}),
		TasksDispatched: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tasks_dispatched_total",
			Help: "Total tasks dispatched to the queue",
		}),
		TasksCompleted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tasks_completed_total",
			Help: "Total tasks completed successfully",
		}),
		TasksFailed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tasks_failed_total",
			Help: "Total tasks that failed permanently",
		}),
		TasksRetried: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tasks_retried_total",
			Help: "Total task retry attempts",
		}),
		TasksDeadLettered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tasks_dead_lettered_total",
			Help: "Total tasks moved to dead letter queue",
		}),
		ActiveWorkflows: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "active_workflow_executions",
			Help: "Number of currently active workflow executions",
		}),
		QueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "task_queue_depth",
			Help: "Current depth of the Redis task queue",
		}),
		RetryQueueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "retry_queue_depth",
			Help: "Current number of tasks awaiting retry",
		}),
		WSClients: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "websocket_clients_connected",
			Help: "Number of currently connected WebSocket clients",
		}),
		TaskDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "task_duration_seconds",
			Help:    "Task execution duration distribution",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
		}),
	}

	reg.MustRegister(
		m.WorkflowsStarted, m.WorkflowsCompleted, m.WorkflowsFailed,
		m.TasksDispatched, m.TasksCompleted, m.TasksFailed,
		m.TasksRetried, m.TasksDeadLettered,
		m.ActiveWorkflows, m.QueueDepth, m.RetryQueueDepth, m.WSClients,
		m.TaskDuration,
	)

	return m
}

// Handler returns the Prometheus HTTP handler for /metrics
func Handler() http.Handler {
	return promhttp.Handler()
}
