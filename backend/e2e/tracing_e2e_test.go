//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// jaegerURL is the e2e stack's Jaeger (make e2e enables the tracing profile
// and points the backend at it). Jaeger 2 serves only its v3 query API.
const jaegerURL = "http://localhost:16686"

// otlpSpan is the part of a span in Jaeger's OTLP JSON that the test reads.
type otlpSpan struct {
	TraceID    string `json:"traceId"`
	Name       string `json:"name"`
	Kind       int    `json:"kind"`
	Attributes []struct {
		Key   string `json:"key"`
		Value struct {
			StringValue string `json:"stringValue"`
		} `json:"value"`
	} `json:"attributes"`
}

const spanKindServer = 2

// O5 against the real stack: one trigger is one trace in Jaeger, from the API
// request through dispatch and the worker to result processing.
func TestE2E_TriggerIsOneTrace(t *testing.T) {
	var wf struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows", map[string]any{
		"name": "e2e-trace",
		"tasks": []map[string]any{{
			"id": "a", "name": "A", "type": "generic", "dependencies": []string{},
			"config":    map[string]any{"script": "echo traced"},
			"container": map[string]any{"image": "alpine:3.22"},
		}},
	}, http.StatusCreated, &wf)
	since := time.Now().Add(-time.Minute)
	var run struct{ ID string }
	do(t, http.MethodPost, baseURL+"/api/workflows/"+wf.ID+"/trigger", map[string]any{}, http.StatusAccepted, &run)

	need := []string{"start workflow", "dispatch task", "run task", "process result"}
	var got map[string]bool
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		spans := runTrace(t, since, wf.ID)
		got = map[string]bool{}
		server := false
		for _, s := range spans {
			got[s.Name] = true
			server = server || s.Kind == spanKindServer
		}
		complete := server
		for _, n := range need {
			complete = complete && got[n]
		}
		if complete {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("workflow %s: want one trace with an HTTP server span and %v, got span names %v", wf.ID, need, got)
}

// runTrace returns the spans of the trace whose "start workflow" span names
// workflowID. The v3 API ignores attribute filters, so it matches here.
func runTrace(t *testing.T, since time.Time, workflowID string) []otlpSpan {
	t.Helper()
	q := url.Values{
		"query.service_name":   {"fluxor-backend"},
		"query.start_time_min": {since.UTC().Format(time.RFC3339)},
		"query.start_time_max": {time.Now().Add(time.Minute).UTC().Format(time.RFC3339)},
		"query.num_traces":     {"100"},
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, jaegerURL+"/api/v3/traces?"+q.Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Logf("jaeger: %v", err)
		return nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Result struct {
			ResourceSpans []struct {
				ScopeSpans []struct {
					Spans []otlpSpan `json:"spans"`
				} `json:"scopeSpans"`
			} `json:"resourceSpans"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Logf("jaeger %d: %s", resp.StatusCode, raw)
		return nil
	}
	byTrace := map[string][]otlpSpan{}
	runTraceID := ""
	for _, rs := range body.Result.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				byTrace[s.TraceID] = append(byTrace[s.TraceID], s)
				for _, a := range s.Attributes {
					if s.Name == "start workflow" && a.Key == "workflow.id" && a.Value.StringValue == workflowID {
						runTraceID = s.TraceID
					}
				}
			}
		}
	}
	return byTrace[runTraceID]
}
