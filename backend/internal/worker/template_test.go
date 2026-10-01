package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/a-matson/workflow-orchestrator/backend/internal/egress"
	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
	"github.com/a-matson/workflow-orchestrator/backend/internal/templating"
)

// F4: the worker renders config templates after the pickup, so the task runs
// with the rendered values and a template error fails it.
func TestPickUpAndDispatch_RendersTemplates(t *testing.T) {
	var gotPath string
	srv, hits := countingServer(t, func(_ http.ResponseWriter, r *http.Request) { gotPath = r.URL.Path })
	g, err := egress.New(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{notifier: pickupNotifier{}, httpClient: g.HTTPClient(5 * time.Second)}
	noLog := func(string, string, map[string]any) {}
	ctx := context.Background()

	msg := &models.TaskMessage{
		TaskExecID: "t", TaskType: "http_request",
		Config:       map[string]any{"url": srv.URL + "/{{ .payload.region }}/{{ .tasks.a.output.n }}"},
		TemplateData: templating.Data(map[string]any{"region": "eu"}, map[string]json.RawMessage{"a": json.RawMessage(`{"n":3}`)}),
	}
	if _, _, ran, err := w.pickUpAndDispatch(ctx, ctx, msg, noLog, &redactor{}); !ran || err != nil {
		t.Fatalf("pickUpAndDispatch = ran %v, %v", ran, err)
	}
	if hits.Load() != 1 || gotPath != "/eu/3" {
		t.Errorf("request path %q (%d hits), want /eu/3", gotPath, hits.Load())
	}
	if !strings.Contains(msg.Config["url"].(string), "{{") {
		t.Error("rendering changed the message's own config")
	}

	bad := *msg
	bad.Config = map[string]any{"url": srv.URL + "/{{ .payload.regoin }}"}
	if _, _, ran, err := w.pickUpAndDispatch(ctx, ctx, &bad, noLog, &redactor{}); !ran || err == nil || !strings.Contains(err.Error(), "config template") {
		t.Errorf("missing key: ran %v err %v, want a config template failure", ran, err)
	}
	if hits.Load() != 1 {
		t.Errorf("a failed render still sent a request (%d hits)", hits.Load())
	}
}
