package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"orch-test/backend/internal/oas"
)

func TestTransportErrorsUseErrorSchema(t *testing.T) {
	h, err := New(oas.UnimplementedHandler{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		method string
		path   string
		status int
	}{
		{"unknown route", http.MethodGet, "/api/nope", http.StatusNotFound},
		{"wrong method", http.MethodPost, "/api/health", http.StatusMethodNotAllowed},
		{"unimplemented operation", http.MethodGet, "/api/health", http.StatusNotImplemented},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			var body oas.Error
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Error == "" {
				t.Fatalf("body is not an Error: %v", err)
			}
		})
	}
}
