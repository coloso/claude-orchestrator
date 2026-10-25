// Package e2e_test drives the fully wired application through the ogen-generated client,
// checking the implementation against api/openapi.yaml from the outside.
package e2e_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"orch-test/backend/internal/bootstrap"
	"orch-test/backend/internal/middleware"
	"orch-test/backend/internal/oas"
)

// generousLimits is for functional tests: every request comes from 127.0.0.1, so the
// production limits would throttle tests that make many calls. The rate limit tests
// use the defaults instead.
func generousLimits() middleware.RateLimitConfig {
	cfg := middleware.DefaultRateLimitConfig()
	cfg.Read = middleware.Limit{PerSecond: 1e6, Burst: 1e6}
	cfg.Write = middleware.Limit{PerSecond: 1e6, Burst: 1e6}
	return cfg
}

// newClient starts a fresh, fully wired server and returns a generated client for it.
func newClient(t *testing.T) *oas.Client {
	t.Helper()
	app, err := bootstrap.New(context.Background(), bootstrap.Config{Storage: bootstrap.StorageMemory, RateLimit: generousLimits()})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.Handler)
	t.Cleanup(func() {
		srv.Close()
		_ = app.Close(context.Background())
	})
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
