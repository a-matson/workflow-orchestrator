package worker

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/workflow-platform/backend/internal/models"
	"github.com/workflow-platform/backend/internal/persistence"
)

// TaskNotifier is implemented by the orchestrator to receive worker lifecycle events.
// Using an interface avoids a circular import between worker and orchestrator packages.
type TaskNotifier interface {
	MarkTaskRunning(ctx context.Context, taskExecID, workerID string) error
}

// Worker pulls tasks from Redis, executes them using the config provided by the
// frontend, and publishes results back. All execution is driven by msg.Config
// which flows directly from the task definition saved in DB.
type Worker struct {
	id          string
	redis       *persistence.RedisClient
	notifier    TaskNotifier // optional: notifies orchestrator when a task starts
	concurrency int
	semaphore   chan struct{}
	httpClient  *http.Client
}

// Pool manages a set of concurrent workers.
type Pool struct {
	workers []*Worker
	redis   *persistence.RedisClient
}

func NewPool(redis *persistence.RedisClient, workerCount, concurrencyPerWorker int) *Pool {
	return NewPoolWithNotifier(redis, workerCount, concurrencyPerWorker, nil)
}

func NewPoolWithNotifier(redis *persistence.RedisClient, workerCount, concurrencyPerWorker int, notifier TaskNotifier) *Pool {
	workers := make([]*Worker, workerCount)
	for i := 0; i < workerCount; i++ {
		workers[i] = &Worker{
			id:          fmt.Sprintf("worker-%s", uuid.New().String()[:8]),
			redis:       redis,
			notifier:    notifier,
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

			go func(taskMsg *models.TaskMessage) {
				defer func() { <-w.semaphore }()
				w.executeTask(ctx, taskMsg)
			}(msg)
		}
	}
}

// executeTask acquires a distributed lock for idempotency, dispatches to the
// correct executor, and publishes the result back to Redis.
func (w *Worker) executeTask(ctx context.Context, msg *models.TaskMessage) {
	startedAt := time.Now()

	log.Info().
		Str("worker_id", w.id).
		Str("task_exec_id", msg.TaskExecID).
		Str("task_type", msg.TaskType).
		Str("task_name", msg.TaskName).
		Int("retry", msg.RetryCount).
		Msg("executing task")

	var logs []models.LogEntry
	addLog := func(level, message string, fields map[string]any) {
		logs = append(logs, models.LogEntry{
			Timestamp: time.Now(),
			Level:     level,
			Message:   message,
			Fields:    fields,
		})
	}

	// Create execution context with timeout
	taskCtx := ctx
	if msg.Timeout > 0 {
		var cancel context.CancelFunc
		taskCtx, cancel = context.WithTimeout(ctx, msg.Timeout)
		defer cancel()
	}

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
	})

	// Notify the orchestrator that this task is now running.
	// This moves it from Queued→Running in the in-memory state so that
	// dispatchReadyTasks correctly accounts for in-flight tasks.
	if w.notifier != nil {
		if err := w.notifier.MarkTaskRunning(ctx, msg.TaskExecID, w.id); err != nil {
			log.Warn().Err(err).Str("task_exec_id", msg.TaskExecID).Msg("failed to mark task running")
		}
	}

	output, execErr := w.dispatch(taskCtx, msg, addLog)

	completedAt := time.Now()

	result := &models.TaskResult{
		TaskExecID:     msg.TaskExecID,
		WorkflowExecID: msg.WorkflowExecID,
		WorkerID:       w.id,
		StartedAt:      startedAt,
		CompletedAt:    completedAt,
	}

	if execErr != nil {
		result.Success = false
		result.Error = execErr.Error()
		addLog("error", "Task failed", map[string]any{
			"error":       execErr.Error(),
			"duration_ms": completedAt.Sub(startedAt).Milliseconds(),
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

func (w *Worker) dispatch(ctx context.Context, msg *models.TaskMessage, addLog logFn) (map[string]any, error) {
	switch msg.TaskType {
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

// ─── HTTP Request ─────────────────────────────────────────────────────────────
// Config: url (string), method (string), headers (map), body (string|object),
//         timeout_ms (number)

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

	db, err := sql.Open(driver, connStr)
	if err != nil {
		return nil, fmt.Errorf("database_query: open: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Msg("failed to close database connection")
		}
	}()
	db.SetConnMaxLifetime(30 * time.Second)
	db.SetMaxOpenConns(2)

	addLog("info", "Executing query", map[string]any{"query": truncate(query, 200)})

	qCtx, qCancel := context.WithTimeout(ctx, 60*time.Second)
	defer qCancel()

	rows, err := db.QueryContext(qCtx, query)
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

	addLog("info", "Running transform", map[string]any{
		"input_format":  cfg["input_format"],
		"output_format": cfg["output_format"],
		"script":        truncate(script, 200),
	})

	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "sh", "-c", script)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
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

	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, modelName, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
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

	addLog("info", fmt.Sprintf("Running: %s %s", command, strings.Join(args, " ")), map[string]any{
		"command": command,
		"args":    args,
	})

	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, command, args...)

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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
