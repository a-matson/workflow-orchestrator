package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func sign(key, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(ts + "." + string(body)))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestValidWebhookSignature(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	body := []byte(`{"ref":"main"}`)
	good := sign("k3y", ts, body)
	stale := strconv.FormatInt(now.Add(-webhookWindow-time.Second).Unix(), 10)
	future := strconv.FormatInt(now.Add(webhookWindow+time.Second).Unix(), 10)

	tests := []struct {
		name, key, ts, sig string
		body               []byte
		want               bool
	}{
		{"valid", "k3y", ts, good, body, true},
		{"edge of window", "k3y", strconv.FormatInt(now.Add(-webhookWindow).Unix(), 10), sign("k3y", strconv.FormatInt(now.Add(-webhookWindow).Unix(), 10), body), body, true},
		{"stale", "k3y", stale, sign("k3y", stale, body), body, false},
		{"future", "k3y", future, sign("k3y", future, body), body, false},
		{"wrong key", "other", ts, good, body, false},
		{"tampered body", "k3y", ts, good, []byte(`{"ref":"evil"}`), false},
		{"timestamp not signed as sent", "k3y", ts + "0", good, body, false},
		{"no prefix", "k3y", ts, good[len("sha256="):], body, false},
		{"not hex", "k3y", ts, "sha256=zz", body, false},
		{"missing timestamp", "k3y", "", good, body, false},
		{"missing signature", "k3y", ts, "", body, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := validWebhookSignature(tc.key, tc.ts, tc.sig, tc.body, now); got != tc.want {
				t.Errorf("validWebhookSignature = %v, want %v", got, tc.want)
			}
		})
	}
}
