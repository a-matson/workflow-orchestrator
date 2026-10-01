package templating

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	data := Data(map[string]any{"region": "eu"}, map[string]json.RawMessage{
		"extract": json.RawMessage(`{"rows": 42}`),
		"plain":   json.RawMessage(`not json`),
	})
	cfg := map[string]any{
		"url":     "https://{{ .payload.region }}.example.com",
		"args":    []any{"--rows", "{{ .tasks.extract.output.rows }}"},
		"headers": map[string]any{"X-Note": "{{ .tasks.plain.output }}"},
		"plain":   "no template",
		"timeout": 30,
	}
	got, err := Render(cfg, data)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"url":     "https://eu.example.com",
		"args":    []any{"--rows", "42"},
		"headers": map[string]any{"X-Note": "not json"},
		"plain":   "no template",
		"timeout": 30,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Render =\n%v\nwant\n%v", got, want)
	}
	if cfg["url"] != "https://{{ .payload.region }}.example.com" {
		t.Error("Render changed its input")
	}
}

func TestRender_MissingKeyFails(t *testing.T) {
	_, err := Render(map[string]any{"q": "{{ .payload.regoin }}"}, Data(map[string]any{"region": "eu"}, nil))
	if err == nil || !strings.Contains(err.Error(), "regoin") {
		t.Errorf("Render with a missing key = %v, want an error naming it", err)
	}
}

func TestCheck_And_HasTemplates(t *testing.T) {
	if err := Check(map[string]any{"a": []any{"{{ .payload.x"}}); err == nil {
		t.Error("Check accepted an unterminated action")
	}
	if err := Check(map[string]any{"a": "{{ .payload.x }}", "b": 1}); err != nil {
		t.Errorf("Check rejected a valid template: %v", err)
	}
	if HasTemplates(map[string]any{"a": "plain", "b": []any{1, "x"}}) {
		t.Error("HasTemplates found a template in plain strings")
	}
	if !HasTemplates(map[string]any{"b": []any{"{{ .payload.x }}"}}) {
		t.Error("HasTemplates missed a nested template")
	}
}
