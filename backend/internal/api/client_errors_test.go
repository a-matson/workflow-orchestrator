package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestReportClientError_LogsCappedFields(t *testing.T) {
	var logs bytes.Buffer
	body, err := json.Marshal(clientErrorReport{
		Message: "boom", Stack: strings.Repeat("s", 10_000), Source: "vue", Path: "/builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(zerolog.New(&logs).WithContext(t.Context()),
		http.MethodPost, "/api/client-errors", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	(&Handler{}).ReportClientError(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("log line %q: %v", logs.String(), err)
	}
	if entry["message"] != "client error: boom" || entry["client_path"] != "/builder" || entry["client_source"] != "vue" {
		t.Errorf("log entry = %v", entry)
	}
	if n := len(entry["client_stack"].(string)); n != 4000 {
		t.Errorf("logged stack is %d bytes, want 4000", n)
	}
}
