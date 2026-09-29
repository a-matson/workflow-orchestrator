package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParsePagination(t *testing.T) {
	tests := []struct {
		name          string
		query         string
		limit, offset int
		wantErr       bool
	}{
		{"defaults", "", 50, 0, false},
		{"explicit", "limit=10&offset=5", 10, 5, false},
		{"capped", "limit=1000", 200, 0, false},
		{"negative limit", "limit=-1", 0, 0, true},
		{"negative offset", "offset=-1", 0, 0, true},
		{"non-numeric limit", "limit=abc", 0, 0, true},
		{"non-numeric offset", "offset=abc", 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequestWithContext(t.Context(), "GET", "/x?"+tt.query, nil)
			limit, offset, err := parsePagination(r, 50)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if limit != tt.limit || offset != tt.offset {
				t.Errorf("got (%d, %d), want (%d, %d)", limit, offset, tt.limit, tt.offset)
			}
		})
	}
}

func TestListWorkflows_RejectsInvalidPagination(t *testing.T) {
	// A nil store is safe: the handler must reject before touching it.
	mux := NewHandler(nil, nil, nil, nil, nil).Routes()
	r := httptest.NewRequestWithContext(t.Context(), "GET", "/api/workflows?limit=-1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
