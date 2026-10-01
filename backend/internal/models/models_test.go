package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTaskResultAttempt_SurvivesJSON(t *testing.T) {
	zero := 0
	cases := []struct {
		name string
		in   TaskResult
		want int
	}{
		// Attempt 0 must not be omitted, or it would read back as unknown.
		{"attempt 0", TaskResult{RetryCount: &zero}, 0},
		{"predates the field", TaskResult{}, -1},
	}
	for _, tc := range cases {
		b, err := json.Marshal(tc.in)
		if err != nil {
			t.Fatalf("%s: marshal: %v", tc.name, err)
		}
		var out TaskResult
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("%s: unmarshal: %v", tc.name, err)
		}
		if got := out.Attempt(); got != tc.want {
			t.Errorf("%s: Attempt() after JSON round trip = %d, want %d (%s)", tc.name, got, tc.want, b)
		}
	}
}

// Durations accept a Go duration string as well as integer nanoseconds, so
// hand-written definitions can say "30s"; responses keep sending integers.
func TestDefinitionDurations_AcceptStrings(t *testing.T) {
	var def TaskDefinition
	in := `{"id":"a","timeout":"30s","retry_policy":{"max_retries":2,"initial_delay":"1.5s","max_delay":300000000000}}`
	if err := json.Unmarshal([]byte(in), &def); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if def.ID != "a" || def.Timeout != 30*time.Second {
		t.Errorf("id %q timeout %s, want a 30s", def.ID, def.Timeout)
	}
	if p := def.RetryPolicy; p == nil || p.MaxRetries != 2 || p.InitialDelay != 1500*time.Millisecond || p.MaxDelay != 5*time.Minute {
		t.Errorf("retry policy = %+v, want 2 retries, 1.5s, 5m", p)
	}

	out, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"timeout":30000000000`) {
		t.Errorf("marshal = %s, want timeout as integer nanoseconds", out)
	}

	// A field absent from the input keeps its value, as with plain encoding/json.
	kept := TaskDefinition{Timeout: time.Minute}
	if err := json.Unmarshal([]byte(`{"id":"b"}`), &kept); err != nil || kept.Timeout != time.Minute {
		t.Errorf("absent timeout = %s (err %v), want 1m kept", kept.Timeout, err)
	}

	for _, bad := range []string{`{"timeout":"soon"}`, `{"retry_policy":{"initial_delay":true}}`} {
		if err := json.Unmarshal([]byte(bad), &TaskDefinition{}); err == nil {
			t.Errorf("unmarshal %s: want an error", bad)
		}
	}
}
