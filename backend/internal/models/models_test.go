package models

import (
	"encoding/json"
	"testing"
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
