package worker

import (
	"context"
	"strings"
	"sync"
)

// SecretSource resolves a secret's value by name; secrets.Vault implements it.
type SecretSource interface {
	Secret(ctx context.Context, name string) (string, error)
}

// SetSecrets lets the pool's workers resolve {{ secret "name" }} in task config.
// Call it before Start.
func (p *Pool) SetSecrets(s SecretSource) {
	for _, w := range p.workers {
		w.secrets = s
	}
}

// redactor masks the secret values one run resolved, in the log lines, error
// and output that run reports.
type redactor struct {
	mu     sync.Mutex
	values []string
}

func (r *redactor) add(v string) {
	if v == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.values = append(r.values, v)
}

func (r *redactor) redact(s string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}

// redactValue masks strings inside v, copying maps and slices.
func (r *redactor) redactValue(v any) any {
	switch x := v.(type) {
	case string:
		return r.redact(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = r.redactValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.redactValue(e)
		}
		return out
	default:
		return v
	}
}
