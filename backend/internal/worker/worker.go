package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/egress"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/storage"
)

var dbCache sync.Map // map[string]*sql.DB

// TaskNotifier is implemented by the orchestrator to receive worker lifecycle events.
// Using an interface avoids a circular import.
type TaskNotifier interface {
	MarkTaskRunning(ctx context.Context, taskExecID, workerID string) error
	StreamLog(workflowExecID, taskExecID, taskName string, entry models.LogEntry)
}

// Worker pulls tasks from Redis, executes them using the config provided by the
// frontend, and publishes results back. All execution is driven by msg.Config
// which flows directly from the task definition saved in DB.
type Worker struct {
	id          string
	redis       *persistence.RedisClient
	notifier    TaskNotifier
	executor    *ContainerExecutor // nil if Docker unavailable
	concurrency int
	semaphore   chan struct{}
	httpClient  *http.Client // guarded: every task-initiated request goes through the egress guard
}

// Pool manages a set of concurrent workers.
type Pool struct {
	workers []*Worker
	redis   *persistence.RedisClient
}

// NewPool creates workers with all capabilities: task notification,
// container isolation, and artifact storage. Outbound requests made by tasks
// are restricted by guard.
func NewPool(
	redis *persistence.RedisClient,
	workerCount, concurrencyPerWorker int,
	notifier TaskNotifier,
	storageClient *storage.Client,
	ws Workspace,
	guard *egress.Guard,
) (*Pool, error) {
	var executor *ContainerExecutor
	execErr := errors.New("artifact storage (MinIO) unavailable")
	if storageClient != nil {
		executor, execErr = NewContainerExecutor(storageClient, ws)
		// A configured volume means the deployment expects task containers, so a
		// broken setup must stop startup rather than leave code tasks failing.
		if execErr != nil && ws.Volume != "" {
			return nil, fmt.Errorf("container executor: %w", execErr)
		}
	}
	if execErr != nil {
		// http_request, database_query and notification tasks still work, so the
		// backend starts; code tasks fail closed in dispatch.
		log.Warn().Err(execErr).Msg("container runtime unavailable: data_transform, generic and ml_inference tasks will fail; restart the backend once Docker and MinIO are reachable")
	}

	httpClient := guard.HTTPClient(60 * time.Second)
	workers := make([]*Worker, workerCount)
	for i := 0; i < workerCount; i++ {
		workers[i] = &Worker{
			id:          fmt.Sprintf("worker-%s", uuid.New().String()[:8]),
			redis:       redis,
			notifier:    notifier,
			executor:    executor,
			concurrency: concurrencyPerWorker,
			semaphore:   make(chan struct{}, concurrencyPerWorker),
			httpClient:  httpClient,
		}
	}
	return &Pool{workers: workers, redis: redis}, nil
}

func (p *Pool) Start(ctx context.Context) {
	var wg sync.WaitGroup
	for _, w := range p.workers {
		wg.Add(1)
		go func(worker *Worker) {
			defer wg.Done()
			worker.run(ctx)
		}(w)
	}
	log.Info().Int("worker_count", len(p.workers)).Msg("worker pool started")
	wg.Wait()
}

