package server

import (
	"context"
	"net/http/httptest"
	"testing"

	"orch-test/backend/internal/oas"
)

// newClient starts the real server and returns an ogen-generated client for it.
func newClient(t *testing.T) *oas.Client {
	t.Helper()
	h, err := New()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	client, err := oas.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestHealth(t *testing.T) {
	res, err := newClient(t).GetHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok", res.Status)
	}
}
