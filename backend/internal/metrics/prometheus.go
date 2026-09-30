package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
)

// Sources are read at scrape time.
type Sources struct {
	// Snapshot returns the orchestrator's counters, keyed as in Orchestrator.GetMetrics.
	Snapshot   func() map[string]int64
	QueueDepth func(context.Context) (int64, error)
	WSClients  func() int
}

// series maps Snapshot keys to the names the Grafana dashboard queries.
var series = []struct {
	key  string
	desc *prometheus.Desc
	typ  prometheus.ValueType
}{
	{"workflows_started", desc("workflow_executions_started_total", "Workflow executions started"), prometheus.CounterValue},
	{"workflows_completed", desc("workflow_executions_completed_total", "Workflow executions completed successfully"), prometheus.CounterValue},
	{"workflows_failed", desc("workflow_executions_failed_total", "Workflow executions that failed"), prometheus.CounterValue},
	{"workflows_cancelled", desc("workflow_executions_cancelled_total", "Workflow executions cancelled"), prometheus.CounterValue},
	{"tasks_dispatched", desc("tasks_dispatched_total", "Tasks dispatched to the queue"), prometheus.CounterValue},
	{"tasks_completed", desc("tasks_completed_total", "Tasks completed successfully"), prometheus.CounterValue},
	{"tasks_failed", desc("tasks_failed_total", "Tasks that failed permanently"), prometheus.CounterValue},
	{"tasks_retried", desc("tasks_retried_total", "Task retry attempts"), prometheus.CounterValue},
	{"tasks_dead_lettered", desc("tasks_dead_lettered_total", "Tasks moved to the dead letter queue"), prometheus.CounterValue},
	{"active_workflows", desc("active_workflow_executions", "Workflow executions in progress"), prometheus.GaugeValue},
	{"retry_queue_depth", desc("retry_queue_depth", "Tasks waiting for a retry"), prometheus.GaugeValue},
}

var queueDepthDesc = desc("task_queue_depth", "Messages in the Redis task queue")

func desc(name, help string) *prometheus.Desc { return prometheus.NewDesc(name, help, nil, nil) }

// Counters restart at zero with the process, which Prometheus' rate() treats as a counter reset.
type collector struct{ src Sources }

func (c collector) Describe(ch chan<- *prometheus.Desc) {
	for _, s := range series {
		ch <- s.desc
	}
	ch <- queueDepthDesc
}

func (c collector) Collect(ch chan<- prometheus.Metric) {
	snap := c.src.Snapshot()
	for _, s := range series {
		ch <- prometheus.MustNewConstMetric(s.desc, s.typ, float64(snap[s.key]))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// An absent series reads as "unknown" in Prometheus; a 0 would claim an empty queue.
	if depth, err := c.src.QueueDepth(ctx); err != nil {
		log.Warn().Err(err).Msg("metrics: could not read task queue depth")
	} else {
		ch <- prometheus.MustNewConstMetric(queueDepthDesc, prometheus.GaugeValue, float64(depth))
	}
}

// Register adds the app's series to reg and returns the task duration
// histogram, which the caller feeds with each completed task's run time in seconds.
func Register(reg prometheus.Registerer, src Sources) prometheus.Observer {
	durations := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "task_duration_seconds",
		Help:    "Run time of completed tasks",
		Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
	})
	reg.MustRegister(
		collector{src},
		durations,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "websocket_clients_connected",
			Help: "Connected WebSocket clients",
		}, func() float64 { return float64(src.WSClients()) }),
	)
	return durations
}

// Handler serves reg's series.
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}