func (w *Worker) run(ctx context.Context) {
	log.Info().Str("worker_id", w.id).Msg("worker started")
	for {
		select {
		case <-ctx.Done():
			log.Info().Str("worker_id", w.id).Msg("worker shutting down")
			return
		default:
			msg, err := w.redis.DequeueTask(ctx, 2*time.Second)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Error().Err(err).Str("worker_id", w.id).Msg("dequeue error")
				time.Sleep(time.Second)
				continue
			}
			if msg == nil {
				continue
			}

			// Acquire concurrency slot
			select {
			case w.semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}

			go func(ctx context.Context, taskMsg *models.TaskMessage) {
				defer func(ctx context.Context) {
					if r := recover(); r != nil {
						// Log the panic on the worker
						log.Error().
							Interface("panic", r).
							Str("worker_id", w.id).
							Str("task_exec_id", taskMsg.TaskExecID).
							Msg("recovered from panic during task execution")

						// Notify the orchestrator immediately
						reportCtx, reportCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
						defer reportCancel()

						errMsg := fmt.Sprintf("worker panic: %v", r)

						result := &models.TaskResult{
							TaskExecID:     taskMsg.TaskExecID,
							WorkflowExecID: taskMsg.WorkflowExecID,
							WorkerID:       w.id,
							RetryCount:     &taskMsg.RetryCount,
							Success:        false,
							Error:          errMsg,
							StartedAt:      time.Now(), // Fallback approximation
							CompletedAt:    time.Now(),
						}

						if err := w.redis.PublishResult(reportCtx, result); err != nil {
							log.Error().
								Err(err).
								Str("task_exec_id", taskMsg.TaskExecID).
								Msg("failed to publish panic result to orchestrator")
						}
					}

					// Release the concurrency slot back to the worker pool
					<-w.semaphore
				}(ctx)

				w.executeTask(ctx, taskMsg)
			}(ctx, msg)
		}
	}
}

// executeTask acquires the idempotency lock, marks the task running, runs it
// through dispatch and publishes the result.
func (w *Worker) executeTask(ctx context.Context, msg *models.TaskMessage) {
	startedAt := time.Now()

	taskLogger := log.With().
		Str("worker_id", w.id).
		Str("task_exec_id", msg.TaskExecID).
		Str("task_type", msg.TaskType).
		Str("task_name", msg.TaskName).
		Int("retry", msg.RetryCount).
		Bool("isolated", w.usesContainer(msg)).
		Logger()

	taskLogger.Info().Msg("executing task")

	var logs []models.LogEntry
	addLog := func(level, message string, fields map[string]any) {
		entry := models.LogEntry{
			Timestamp: time.Now(),
			Level:     level,
			Message:   message,
			Fields:    fields,
		}
		logs = append(logs, entry)

		// Broadcast immediately so the UI streams output in real time
		if w.notifier != nil {
			w.notifier.StreamLog(msg.WorkflowExecID, msg.TaskExecID, msg.TaskName, entry)
		}
	}

	// Create execution context with timeout
	taskCtx := ctx
	if msg.Timeout > 0 {
		var cancel context.CancelFunc
		taskCtx, cancel = context.WithTimeout(ctx, msg.Timeout)
		defer cancel()
	}

	// Distributed idempotency lock — prevents double-execution on re-delivery
	locked, err := w.redis.AcquireTaskLock(taskCtx, msg.TaskExecID, 10*time.Minute)
	if err != nil || !locked {
		log.Warn().Str("task_exec_id", msg.TaskExecID).Msg("task already locked, skipping")
		return
	}
	defer func() { _ = w.redis.ReleaseTaskLock(ctx, msg.TaskExecID) }()

	addLog("info", "Task execution started", map[string]any{
		"worker_id": w.id,
		"task_type": msg.TaskType,
		"task_name": msg.TaskName,
		"retry":     msg.RetryCount,
		"isolated":  w.usesContainer(msg),
	})

	// Notify orchestrator: Queued → Running
	if w.notifier != nil {
		if err := w.notifier.MarkTaskRunning(ctx, msg.TaskExecID, w.id); err != nil {
			log.Warn().Err(err).Str("task_exec_id", msg.TaskExecID).Msg("failed to mark task running")
		}
	}

	output, artifactsOut, execErr := w.dispatch(taskCtx, msg, addLog)

	completedAt := time.Now()

	result := &models.TaskResult{
		TaskExecID:     msg.TaskExecID,
		WorkflowExecID: msg.WorkflowExecID,
		WorkerID:       w.id,
		RetryCount:     &msg.RetryCount,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
		ArtifactsOut:   artifactsOut,
	}

	if execErr != nil {
		result.Success = false
		result.Error = execErr.Error()
		addLog("error", "Task failed", map[string]any{
			"error":         execErr.Error(),
			"duration_ms":   completedAt.Sub(startedAt).Milliseconds(),
			"artifacts_out": len(artifactsOut),
		})
	} else {
		result.Success = true
		if out, err := json.Marshal(output); err == nil {
			result.Output = out
		}
		addLog("info", "Task completed successfully", map[string]any{
			"duration_ms": completedAt.Sub(startedAt).Milliseconds(),
		})
	}
	result.Logs = logs

	if err := w.redis.PublishResult(ctx, result); err != nil {
		log.Error().Err(err).Str("task_exec_id", msg.TaskExecID).Msg("failed to publish result")
	}
}

