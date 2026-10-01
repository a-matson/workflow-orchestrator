// Package workflowyaml converts workflow definitions to and from YAML. The
// YAML uses the same field names as the JSON API, and durations may be
// written as strings ("30s").
package workflowyaml

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/a-matson/workflow-orchestrator/backend/internal/models"
)

// Parse decodes a workflow definition, native or a GitHub Actions workflow
// (see parseGitHubActions). Unknown fields are an error, so a misspelled key
// fails instead of being silently dropped. It does not validate the DAG;
// callers run the same validation as the JSON API.
func Parse(src []byte) (*models.WorkflowDefinition, error) {
	var doc any
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	m, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("yaml: the document must be a mapping of workflow fields")
	}
	if isGitHubActions(m) {
		return parseGitHubActions(src)
	}
	// Through JSON so the model's json tags, and its duration-string
	// decoding, apply unchanged.
	j, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	var def models.WorkflowDefinition
	if err := dec.Decode(&def); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	return &def, nil
}

// serverFields are set by the server, so an exported file imports as a new workflow.
var serverFields = []string{"id", "created_at", "updated_at", "next_run_at"}

// Render encodes def as YAML, with durations as strings and without the
// fields the server assigns.
func Render(def *models.WorkflowDefinition) ([]byte, error) {
	j, err := json.Marshal(def)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	for _, f := range serverFields {
		delete(doc, f)
	}
	humanizeDurations(doc["global_retry"], "initial_delay", "max_delay")
	if tasks, ok := doc["tasks"].([]any); ok {
		for _, t := range tasks {
			humanizeDurations(t, "timeout")
			if tm, ok := t.(map[string]any); ok {
				humanizeDurations(tm["retry_policy"], "initial_delay", "max_delay")
			}
		}
	}
	return yaml.Marshal(plainNumbers(doc))
}

// plainNumbers replaces json.Number, which yaml would quote as a string, with
// int64 or float64.
func plainNumbers(v any) any {
	switch x := v.(type) {
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i
		}
		if f, err := x.Float64(); err == nil {
			return f
		}
		return x.String()
	case map[string]any:
		for k, e := range x {
			x[k] = plainNumbers(e)
		}
	case []any:
		for i, e := range x {
			x[i] = plainNumbers(e)
		}
	}
	return v
}

// humanizeDurations rewrites integer-nanosecond fields of m as duration strings.
func humanizeDurations(m any, keys ...string) {
	obj, ok := m.(map[string]any)
	if !ok {
		return
	}
	for _, k := range keys {
		if n, ok := obj[k].(json.Number); ok {
			if ns, err := n.Int64(); err == nil {
				obj[k] = time.Duration(ns).String()
			}
		}
	}
}
