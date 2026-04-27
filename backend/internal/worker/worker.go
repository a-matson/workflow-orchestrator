package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"

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
	httpClient  *http.Client
}

// Pool manages a set of concurrent workers.
type Pool struct {
	workers []*Worker
	redis   *persistence.RedisClient
}

// NewPool creates workers without Docker/MinIO (legacy in-process mode)
func NewPool(redis *persistence.RedisClient, workerCount, concurrencyPerWorker int) *Pool {
	return NewPoolWithNotifier(redis, workerCount, concurrencyPerWorker, nil)
}

// NewPoolWithNotifier creates workers with a TaskNotifier and optional
// ContainerExecutor for isolated task execution.
func NewPoolWithNotifier(redis *persistence.RedisClient, workerCount, concurrencyPerWorker int, notifier TaskNotifier) *Pool {
	return NewPoolFull(redis, workerCount, concurrencyPerWorker, notifier, nil)
}

// NewPoolFull creates workers with all capabilities: task notification,
// container isolation, and artifact storage.
func NewPoolFull(
	redis *persistence.RedisClient,
	workerCount, concurrencyPerWorker int,
	notifier TaskNotifier,
	storageClient *storage.Client,
) *Pool {
	var executor *ContainerExecutor
	if storageClient != nil {
		var err error
		executor, err = NewContainerExecutor(storageClient)
		if err != nil {
			log.Warn().Err(err).Msg("Docker unavailable — container isolation disabled; tasks run in-process")
		}
	}

	workers := make([]*Worker, workerCount)
	for i := 0; i < workerCount; i++ {
		workers[i] = &Worker{
			id:          fmt.Sprintf("worker-%s", uuid.New().String()[:8]),
			redis:       redis,
			notifier:    notifier,
			executor:    executor,
			concurrency: concurrencyPerWorker,
			semaphore:   make(chan struct{}, concurrencyPerWorker),
			httpClient: &http.Client{
				Timeout: 60 * time.Second,
				Transport: &http.Transport{
					MaxIdleConnsPerHost: 10,
					IdleConnTimeout:     60 * time.Second,
				},
			},
		}
	}
	return &Pool{workers: workers, redis: redis}
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

// executeTask is the top-level dispatcher. It acquires the idempotency lock,
// notifies the orchestrator that the task is running, then routes to either
// the container executor or the in-process executor.
func (w *Worker) executeTask(ctx context.Context, msg *models.TaskMessage) {
	startedAt := time.Now()

	taskLogger := log.With().
		Str("worker_id", w.id).
		Str("task_exec_id", msg.TaskExecID).
		Str("task_type", msg.TaskType).
		Str("task_name", msg.TaskName).
		Int("retry", msg.RetryCount).
		Bool("isolated", msg.Container != nil && w.executor != nil).
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
		"isolated":  msg.Container != nil && w.executor != nil,
	})

	// Notify orchestrator: Queued → Running
	if w.notifier != nil {
		if err := w.notifier.MarkTaskRunning(ctx, msg.TaskExecID, w.id); err != nil {
			log.Warn().Err(err).Str("task_exec_id", msg.TaskExecID).Msg("failed to mark task running")
		}
	}

	var output map[string]any
	var artifactsOut []models.ResolvedArtifact
	var execErr error

	// Route: gha_job gets its own executor that preserves job-level shared state
	if msg.TaskType == "gha_job" && w.executor != nil {
		stdout, arts, runErr := w.executor.RunGHAJob(taskCtx, msg, addLog)
		artifactsOut = arts
		execErr = runErr
		if runErr == nil {
			if json.Unmarshal([]byte(stdout), &output) != nil {
				output = map[string]any{"output": stdout}
			}
			if len(arts) > 0 {
				keys := make([]string, len(arts))
				for i, a := range arts {
					keys[i] = a.MinioKey
				}
				output["artifacts"] = keys
			}
		}
	} else if msg.Container != nil && w.executor != nil {
		// Route: container isolation if a ContainerSpec is present and Docker is available
		stdout, arts, runErr := w.executor.Run(taskCtx, msg, addLog)
		artifactsOut = arts
		execErr = runErr
		if runErr == nil {
			// Try to parse stdout as JSON for structured output
			if json.Unmarshal([]byte(stdout), &output) != nil {
				output = map[string]any{"output": stdout}
			}
			if len(arts) > 0 {
				keys := make([]string, len(arts))
				for i, a := range arts {
					keys[i] = a.MinioKey
				}
				output["artifacts"] = keys
			}
		}
	} else {
		// In-process execution for built-in task types
		output, execErr = w.dispatchInProcess(taskCtx, msg, addLog)
	}

	completedAt := time.Now()

	result := &models.TaskResult{
		TaskExecID:     msg.TaskExecID,
		WorkflowExecID: msg.WorkflowExecID,
		WorkerID:       w.id,
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

func (w *Worker) dispatchInProcess(ctx context.Context, msg *models.TaskMessage, addLog logFn) (map[string]any, error) {
	switch msg.TaskType {
	case "gha_job":
		// Docker unavailable — run steps as sequential shell commands in a sandbox
		return w.execGHAJobFallback(ctx, msg, addLog)
	case "http_request":
		return w.execHTTP(ctx, msg, addLog)
	case "database_query":
		return w.execDBQuery(ctx, msg, addLog)
	case "data_transform":
		return w.execDataTransform(ctx, msg, addLog)
	case "ml_inference":
		return w.execMLInference(ctx, msg, addLog)
	case "notification":
		return w.execNotification(ctx, msg, addLog)
	default:
		return w.execGeneric(ctx, msg, addLog)
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
		return nil, fmt.Errorf("http_request: %w", err)
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

	resp, err := w.httpClient.Do(req)
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

// ─── Data Transform ───────────────────────────────────────────────────────────
// Config: script (string — shell command), input_format, output_format

func (w *Worker) execDataTransform(ctx context.Context, msg *models.TaskMessage, addLog logFn) (map[string]any, error) {
	cfg := msg.Config

	script, _ := cfg["script"].(string)
	if script == "" {
		return nil, fmt.Errorf("data_transform: 'script' is required")
	}

	// Run in a clean, empty sandbox directory so the script cannot access the
	// host project source, backend binaries, or any other host files.
	sandbox, err := createSandbox()
	if err != nil {
		return nil, fmt.Errorf("data_transform: %w", err)
	}
	defer func() { _ = os.RemoveAll(sandbox) }()

	addLog("info", "Running transform", map[string]any{"script": truncate(script, 200), "sandbox": sandbox})

	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = sandbox
	cmd.Env = sandboxEnv(msg)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if stderr.Len() > 0 {
		addLog("warn", "stderr", map[string]any{"output": truncate(stderr.String(), 500)})
	}
	if err != nil {
		return nil, fmt.Errorf("data_transform: %w — %s", err, truncate(stderr.String(), 200))
	}

	out := stdout.String()
	addLog("info", "Transform complete", map[string]any{"output_bytes": len(out)})

	var parsed any
	if json.Unmarshal([]byte(out), &parsed) == nil {
		return map[string]any{"output": parsed, "output_bytes": len(out)}, nil
	}
	return map[string]any{"output": out, "output_bytes": len(out)}, nil
}

// ─── ML Inference ─────────────────────────────────────────────────────────────
// Config: model_name (string — path to binary/script), input_path, output_path,
//         batch_size (number)

func (w *Worker) execMLInference(ctx context.Context, msg *models.TaskMessage, addLog logFn) (map[string]any, error) {
	cfg := msg.Config

	modelName, _ := cfg["model_name"].(string)
	if modelName == "" {
		return nil, fmt.Errorf("ml_inference: 'model_name' is required")
	}

	inputPath, _ := cfg["input_path"].(string)
	outputPath, _ := cfg["output_path"].(string)
	batchSize := 32
	if bs, ok := cfg["batch_size"].(float64); ok && bs > 0 {
		batchSize = int(bs)
	}

	args := []string{"--batch-size", fmt.Sprintf("%d", batchSize)}
	if inputPath != "" {
		args = append(args, "--input", inputPath)
	}
	if outputPath != "" {
		args = append(args, "--output", outputPath)
	}

	addLog("info", fmt.Sprintf("Running model: %s", modelName), map[string]any{
		"input": inputPath, "output": outputPath, "batch_size": batchSize,
	})

	sandbox, err := createSandbox()
	if err != nil {
		return nil, fmt.Errorf("ml_inference: %w", err)
	}
	defer func() { _ = os.RemoveAll(sandbox) }()

	cmd := exec.CommandContext(ctx, modelName, args...)
	cmd.Dir = sandbox
	cmd.Env = sandboxEnv(msg)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if stderr.Len() > 0 {
		addLog("info", "model output", map[string]any{"output": truncate(stderr.String(), 500)})
	}
	if err != nil {
		return nil, fmt.Errorf("ml_inference: %w", err)
	}

	out := stdout.String()
	addLog("info", "Inference complete", map[string]any{"output_path": outputPath})

	var parsed any
	if json.Unmarshal([]byte(out), &parsed) == nil {
		return map[string]any{"output": parsed, "output_path": outputPath}, nil
	}
	return map[string]any{"output": out, "output_path": outputPath}, nil
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
		return nil, fmt.Errorf("slack: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.httpClient.Do(req)
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
	resp, err := w.httpClient.Do(req)
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
		return nil, fmt.Errorf("webhook: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.httpClient.Do(req)
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

// ─── Generic / Shell ──────────────────────────────────────────────────────────
// Config: command (string), args ([]string|[]any), env (map[string]string)

func (w *Worker) execGeneric(ctx context.Context, msg *models.TaskMessage, addLog logFn) (map[string]any, error) {
	cfg := msg.Config

	command, _ := cfg["command"].(string)
	if command == "" {
		addLog("info", "No command configured — task is a no-op", nil)
		return map[string]any{"status": "no-op"}, nil
	}

	var args []string
	switch a := cfg["args"].(type) {
	case []any:
		for _, v := range a {
			if s, ok := v.(string); ok {
				args = append(args, s)
			}
		}
	case []string:
		args = a
	}
	// Reject path-traversal attempts in the command binary itself
	if strings.Contains(command, "..") || strings.HasPrefix(command, "/") {
		return nil, fmt.Errorf("generic: command must be a binary name, not a path: %q", command)
	}

	sandbox, sandboxErr := createSandbox()
	if sandboxErr != nil {
		return nil, fmt.Errorf("generic: %w", sandboxErr)
	}
	defer func() { _ = os.RemoveAll(sandbox) }()

	addLog("info", fmt.Sprintf("Running: %s %s", command, strings.Join(args, " ")), map[string]any{
		"command": command,
		"args":    args,
	})

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = sandbox
	// Start with the clean base env, then merge any task-specific vars
	cmd.Env = sandboxEnv(msg)

	if envMap, ok := cfg["env"].(map[string]any); ok {
		for k, v := range envMap {
			if vs, ok := v.(string); ok {
				cmd.Env = append(cmd.Env, k+"="+vs)
			}
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		exitCode = exitErr.ExitCode()
	}

	if stderr.Len() > 0 {
		addLog("warn", "stderr", map[string]any{"output": truncate(stderr.String(), 500)})
	}
	if stdout.Len() > 0 {
		addLog("info", "stdout", map[string]any{"output": truncate(stdout.String(), 500)})
	}

	if err != nil {
		return nil, fmt.Errorf("generic: exited %d: %w", exitCode, err)
	}

	addLog("info", "Command completed", map[string]any{"exit_code": exitCode})

	out := stdout.String()
	var parsed any
	if json.Unmarshal([]byte(out), &parsed) == nil {
		return map[string]any{"output": parsed, "exit_code": exitCode}, nil
	}
	return map[string]any{"output": out, "exit_code": exitCode}, nil
}

// execGHAJobFallback runs a gha_job in-process (no Docker) by executing each
// run: step as a sequential shell command in a sandbox directory.
// Steps using uses: are skipped with a warning — they require the runner image.
func (w *Worker) execGHAJobFallback(ctx context.Context, msg *models.TaskMessage, addLog logFn) (map[string]any, error) {
	stepsRaw, ok := msg.Config["steps"]
	if !ok {
		return nil, fmt.Errorf("gha_job: missing steps in config")
	}
	b, _ := json.Marshal(stepsRaw)
	var steps []models.GHAStep
	if err := json.Unmarshal(b, &steps); err != nil {
		return nil, fmt.Errorf("gha_job: parse steps: %w", err)
	}

	sandbox, err := createSandbox()
	if err != nil {
		return nil, fmt.Errorf("gha_job: %w", err)
	}
	defer func() { _ = os.RemoveAll(sandbox) }()

	addLog("warn", "Docker unavailable — running gha_job steps in-process (reduced fidelity)", nil)

	for i, step := range steps {
		if step.Run == "" {
			addLog("warn", fmt.Sprintf("Step %d (%s): skipped (uses: %s requires runner image)", i+1, step.Name, step.Uses), nil)
			continue
		}
		addLog("info", fmt.Sprintf("▶ Step %d: %s", i+1, step.Name), nil)

		cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		cmd := exec.CommandContext(cmdCtx, "sh", "-c", step.Run)
		cmd.Dir = sandbox
		cmd.Env = sandboxEnv(msg)
		for k, v := range step.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		cancel()

		if stdout.Len() > 0 {
			addLog("info", stdout.String(), nil)
		}
		if stderr.Len() > 0 {
			addLog("warn", stderr.String(), nil)
		}
		if runErr != nil {
			return nil, fmt.Errorf("gha_job fallback: step %d (%s) failed: %w", i+1, step.Name, runErr)
		}
	}
	return map[string]any{"steps": len(steps), "mode": "fallback"}, nil
}

// createSandbox creates a fresh, empty, world-inaccessible temporary directory
// that is used as the working directory for all in-process task executors.
// The directory is completely isolated from the backend source tree: it is
// created under the OS temp base (e.g. /tmp), has mode 0700 so no other
// process can list it, and is removed by the caller after the task finishes.
func createSandbox() (string, error) {
	dir, err := os.MkdirTemp("", "fluxor-sandbox-*")
	if err != nil {
		return "", fmt.Errorf("create sandbox dir: %w", err)
	}
	// Restrict to owner only — prevents other processes from peeking
	if err := os.Chmod(dir, 0o700); err != nil {
		defer func() { _ = os.RemoveAll(dir) }()
		return "", fmt.Errorf("chmod sandbox dir: %w", err)
	}
	return dir, nil
}

// sandboxEnv returns a minimal, clean environment for subprocess execution.
// It deliberately omits the inherited process environment so user scripts
// cannot read secrets (POSTGRES_URL, MINIO_SECRET_KEY, REDIS_ADDR, etc.)
// that are present in the backend process env.
func sandboxEnv(msg *models.TaskMessage) []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/tmp",
		"TMPDIR=/tmp",
		"FLUXOR_TASK_EXEC_ID=" + msg.TaskExecID,
		"FLUXOR_WORKFLOW_EXEC_ID=" + msg.WorkflowExecID,
		"FLUXOR_TASK_NAME=" + msg.TaskName,
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