type logFn func(level, message string, fields map[string]any)

// runsUserCode reports whether a task type executes a user-supplied script,
// command or binary. Unknown types count as code: they used to fall through
// to the shell executor, so saved definitions may carry commands.
func runsUserCode(taskType string) bool {
	switch taskType {
	case "http_request", "database_query", "notification":
		return false
	default:
		return true
	}
}

func (w *Worker) usesContainer(msg *models.TaskMessage) bool {
	return runsUserCode(msg.TaskType) || (msg.Container != nil && w.executor != nil)
}

// dispatch runs msg to completion. User code runs only in a task container;
// without a container executor those tasks fail closed (ADR 0003).
func (w *Worker) dispatch(ctx context.Context, msg *models.TaskMessage, addLog logFn) (map[string]any, []models.ResolvedArtifact, error) {
	if !w.usesContainer(msg) {
		out, err := w.dispatchInProcess(ctx, msg, addLog)
		return out, nil, err
	}
	// Runs nothing, so it needs no container; demo templates and saved
	// placeholder tasks rely on it.
	if msg.TaskType == "generic" && buildCommand(msg) == nil {
		addLog("info", "No command configured — task is a no-op", nil)
		return map[string]any{"status": "no-op"}, nil, nil
	}
	if w.executor == nil {
		return nil, nil, fmt.Errorf("container runtime unavailable: %s tasks run only in containers (restart the backend once Docker and MinIO are reachable)", msg.TaskType)
	}

	stdout, arts, err := w.executor.Run(ctx, msg, addLog)
	if err != nil {
		return nil, arts, err
	}
	var output map[string]any
	// A stdout of "null" decodes without error but leaves output nil.
	if json.Unmarshal([]byte(stdout), &output) != nil || output == nil {
		output = map[string]any{"output": stdout}
	}
	if len(arts) > 0 {
		keys := make([]string, len(arts))
		for i, a := range arts {
			keys[i] = a.MinioKey
		}
		output["artifacts"] = keys
	}
	return output, arts, nil
}

func (w *Worker) dispatchInProcess(ctx context.Context, msg *models.TaskMessage, addLog logFn) (map[string]any, error) {
	switch msg.TaskType {
	case "http_request":
		return w.execHTTP(ctx, msg, addLog)
	case "database_query":
		return w.execDBQuery(ctx, msg, addLog)
	case "notification":
		return w.execNotification(ctx, msg, addLog)
	default:
		return nil, fmt.Errorf("task type %q has no in-process executor", msg.TaskType)
	}
}

