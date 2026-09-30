package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/egress"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/persistence"
	"github.com/a-matson/workflow-orchestrator/backend/internal/storage"
)

// TaskNotifier is implemented by the orchestrator to receive worker lifecycle events.
// Using an interface avoids a circular import.
type TaskNotifier interface {
	MarkTaskRunning(ctx context.Context, taskExecID, workerID string, attempt int, timeout time.Duration) error
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
	guard       *egress.Guard
	limits      Limits
	// sendmail is a seam for tests; nil runs the host binary.
	sendmail   sendmailFunc
	httpClient *http.Client // guarded: every task-initiated request goes through the egress guard
	running    *runningTasks
}

// Pool manages a set of concurrent workers.
type Pool struct {
	workers []*Worker
	redis   *persistence.RedisClient
	running *runningTasks
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

	limits, err := LimitsFromEnv()
	if err != nil {
		return nil, err
	}
	if executor != nil {
		executor.limits = limits
	}

	httpClient := guard.HTTPClient(60 * time.Second)
	running := newRunningTasks()
	workers := make([]*Worker, workerCount)
	for i := 0; i < workerCount; i++ {
		workers[i] = &Worker{
			id:          fmt.Sprintf("worker-%s", uuid.New().String()[:8]),
			redis:       redis,
			notifier:    notifier,
			executor:    executor,
			concurrency: concurrencyPerWorker,
			semaphore:   make(chan struct{}, concurrencyPerWorker),
			guard:       guard,
			limits:      limits,
			httpClient:  httpClient,
			running:     running,
		}
	}
	return &Pool{workers: workers, redis: redis, running: running}, nil
}

// Start runs the workers until ctx is done, then stops taking tasks and waits
// up to grace for the running ones to finish and publish their results. Runs
// still going after grace are stopped without a result: their rows stay
// running, and the next start's recovery re-queues them without spending a
// retry, since the shutdown was not the task's failure.
func (p *Pool) Start(ctx context.Context, grace time.Duration) {
	taskCtx, stopTasks := context.WithCancelCause(context.WithoutCancel(ctx))
	defer stopTasks(nil)
	var loops, tasks sync.WaitGroup
	for _, w := range p.workers {
		loops.Go(func() { w.run(ctx, taskCtx, &tasks) })
	}
	log.Info().Int("worker_count", len(p.workers)).Msg("worker pool started")
	loops.Wait()

	drained := make(chan struct{})
	go func() {
		tasks.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		log.Info().Msg("worker pool drained")
	case <-time.After(grace):
		log.Warn().Dur("grace", grace).Msg("worker pool: stopping tasks still running after the shutdown grace period")
		stopTasks(errShutdown)
		<-drained
	}
}

// run takes tasks until ctx is done. Each task runs on taskCtx, which
// outlives ctx so a shutdown can drain it, and is counted in tasks.
func (w *Worker) run(ctx, taskCtx context.Context, tasks *sync.WaitGroup) {
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

			tasks.Add(1)
			go func(ctx context.Context, taskMsg *models.TaskMessage) {
				defer tasks.Done()
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
			}(taskCtx, msg)
		}
	}
}

// executeTask marks the task running, runs it through dispatch and publishes
// the result. The pickup transition is the only dedupe: it admits one message
// per attempt.
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

	logs := newBoundedLogs(w.limits.logs())
	addLog := func(level, message string, fields map[string]any) {
		entry := models.LogEntry{
			Timestamp: time.Now(),
			Level:     level,
			Attempt:   msg.RetryCount,
			Message:   message,
			Fields:    fields,
		}

		// Broadcast immediately so the UI streams output in real time
		for _, e := range logs.add(entry) {
			if w.notifier != nil {
				w.notifier.StreamLog(msg.WorkflowExecID, msg.TaskExecID, msg.TaskName, e)
			}
		}
	}

	// Tracked before the pickup: once the row is running, a cancel must find
	// this run, and a cancel of a row not yet picked up fails the pickup instead.
	runCtx, release := w.running.track(ctx, msg.TaskExecID)
	defer release()
	taskCtx := runCtx
	if msg.Timeout > 0 {
		var cancel context.CancelFunc
		taskCtx, cancel = context.WithTimeout(runCtx, msg.Timeout)
		defer cancel()
	}

	output, artifactsOut, ran, execErr := w.pickUpAndDispatch(ctx, taskCtx, msg, addLog)
	if !ran {
		if errors.Is(execErr, persistence.ErrConflict) {
			taskLogger.Warn().Err(execErr).Msg("dropping task message: its row is not queued at this attempt")
		} else {
			taskLogger.Error().Err(execErr).Msg("dropping task message: its pickup could not be recorded")
		}
		return
	}
	// The cancel already closed the row; a failure result would only be
	// dropped by the status guard.
	if errors.Is(context.Cause(runCtx), errTaskCancelled) {
		taskLogger.Info().Msg("task cancelled; not publishing its result")
		return
	}
	if errors.Is(context.Cause(runCtx), errShutdown) {
		taskLogger.Warn().Msg("task stopped by shutdown; recovery re-queues it on the next start")
		return
	}

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
	result.Logs = logs.entries

	// Detached: the drain's grace can end the task context between the run
	// finishing and this publish, and the result would be lost.
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := w.redis.PublishResult(pubCtx, result); err != nil {
		log.Error().Err(err).Str("task_exec_id", msg.TaskExecID).Msg("failed to publish result")
	}
}

