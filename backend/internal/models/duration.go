package models

import (
	"encoding/json"
	"fmt"
	"time"
)

// The UnmarshalJSON methods below shadow their duration fields with this type
// and seed the shadows from the current values, so a field absent from the
// input keeps its value, as with plain encoding/json.

// durationJSON decodes a duration given either as integer nanoseconds, the
// API's wire format, or as a Go duration string such as "30s" or "1m30s".
type durationJSON time.Duration

func (d *durationJSON) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("duration %q: %w", s, err)
		}
		*d = durationJSON(v)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("duration must be integer nanoseconds or a string like \"30s\": %s", b)
	}
	*d = durationJSON(n)
	return nil
}

// UnmarshalJSON accepts duration strings for initial_delay and max_delay.
func (p *RetryPolicy) UnmarshalJSON(b []byte) error {
	type plain RetryPolicy
	aux := struct {
		*plain
		InitialDelay durationJSON `json:"initial_delay"`
		MaxDelay     durationJSON `json:"max_delay"`
	}{plain: (*plain)(p), InitialDelay: durationJSON(p.InitialDelay), MaxDelay: durationJSON(p.MaxDelay)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	p.InitialDelay, p.MaxDelay = time.Duration(aux.InitialDelay), time.Duration(aux.MaxDelay)
	return nil
}

// UnmarshalJSON accepts a duration string for timeout.
func (t *TaskDefinition) UnmarshalJSON(b []byte) error {
	type plain TaskDefinition
	aux := struct {
		*plain
		Timeout durationJSON `json:"timeout"`
	}{plain: (*plain)(t), Timeout: durationJSON(t.Timeout)}
	if err := json.Unmarshal(b, &aux); err != nil {
		return err
	}
	t.Timeout = time.Duration(aux.Timeout)
	return nil
}
