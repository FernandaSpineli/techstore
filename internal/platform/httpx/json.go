// Package httpx holds the HTTP plumbing shared by every API module: JSON
// responses, the error envelope and middleware.
package httpx

import (
	"encoding/json"
	"net/http"
)

// WriteJSON writes v as a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		// Only reachable with a programming error (e.g. an unsupported type);
		// never send a half-written body.
		http.Error(w, `{"error":{"code":"INTERNAL","message":"Internal server error"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