func (w *Worker) execHTTP(ctx context.Context, msg *models.TaskMessage, addLog logFn) (map[string]any, error) {
	cfg := msg.Config

	rawURL, _ := cfg["url"].(string)
	if rawURL == "" {
		return nil, fmt.Errorf("http_request: 'url' is required in task config")
	}

	method, _ := cfg["method"].(string)
	if method == "" {
		method = "GET"
	}
	method = strings.ToUpper(method)

	var bodyBytes []byte
	switch b := cfg["body"].(type) {
	case string:
		bodyBytes = []byte(b)
	case map[string]any:
		bodyBytes, _ = json.Marshal(b)
	}

	reqTimeout := 30 * time.Second
	if ms, ok := cfg["timeout_ms"].(float64); ok && ms > 0 {
		reqTimeout = time.Duration(ms) * time.Millisecond
	}

	httpCtx, cancel := context.WithTimeout(ctx, reqTimeout)
	defer cancel()

	var bodyReader io.Reader
	if len(bodyBytes) > 0 {
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequestWithContext(httpCtx, method, rawURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("http_request: %w", withoutURL(err))
	}
	if len(bodyBytes) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if hdrs, ok := cfg["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if vs, ok := v.(string); ok {
				req.Header.Set(k, vs)
			}
		}
	}

	addLog("info", fmt.Sprintf("→ %s %s", method, rawURL), map[string]any{
		"body_bytes": len(bodyBytes),
	})

	resp, err := w.do(req)
	if err != nil {
		return nil, fmt.Errorf("http_request: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Msg("failed to close response body")
		}
	}()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	addLog("info", fmt.Sprintf("← %d %s", resp.StatusCode, http.StatusText(resp.StatusCode)), map[string]any{
		"status":         resp.StatusCode,
		"response_bytes": len(respBody),
	})

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("http_request: server returned %d: %s", resp.StatusCode, truncate(string(respBody), 200))
	}

	var parsedBody any
	if json.Unmarshal(respBody, &parsedBody) != nil {
		parsedBody = string(respBody)
	}
	return map[string]any{
		"status":         resp.StatusCode,
		"response_bytes": len(respBody),
		"body":           parsedBody,
	}, nil
}

// ─── Database Query ───────────────────────────────────────────────────────────
// Config: connection_string (string), query (string), max_rows (number)

func (w *Worker) execDBQuery(ctx context.Context, msg *models.TaskMessage, addLog logFn) (map[string]any, error) {
	cfg := msg.Config

	connStr, _ := cfg["connection_string"].(string)
	if connStr == "" {
		return nil, fmt.Errorf("database_query: 'connection_string' is required")
	}
	query, _ := cfg["query"].(string)
	if query == "" {
		return nil, fmt.Errorf("database_query: 'query' is required")
	}

	maxRows := 10000
	if mr, ok := cfg["max_rows"].(float64); ok && mr > 0 {
		maxRows = int(mr)
	}

	// Not guarded yet, and harmless today because no SQL driver is registered,
	// so sql.Open fails. S4 (database_query allowlist) adds the driver and
	// routes its connections through egress.Guard.DialContext; until then this
	// path must not gain a driver.
	driver := "postgres"
	if strings.HasPrefix(connStr, "mysql://") {
		driver = "mysql"
		connStr = strings.TrimPrefix(connStr, "mysql://")
	}

	addLog("info", fmt.Sprintf("Connecting (%s)", driver), nil)

	// Use cached connection pool
	var db *sql.DB
	if cached, ok := dbCache.Load(connStr); ok {
		db = cached.(*sql.DB)
	} else {
		var err error
		db, err = sql.Open(driver, connStr)
		if err != nil {
			return nil, fmt.Errorf("database_query: open: %w", err)
		}
		// Configure pool limits appropriately
		db.SetConnMaxLifetime(30 * time.Minute)
		db.SetMaxOpenConns(5)
		dbCache.Store(connStr, db)
	}

	addLog("info", "Executing query", map[string]any{"query": truncate(query, 200)})

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("database_query: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Msg("failed to close sql rows")
		}
	}()

	cols, _ := rows.Columns()
	var results []map[string]any
	for rows.Next() {
		if len(results) >= maxRows {
			addLog("warn", fmt.Sprintf("max_rows limit (%d) reached", maxRows), nil)
			break
		}
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range ptrs {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = vals[i]
			}
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("database_query: scan: %w", err)
	}

	addLog("info", "Query complete", map[string]any{"rows": len(results), "columns": cols})
	return map[string]any{"rows": results, "columns": cols, "row_count": len(results)}, nil
}

// ─── Notification ─────────────────────────────────────────────────────────────
// Config: notify_type (slack|email|webhook|pagerduty), channel (string), message

