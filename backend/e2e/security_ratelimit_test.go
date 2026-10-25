package e2e_test

import (
	"net/http"
	"strings"
	"testing"

	"orch-test/backend/internal/middleware"
)

// TestRateLimitAppliesToLoopbackAndSparesHealth runs the app with the default limits
// (a zero bootstrap.Config.RateLimit). The client connects over loopback and is limited.
func TestRateLimitAppliesToLoopbackAndSparesHealth(t *testing.T) {
	cfg, db := e2eMongoConfig(t)
	cfg.RateLimit = middleware.RateLimitConfig{}
	env := e2eServe(t, cfg, db)

	do := func(method, path, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, env.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res
	}

	// An invalid body is rejected with 400 but still takes a write token.
	for i := range 20 {
		if res := do(http.MethodPost, "/api/books", "{}"); res.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("write #%d limited within the burst", i+1)
		}
	}
	res := do(http.MethodPost, "/api/books", "{}")
	if res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("write after the burst: status = %d, want 429", res.StatusCode)
	}
	if res.Header.Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}

	for i := range 300 {
		if res := do(http.MethodGet, "/api/health", ""); res.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/health #%d: status = %d, want 200", i+1, res.StatusCode)
		}
	}
}
