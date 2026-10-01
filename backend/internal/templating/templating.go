// Package templating renders the strings in a task's config with text/template.
// Templates see the run's trigger payload and the outputs of the task's
// direct dependencies:
//
//	{{ .payload.region }}
//	{{ .tasks.extract.output.rows }}
//
// A missing key is an error, never an empty string, so a typo fails the task
// instead of running it with a blank value.
package templating

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

// Data builds what templates see from the trigger payload and the raw JSON
// outputs of the task's dependencies, keyed by task definition id. An output
// that is not JSON is exposed as its text.
func Data(payload map[string]any, outputs map[string]json.RawMessage) map[string]any {
	tasks := make(map[string]any, len(outputs))
	for id, raw := range outputs {
		var v any
		if len(raw) > 0 && json.Unmarshal(raw, &v) != nil {
			v = string(raw)
		}
		tasks[id] = map[string]any{"output": v}
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return map[string]any{"payload": payload, "tasks": tasks}
}

// HasTemplates reports whether any string in v contains a template action.
func HasTemplates(v any) bool {
	found := false
	// The callback never fails, so neither does the walk.
	_, _ = walk(v, func(s string) (string, error) {
		found = found || strings.Contains(s, "{{")
		return s, nil
	})
	return found
}

// SecretFunc is the template function that resolves a secret by name. Only
// the worker supplies it; elsewhere a template that calls it fails to render.
const SecretFunc = "secret"

// knownFuncs lets Check accept templates that call functions only the worker
// provides.
var knownFuncs = template.FuncMap{SecretFunc: func(string) (string, error) { return "", nil }}

// Check parses every template in v, for validation when a workflow is saved.
func Check(v any) error {
	_, err := walk(v, func(s string) (string, error) {
		_, err := parse(s, knownFuncs)
		return s, err
	})
	return err
}

// Render returns a copy of v with every template string executed against
// data, with funcs (which may be nil) available to the templates.
func Render(v any, data map[string]any, funcs template.FuncMap) (any, error) {
	return walk(v, func(s string) (string, error) {
		t, err := parse(s, funcs)
		if err != nil || t == nil {
			return s, err
		}
		var b strings.Builder
		if err := t.Execute(&b, data); err != nil {
			return "", err
		}
		return b.String(), nil
	})
}

// parse returns nil for a string without template actions, which is left as is.
func parse(s string, funcs template.FuncMap) (*template.Template, error) {
	if !strings.Contains(s, "{{") {
		return nil, nil
	}
	t, err := template.New("config").Option("missingkey=error").Funcs(funcs).Parse(s)
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", s, err)
	}
	return t, nil
}

// walk applies f to every string in v, copying maps and slices.
func walk(v any, f func(string) (string, error)) (any, error) {
	switch x := v.(type) {
	case string:
		return f(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			r, err := walk(e, f)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := walk(e, f)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	default:
		return v, nil
	}
}
