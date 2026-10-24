// Package server wraps an oas.Handler in the ogen server and renders transport-level errors.
package server

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ogen-go/ogen/ogenerrors"

	"orch-test/backend/internal/oas"
)

// New returns the root HTTP handler for h.
func New(h oas.Handler) (http.Handler, error) {
	return oas.NewServer(h,
		oas.WithErrorHandler(writeError),
		oas.WithNotFound(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, "no route for "+r.Method+" "+r.URL.Path)
		}),
		oas.WithMethodNotAllowed(func(w http.ResponseWriter, r *http.Request, allowed string) {
			w.Header().Set("Allow", allowed)
			writeJSON(w, http.StatusMethodNotAllowed, r.Method+" not allowed, use "+allowed)
		}),
	)
}

// writeError renders request decoding/validation errors in the spec's Error format.
func writeError(ctx context.Context, w http.ResponseWriter, r *http.Request, err error) {
	writeJSON(w, ogenerrors.ErrorCode(err), err.Error())
}

func writeJSON(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(oas.Error{Error: msg})
}