type logFn func(level, message string, fields map[string]any)

// pickUpAndDispatch records the pickup of msg, then runs it through dispatch.
// ran is false, with the pickup error, when the pickup was not recorded. A
// conflict means the message is a duplicate, stale, or from a rolled-back
// dispatch. Any other error leaves the row's state unknown (the write may
// have committed), so running could duplicate another worker's run; the
// message is dropped instead. A row whose pickup did commit then stays
// running until Orchestrator.ReapTimedOut fails it past its timeout.
func (w *Worker) pickUpAndDispatch(ctx, taskCtx context.Context, msg *models.TaskMessage, addLog logFn) (map[string]any, []models.ResolvedArtifact, bool, error) {
	// Before the first log line, so a dropped message streams nothing.
	if w.notifier != nil {
		if err := w.notifier.MarkTaskRunning(ctx, msg.TaskExecID, w.id, msg.RetryCount, msg.Timeout); err != nil {
			return nil, nil, false, err
		}
	}

	addLog("info", "Task execution started", map[string]any{
		"worker_id": w.id,
		"task_type": msg.TaskType,
		"task_name": msg.TaskName,
		"retry":     msg.RetryCount,
		"isolated":  w.usesContainer(msg),
	})

	out, arts, err := w.dispatch(taskCtx, msg, addLog)
	return out, arts, true, err
}

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

	addLog("info", fmt.Sprintf("→ %s %s", method, redactURL(rawURL)), map[string]any{
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

	respBody, truncated, err := readCapped(resp.Body, w.limits.output())
	if err != nil {
		return nil, fmt.Errorf("http_request: read response: %w", withoutURL(err))
	}

	addLog("info", fmt.Sprintf("← %d %s", resp.StatusCode, http.StatusText(resp.StatusCode)), map[string]any{
		"status":         resp.StatusCode,
		"response_bytes": len(respBody),
		"truncated":      truncated,
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
		"truncated":      truncated,
	}, nil
}

// checkDBDestinations refuses a DSN unless every host it can connect to,
// fallbacks included, passes the egress guard.
func (w *Worker) checkDBDestinations(ctx context.Context, connStr string) error {
	if w.guard == nil {
		return fmt.Errorf("database_query: no egress guard configured")
	}
	// pgconn's parse error redacts the password, but the detail is dropped anyway.
	cfg, err := pgconn.ParseConfig(connStr)
	if err != nil {
		return fmt.Errorf("database_query: invalid connection_string")
	}
	targets := []struct {
		host string
		port uint16
	}{{cfg.Host, cfg.Port}}
	for _, fb := range cfg.Fallbacks {
		targets = append(targets, struct {
			host string
			port uint16
		}{fb.Host, fb.Port})
	}
	for _, t := range targets {
		// Egress covers TCP only; a socket path would reach local services unchecked.
		if strings.HasPrefix(t.host, "/") || strings.HasPrefix(t.host, "@") {
			return fmt.Errorf("database_query: %w: unix socket hosts are not allowed", egress.ErrEgressDenied)
		}
		if err := w.guard.CheckHostPort(ctx, t.host, t.port); err != nil {
			return fmt.Errorf("database_query: %w", err)
		}
	}
	return nil
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

	// The DSN carries the password, so no error below may include it.
	if err := w.checkDBDestinations(ctx, connStr); err != nil {
		return nil, err
	}
	cfgPG, err := pgx.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("database_query: invalid connection_string")
	}
	// Second line of defence after the pre-flight: DNS can rebind between the
	// check and the connection, so every dial is guarded; fallbacks share Config.DialFunc.
	cfgPG.DialFunc = w.guard.DialContext
	// pgx would resolve names itself and dial bare IPs, hiding the name from the
	// guard, so an allowlisted host:port could never match. Passing the name
	// through lets the guard's dialer resolve it and apply both rules.
	cfgPG.LookupFunc = func(_ context.Context, host string) ([]string, error) { return []string{host}, nil }

	addLog("info", "Connecting (postgres)", nil)

	// Opened per task rather than cached by DSN: a cache would keep credentials
	// in memory and pool connections across tenants' tasks.
	db := stdlib.OpenDB(*cfgPG)
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Msg("failed to close database")
		}
	}()
	db.SetMaxOpenConns(1)

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
	truncated := false
	for rows.Next() {
		if len(results) >= maxRows {
			truncated = true
			addLog("warn", fmt.Sprintf("max_rows limit (%d) reached; more rows exist", maxRows), nil)
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
	return map[string]any{"rows": results, "columns": cols, "row_count": len(results), "truncated": truncated}, nil
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

	addLog("info", "Sending "+notifyType+" to "+describeChannel(notifyType, channel), nil)

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
	raw := remoteBody(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("slack: %d %s", resp.StatusCode, raw)
	}
	addLog("info", "Slack delivered", nil)
	return map[string]any{"delivered": true}, nil
}

type sendmailFunc func(ctx context.Context, args []string, stdin []byte) error

// notifyEmail validates the recipients itself and hands them to sendmail as
// argv, never as headers: with `sendmail -t` the To/Cc/Bcc headers choose the
// recipients, so any header injected through user text would add recipients.
func (w *Worker) notifyEmail(ctx context.Context, to, message string, addLog logFn) (map[string]any, error) {
	// net/mail tolerates folded lines, so CR and LF are rejected up front.
	if strings.ContainsAny(to, "\r\n") {
		return nil, fmt.Errorf("email: recipient must not contain line breaks")
	}
	addrs, err := mail.ParseAddressList(to)
	if err != nil {
		return nil, fmt.Errorf("email: invalid recipient: %w", err)
	}
	header := make([]string, len(addrs))
	args := []string{"--"}
	for i, a := range addrs {
		header[i] = a.String()
		args = append(args, a.Address)
	}
	if max := w.limits.output(); int64(len(message)) > max {
		message = message[:max] + truncationLine
	}
	msg := "To: " + strings.Join(header, ", ") + "\nSubject: Fluxor Notification\n\n" + message + "\n"

	run := w.sendmail
	if run == nil {
		run = runSendmail
	}
	if err := run(ctx, args, []byte(msg)); err != nil {
		return nil, err
	}
	addLog("info", "Email sent", map[string]any{"to": to})
	return map[string]any{"delivered": true, "to": to}, nil
}

func runSendmail(ctx context.Context, args []string, stdin []byte) error {
	bin, err := exec.LookPath("sendmail")
	if err != nil {
		return fmt.Errorf("email notifications need a sendmail binary or SMTP config")
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// Not user code: the argv is fixed except for validated addresses placed
	// after "--", and the body only reaches stdin, so this stays outside the
	// container-only rule.
	cmd := exec.CommandContext(cmdCtx, bin, args...) // #nosec G204 -- see above: fixed binary, validated argv
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		out := stderr.String()
		if len(out) > maxErrorBodyBytes {
			out = out[:maxErrorBodyBytes]
		}
		return fmt.Errorf("email: sendmail: %w — %s", err, out)
	}
	return nil
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
	raw := remoteBody(resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return nil, fmt.Errorf("pagerduty: %d %s", resp.StatusCode, raw)
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
	raw := remoteBody(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("webhook: %d %s", resp.StatusCode, truncate(raw, 200))
	}
	addLog("info", "Webhook delivered", map[string]any{"status": resp.StatusCode})
	return map[string]any{"delivered": true, "status": resp.StatusCode}, nil
}

// redactURL keeps scheme, host and path. Userinfo and the query are where
// credentials live, and an unparsable URL is dropped whole rather than guessed at.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<unparsable url>"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

// describeChannel names a notification target without its secret: webhook
// tokens (Slack, Discord) live in the path, so only the host is kept, and a
// PagerDuty channel is the routing key itself.
func describeChannel(notifyType, channel string) string {
	switch notifyType {
	case "email":
		// Logged before notifyEmail validates it, so control characters are
		// stripped here rather than trusted to be absent.
		return strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, channel)
	case "pagerduty":
		return "PagerDuty"
	}
	u, err := url.Parse(channel)
	if err != nil || u.Host == "" {
		return "<unparsable url>"
	}
	return u.Host
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