func (w *Worker) execNotification(ctx context.Context, msg *models.TaskMessage, addLog logFn) (map[string]any, error) {
	cfg := msg.Config

	notifyType, _ := cfg["notify_type"].(string)
	channel, _ := cfg["channel"].(string)
	message, _ := cfg["message"].(string)

	if channel == "" {
		return nil, fmt.Errorf("notification: 'channel' is required")
	}
	if message == "" {
		return nil, fmt.Errorf("notification: 'message' is required")
	}

	addLog("info", fmt.Sprintf("Sending %s to %s", notifyType, channel), nil)

	switch notifyType {
	case "slack":
		return w.notifySlack(ctx, channel, message, addLog)
	case "email":
		return w.notifyEmail(ctx, channel, message, addLog)
	case "pagerduty":
		return w.notifyPagerDuty(ctx, channel, message, addLog)
	default:
		// Treat channel as a webhook URL
		return w.notifyWebhook(ctx, channel, message, addLog)
	}
}

func (w *Worker) notifySlack(ctx context.Context, webhookURL, message string, addLog logFn) (map[string]any, error) {
	body, _ := json.Marshal(map[string]any{"text": message})
	req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("slack: %w", withoutURL(err))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.do(req)
	if err != nil {
		return nil, fmt.Errorf("slack: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Msg("failed to close response body")
		}
	}()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("slack: %d %s", resp.StatusCode, string(raw))
	}
	addLog("info", "Slack delivered", nil)
	return map[string]any{"delivered": true}, nil
}

func (w *Worker) notifyEmail(ctx context.Context, to, message string, addLog logFn) (map[string]any, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Not user code: the argv is fixed and user text only reaches sendmail's
	// stdin, so this stays outside the container-only rule.
	cmd := exec.CommandContext(cmdCtx, "sendmail", "-t")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("To: %s\nSubject: Fluxor Notification\n\n%s\n", to, message))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("email: sendmail: %w — %s", err, stderr.String())
	}
	addLog("info", "Email sent", map[string]any{"to": to})
	return map[string]any{"delivered": true, "to": to}, nil
}

func (w *Worker) notifyPagerDuty(ctx context.Context, routingKey, message string, addLog logFn) (map[string]any, error) {
	body, _ := json.Marshal(map[string]any{
		"routing_key":  routingKey,
		"event_action": "trigger",
		"payload": map[string]any{
			"summary":  message,
			"severity": "info",
			"source":   "fluxor",
		},
	})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://events.pagerduty.com/v2/enqueue", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("pagerduty: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.do(req)
	if err != nil {
		return nil, fmt.Errorf("pagerduty: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Msg("failed to close response body")
		}
	}()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("pagerduty: %d %s", resp.StatusCode, string(raw))
	}
	addLog("info", "PagerDuty triggered", nil)
	return map[string]any{"delivered": true}, nil
}

func (w *Worker) notifyWebhook(ctx context.Context, url, message string, addLog logFn) (map[string]any, error) {
	body, _ := json.Marshal(map[string]any{
		"message":   message,
		"source":    "fluxor",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("webhook: %w", withoutURL(err))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.do(req)
	if err != nil {
		return nil, fmt.Errorf("webhook: %w", err)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Msg("failed to close response body")
		}
	}()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("webhook: %d %s", resp.StatusCode, truncate(string(raw), 200))
	}
	addLog("info", "Webhook delivered", map[string]any{"status": resp.StatusCode})
	return map[string]any{"delivered": true, "status": resp.StatusCode}, nil
}

func (w *Worker) do(req *http.Request) (*http.Response, error) {
	resp, err := w.httpClient.Do(req)
	if err != nil {
		return nil, withoutURL(err)
	}
	return resp, nil
}

// withoutURL drops the *url.Error wrapper, whose message repeats the full URL:
// webhook URLs carry their secret in the path or query, and task errors are
// stored and shown in the UI.
func withoutURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
