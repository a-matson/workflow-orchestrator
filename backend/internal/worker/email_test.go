package worker

import (
	"context"
	"strings"
	"testing"
)

type sendmailCall struct {
	args  []string
	stdin string
}

func emailWorker(calls *[]sendmailCall) *Worker {
	return &Worker{sendmail: func(_ context.Context, args []string, stdin []byte) error {
		*calls = append(*calls, sendmailCall{args, string(stdin)})
		return nil
	}}
}

func TestNotifyEmailRejectsHeaderInjection(t *testing.T) {
	for name, to := range map[string]string{
		"crlf":         "victim@example.com\r\nBcc: attacker@example.com",
		"lf only":      "victim@example.com\nBcc: attacker@example.com",
		"cr only":      "victim@example.com\rBcc: attacker@example.com",
		"display name": "\"Evil\r\nBcc: attacker@example.com\" <victim@example.com>",
		"not address":  "not an address",
	} {
		t.Run(name, func(t *testing.T) {
			var calls []sendmailCall
			_, err := emailWorker(&calls).notifyEmail(context.Background(), to, "hi", func(string, string, map[string]any) {})
			if err == nil {
				t.Fatal("expected an error for an unsafe recipient")
			}
			if len(calls) != 0 {
				t.Fatalf("sendmail ran %d time(s) for a rejected recipient", len(calls))
			}
		})
	}
}

func TestNotifyEmailWritesOneToHeader(t *testing.T) {
	var calls []sendmailCall
	_, err := emailWorker(&calls).notifyEmail(context.Background(), "Ann <ann@example.com>, bob@example.com", "hi", func(string, string, map[string]any) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("sendmail calls = %d, want 1", len(calls))
	}
	c := calls[0]
	header, _, _ := strings.Cut(c.stdin, "\n\n")
	if n := strings.Count(header, "To:"); n != 1 {
		t.Fatalf("To headers = %d, want 1:\n%s", n, header)
	}
	if strings.Contains(c.stdin, "Bcc") {
		t.Fatalf("unexpected Bcc in message:\n%s", c.stdin)
	}
	for _, a := range c.args {
		if a == "-t" {
			t.Fatalf("-t lets headers add recipients: %v", c.args)
		}
	}
	if got := strings.Join(c.args, " "); !strings.HasSuffix(got, "-- ann@example.com bob@example.com") {
		t.Fatalf("argv = %q, want recipients after --", got)
	}
}

func TestNotifyEmailBoundsBody(t *testing.T) {
	var calls []sendmailCall
	w := emailWorker(&calls)
	w.limits = Limits{OutputBytes: 100}
	if _, err := w.notifyEmail(context.Background(), "a@example.com", strings.Repeat("x", 10000), func(string, string, map[string]any) {}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("sendmail calls = %d, want 1", len(calls))
	}
	if len(calls[0].stdin) > 400 {
		t.Fatalf("body not bounded: %d bytes", len(calls[0].stdin))
	}
}
