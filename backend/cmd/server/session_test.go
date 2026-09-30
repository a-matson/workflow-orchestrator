package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestParseSessionSecret(t *testing.T) {
	good := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	if b, err := parseSessionSecret(good); err != nil || len(b) != 32 {
		t.Errorf("32-byte secret: %d bytes, %v", len(b), err)
	}
	short := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 31)))
	for name, raw := range map[string]string{"short": short, "not base64": "not*base64"} {
		_, err := parseSessionSecret(raw)
		if err == nil {
			t.Errorf("%s: no error", name)
		} else if strings.Contains(err.Error(), raw) {
			t.Errorf("%s: error quotes the secret: %v", name, err)
		}
	}
}
