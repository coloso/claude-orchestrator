package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"orch-test/backend/internal/oas"
)

func TestHealth(t *testing.T) {
	down := func(context.Context) error { return errors.New("connection refused") }
	tests := []struct {
		name   string
		ping   func(context.Context) error
		status int // 0 = healthy
	}{
		{"no storage to check", nil, 0},
		{"storage up", func(context.Context) error { return nil }, 0},
		{"storage down", down, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := (&Handler{Ping: tt.ping}).GetHealth(context.Background())
			var e *oas.ErrorStatusCode
			switch {
			case tt.status == 0 && (err != nil || res.Status != "ok"):
				t.Fatalf("got %v, %v; want ok", res, err)
			case tt.status != 0 && (!errors.As(err, &e) || e.StatusCode != tt.status):
				t.Fatalf("err = %v, want status %d", err, tt.status)
			}
		})
	}
}

func TestUnmappedErrorsBecome500(t *testing.T) {
	e := (&Handler{}).NewError(context.Background(), errors.New("boom"))
	if e.StatusCode != http.StatusInternalServerError || e.Response.Error == "boom" {
		t.Fatalf("got %+v; want 500 without leaking the cause", e)
	}
}
