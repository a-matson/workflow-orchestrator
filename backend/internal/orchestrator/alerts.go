package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

const alertTimeout = 10 * time.Second

// AlertPayload is the JSON body POSTed to a workflow's alert URL. The README
// documents it for receivers, so changing a field is a breaking change.
type AlertPayload struct {
	Event        string                `json:"event"`
	WorkflowID   string                `json:"workflow_id"`
	WorkflowName string                `json:"workflow_name"`
	ExecutionID  string                `json:"execution_id"`
	Status       models.WorkflowStatus `json:"status"`
	StartedAt    *time.Time            `json:"started_at,omitempty"`
	CompletedAt  *time.Time            `json:"completed_at,omitempty"`
	FailedTasks  []string              `json:"failed_tasks,omitempty"`
}

// SetAlertClient makes finished runs post their definition's alerts with c.
// Without it no alert is sent. Alert URLs are user-supplied, so c should dial
// through the egress guard.
func (o *Orchestrator) SetAlertClient(c *http.Client) {
	o.alertClient = c
}

// alert posts final's outcome to the target alerts names for its status. It
// returns at once: a slow receiver must not hold up completion. Delivery is
// one attempt, so a receiver that is down misses the alert; a durable outbox
// with retries is the upgrade if alerts must not be lost.
func (o *Orchestrator) alert(ctx context.Context, alerts *models.WorkflowAlerts, final *models.WorkflowExecution) {
	if o.alertClient == nil || alerts == nil {
		return
	}
	target, event := alerts.OnFailure, models.WSEventWorkflowFailed
	if final.Status == models.WorkflowStatusCompleted {
		target, event = alerts.OnSuccess, models.WSEventWorkflowCompleted
	}
	if target == nil {
		return
	}
	payload := AlertPayload{
		Event:        event,
		WorkflowID:   final.WorkflowID,
		WorkflowName: final.WorkflowName,
		ExecutionID:  final.ID,
		Status:       final.Status,
		StartedAt:    final.StartedAt,
		CompletedAt:  final.CompletedAt,
	}
	for _, t := range final.Tasks {
		if t.Status == models.TaskStatusFailed || t.Status == models.TaskStatusDeadLetter {
			payload.FailedTasks = append(payload.FailedTasks, t.TaskDefinitionID)
		}
	}
	// Detached: the alert outlives the result or request that finished the run.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), alertTimeout)
	go func() {
		defer cancel()
		if err := postAlert(ctx, o.alertClient, target.URL, payload); err != nil {
			log.Warn().Err(err).Str("exec_id", final.ID).Str("workflow_id", final.WorkflowID).Msg("workflow alert not delivered")
		}
	}()
}

// postAlert sends payload to rawURL. Its errors never contain the URL, which
// may carry a token (Slack-style webhook URLs do) and errors are logged.
func postAlert(ctx context.Context, c *http.Client, rawURL string, payload AlertPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encoding alert: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("building alert request: invalid URL")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return fmt.Errorf("posting alert: %w", err)
	}
	defer func() {
		// Nothing acts on a close error once the response has been read.
		_ = resp.Body.Close()
	}()
	// Drained so the connection can be reused; bounded, as the receiver is untrusted.
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)); err != nil {
		return fmt.Errorf("reading alert response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("alert receiver answered %d", resp.StatusCode)
	}
	return nil
}
