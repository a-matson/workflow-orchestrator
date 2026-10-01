package api

import "net/http"

type clientErrorReport struct {
	Message string `json:"message"`
	Stack   string `json:"stack"`
	Source  string `json:"source"`
	Path    string `json:"path"`
}

// ReportClientError logs an error the UI caught but could not handle, so a
// broken page shows up in the backend's logs. The UI rate-limits its reports;
// the fields are capped again here because the client is not trusted.
// POST /api/client-errors
func (h *Handler) ReportClientError(w http.ResponseWriter, r *http.Request) {
	var rep clientErrorReport
	if !decodeJSON(w, r, &rep) {
		return
	}
	ev := logFrom(r).Warn().
		Str("client_source", truncate(rep.Source, 20)).
		Str("client_path", truncate(rep.Path, 200)).
		Str("client_stack", truncate(rep.Stack, 4000))
	if p := PrincipalFrom(r.Context()); p != nil {
		ev = ev.Str("actor", p.Name)
	}
	ev.Msg("client error: " + truncate(rep.Message, 500))
	w.WriteHeader(http.StatusNoContent)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
