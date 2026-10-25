package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func testConfig() RateLimitConfig {
	return RateLimitConfig{
		Read:        Limit{PerSecond: 10, Burst: 3},
		Write:       Limit{PerSecond: 1, Burst: 2},
		MaxClients:  3,
		IdleTimeout: time.Minute,
		ExemptPaths: []string{"/api/health"},
	}
}

func newTestLimiter(t *testing.T, cfg RateLimitConfig) (*RateLimiter, http.Handler, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	l, err := NewRateLimiter(clock.Now, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return l, l.Wrap(ok), clock
}

func send(h http.Handler, method, path, remoteAddr string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

const clientA, clientB = "192.0.2.1:1111", "192.0.2.2:2222"

// sendN sends n requests and fails unless every one of them passes.
func sendN(t *testing.T, h http.Handler, n int, method, path, remoteAddr string) {
	t.Helper()
	for i := range n {
		if rec := send(h, method, path, remoteAddr); rec.Code != http.StatusNoContent {
			t.Fatalf("%s %s #%d from %s: status = %d, want 204", method, path, i+1, remoteAddr, rec.Code)
		}
	}
}

func assertLimited(t *testing.T, rec *httptest.ResponseRecorder, retryAfter string) {
	t.Helper()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != retryAfter {
		t.Errorf("Retry-After = %q, want %q", got, retryAfter)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"error":"too many requests"}` {
		t.Errorf("body = %s", got)
	}
}

func TestBurstThenTooManyRequests(t *testing.T) {
	tests := []struct {
		method string
		burst  int
	}{
		{http.MethodGet, 3},
		{http.MethodHead, 3},
		{http.MethodPost, 2},
		{http.MethodPut, 2},
		{http.MethodDelete, 2},
		{http.MethodPatch, 2},
		{http.MethodOptions, 2},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			_, h, _ := newTestLimiter(t, testConfig())
			sendN(t, h, tt.burst, tt.method, "/api/books", clientA)
			assertLimited(t, send(h, tt.method, "/api/books", clientA), "1")
		})
	}
}

func TestRetryAfterIsTimeUntilNextToken(t *testing.T) {
	tests := []struct {
		name      string
		perSecond float64
		advance   time.Duration
		want      string
	}{
		{"slow rate", 0.25, 0, "4"},
		{"partly refilled", 0.25, 2500 * time.Millisecond, "2"},
		{"fast rate rounds up to one second", 1000, 0, "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			cfg.Write = Limit{PerSecond: tt.perSecond, Burst: 1}
			_, h, clock := newTestLimiter(t, cfg)
			sendN(t, h, 1, http.MethodPost, "/api/books", clientA)
			clock.Advance(tt.advance)
			assertLimited(t, send(h, http.MethodPost, "/api/books", clientA), tt.want)
		})
	}
}

func TestTokensRefillAsTheClockAdvances(t *testing.T) {
	_, h, clock := newTestLimiter(t, testConfig())
	sendN(t, h, 2, http.MethodPost, "/api/books", clientA)

	clock.Advance(999 * time.Millisecond)
	assertLimited(t, send(h, http.MethodPost, "/api/books", clientA), "1")

	clock.Advance(time.Millisecond)
	sendN(t, h, 1, http.MethodPost, "/api/books", clientA)
	assertLimited(t, send(h, http.MethodPost, "/api/books", clientA), "1")
}

func TestRefillStopsAtBurst(t *testing.T) {
	_, h, clock := newTestLimiter(t, testConfig())
	sendN(t, h, 2, http.MethodPost, "/api/books", clientA)
	clock.Advance(30 * time.Second)
	sendN(t, h, 2, http.MethodPost, "/api/books", clientA)
	assertLimited(t, send(h, http.MethodPost, "/api/books", clientA), "1")
}

func TestClockGoingBackwardsAddsNoTokens(t *testing.T) {
	_, h, clock := newTestLimiter(t, testConfig())
	sendN(t, h, 2, http.MethodPost, "/api/books", clientA)
	clock.Advance(-time.Hour)
	assertLimited(t, send(h, http.MethodPost, "/api/books", clientA), "1")
}

func TestReadsAndWritesHaveSeparateBuckets(t *testing.T) {
	_, h, _ := newTestLimiter(t, testConfig())
	sendN(t, h, 2, http.MethodPost, "/api/books", clientA)
	assertLimited(t, send(h, http.MethodPost, "/api/books", clientA), "1")
	sendN(t, h, 3, http.MethodGet, "/api/books", clientA)
}

func TestClientsAreIndependent(t *testing.T) {
	_, h, _ := newTestLimiter(t, testConfig())
	sendN(t, h, 2, http.MethodPost, "/api/books", clientA)
	assertLimited(t, send(h, http.MethodPost, "/api/books", clientA), "1")
	sendN(t, h, 2, http.MethodPost, "/api/books", clientB)
}

func TestClientIsTheHostWithoutPort(t *testing.T) {
	_, h, _ := newTestLimiter(t, testConfig())
	sendN(t, h, 1, http.MethodPost, "/api/books", "192.0.2.1:1")
	sendN(t, h, 1, http.MethodPost, "/api/books", "192.0.2.1:2")
	assertLimited(t, send(h, http.MethodPost, "/api/books", "192.0.2.1:3"), "1")

	sendN(t, h, 2, http.MethodPost, "/api/books", "[2001:db8::1]:1")
	assertLimited(t, send(h, http.MethodPost, "/api/books", "[2001:db8::1]:2"), "1")
}

func TestIPv6ClientsInTheSame64ShareABucket(t *testing.T) {
	_, h, _ := newTestLimiter(t, testConfig())
	sendN(t, h, 1, http.MethodPost, "/api/books", "[2001:db8:1:2::1]:1")
	sendN(t, h, 1, http.MethodPost, "/api/books", "[2001:db8:1:2:ffff:ffff:ffff:ffff]:2")
	assertLimited(t, send(h, http.MethodPost, "/api/books", "[2001:db8:1:2:aaaa::5]:3"), "1")
}

func TestIPv6ClientsInDifferent64sAreIndependent(t *testing.T) {
	_, h, _ := newTestLimiter(t, testConfig())
	sendN(t, h, 2, http.MethodPost, "/api/books", "[2001:db8:1:2::1]:1")
	assertLimited(t, send(h, http.MethodPost, "/api/books", "[2001:db8:1:2::1]:1"), "1")
	sendN(t, h, 2, http.MethodPost, "/api/books", "[2001:db8:1:3::1]:1")
}

func TestIPv4MappedIPv6StaysPerAddress(t *testing.T) {
	_, h, _ := newTestLimiter(t, testConfig())
	sendN(t, h, 2, http.MethodPost, "/api/books", "[::ffff:192.0.2.1]:1")
	assertLimited(t, send(h, http.MethodPost, "/api/books", "[::ffff:192.0.2.1]:2"), "1")
	sendN(t, h, 2, http.MethodPost, "/api/books", "[::ffff:192.0.2.2]:1")
}

func TestRemoteAddrWithoutPortIsUsedAsIs(t *testing.T) {
	_, h, _ := newTestLimiter(t, testConfig())
	sendN(t, h, 2, http.MethodPost, "/api/books", "@")
	assertLimited(t, send(h, http.MethodPost, "/api/books", "@"), "1")
	sendN(t, h, 2, http.MethodPost, "/api/books", "")
}

func TestForwardedHeadersAreIgnored(t *testing.T) {
	_, h, _ := newTestLimiter(t, testConfig())
	for i := range 3 {
		req := httptest.NewRequest(http.MethodPost, "/api/books", nil)
		req.RemoteAddr = clientA
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		req.Header.Set("X-Real-IP", fmt.Sprintf("198.51.100.%d", i))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if i == 2 {
			assertLimited(t, rec, "1")
		}
	}
}

func TestExemptPathsAreNeverLimited(t *testing.T) {
	_, h, _ := newTestLimiter(t, testConfig())
	sendN(t, h, 2, http.MethodPost, "/api/books", clientA)
	sendN(t, h, 3, http.MethodGet, "/api/books", clientA)
	sendN(t, h, 100, http.MethodGet, "/api/health", clientA)
	sendN(t, h, 100, http.MethodPost, "/api/health", clientA)
}

func TestExemptPathsDoNotUseTokens(t *testing.T) {
	_, h, _ := newTestLimiter(t, testConfig())
	sendN(t, h, 100, http.MethodGet, "/api/health", clientA)
	sendN(t, h, 3, http.MethodGet, "/api/books", clientA)
}

func TestExemptPathMatchIsExact(t *testing.T) {
	_, h, _ := newTestLimiter(t, testConfig())
	sendN(t, h, 3, http.MethodGet, "/api/health/", clientA)
	assertLimited(t, send(h, http.MethodGet, "/api/healthz", clientA), "1")
}

func TestLoopbackExemption(t *testing.T) {
	for _, exempt := range []bool{false, true} {
		t.Run(fmt.Sprint("exempt=", exempt), func(t *testing.T) {
			cfg := testConfig()
			cfg.ExemptLoopback = exempt
			_, h, _ := newTestLimiter(t, cfg)
			for _, addr := range []string{"127.0.0.1:1", "[::1]:1"} {
				if exempt {
					sendN(t, h, 50, http.MethodPost, "/api/books", addr)
					continue
				}
				sendN(t, h, 2, http.MethodPost, "/api/books", addr)
				assertLimited(t, send(h, http.MethodPost, "/api/books", addr), "1")
			}
			sendN(t, h, 2, http.MethodPost, "/api/books", clientA)
			assertLimited(t, send(h, http.MethodPost, "/api/books", clientA), "1")
		})
	}
}

func TestClientTableNeverExceedsTheCap(t *testing.T) {
	l, h, clock := newTestLimiter(t, testConfig())
	for i := range 1000 {
		addr := fmt.Sprintf("10.0.%d.%d:80", i/256, i%256)
		sendN(t, h, 1, http.MethodGet, "/api/books", addr)
		if n := l.clientCount(); n > 3 {
			t.Fatalf("after %d clients the table has %d entries, cap is 3", i+1, n)
		}
		clock.Advance(time.Millisecond)
	}
}

func TestFullTableEvictsTheLeastRecentlySeenClient(t *testing.T) {
	_, h, _ := newTestLimiter(t, testConfig())
	sendN(t, h, 2, http.MethodPost, "/api/books", "192.0.2.1:1")
	sendN(t, h, 1, http.MethodPost, "/api/books", "192.0.2.2:1")
	sendN(t, h, 1, http.MethodPost, "/api/books", "192.0.2.3:1")
	// 192.0.2.1 is seen again, so 192.0.2.2 is now the least recent one.
	sendN(t, h, 1, http.MethodGet, "/api/books", "192.0.2.1:1")
	sendN(t, h, 1, http.MethodPost, "/api/books", "192.0.2.4:1")

	// 192.0.2.1 kept its empty bucket ...
	assertLimited(t, send(h, http.MethodPost, "/api/books", "192.0.2.1:1"), "1")
	// ... while 192.0.2.2 was evicted and starts over with a full one.
	sendN(t, h, 2, http.MethodPost, "/api/books", "192.0.2.2:1")
}

func TestIdleClientsAreEvicted(t *testing.T) {
	l, h, clock := newTestLimiter(t, testConfig())
	sendN(t, h, 1, http.MethodGet, "/api/books", clientA)
	sendN(t, h, 1, http.MethodGet, "/api/books", clientB)
	clock.Advance(30 * time.Second)
	sendN(t, h, 1, http.MethodGet, "/api/books", clientB)

	clock.Advance(30*time.Second + time.Millisecond)
	sendN(t, h, 1, http.MethodGet, "/api/health", "192.0.2.9:1")
	if n := l.clientCount(); n != 2 {
		t.Fatalf("table has %d entries before the idle sweep, want 2 (exempt requests do not sweep)", n)
	}
	sendN(t, h, 1, http.MethodGet, "/api/books", "192.0.2.9:1")
	if n := l.clientCount(); n != 2 {
		t.Fatalf("table has %d entries, want 2 (idle client A evicted)", n)
	}
}

func TestDefaultRateLimitConfig(t *testing.T) {
	cfg := DefaultRateLimitConfig()
	if cfg.Read != (Limit{PerSecond: 50, Burst: 100}) {
		t.Errorf("Read = %+v", cfg.Read)
	}
	if cfg.Write != (Limit{PerSecond: 5, Burst: 20}) {
		t.Errorf("Write = %+v", cfg.Write)
	}
	if cfg.MaxClients != 10_000 {
		t.Errorf("MaxClients = %d", cfg.MaxClients)
	}
	if cfg.IdleTimeout != 5*time.Minute {
		t.Errorf("IdleTimeout = %v", cfg.IdleTimeout)
	}
	if len(cfg.ExemptPaths) != 1 || cfg.ExemptPaths[0] != "/api/health" {
		t.Errorf("ExemptPaths = %v", cfg.ExemptPaths)
	}
	if _, err := NewRateLimiter(time.Now, cfg); err != nil {
		t.Errorf("default config is invalid: %v", err)
	}
}

func TestNewRateLimiterRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		modify func(c *RateLimitConfig)
	}{
		{"zero read rate", func(c *RateLimitConfig) { c.Read.PerSecond = 0 }},
		{"negative write rate", func(c *RateLimitConfig) { c.Write.PerSecond = -1 }},
		{"zero read burst", func(c *RateLimitConfig) { c.Read.Burst = 0 }},
		{"zero write burst", func(c *RateLimitConfig) { c.Write.Burst = 0 }},
		{"zero max clients", func(c *RateLimitConfig) { c.MaxClients = 0 }},
		{"zero idle timeout", func(c *RateLimitConfig) { c.IdleTimeout = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig()
			tt.modify(&cfg)
			if _, err := NewRateLimiter(time.Now, cfg); err == nil {
				t.Fatal("want an error")
			}
		})
	}
	if _, err := NewRateLimiter(nil, testConfig()); err == nil {
		t.Fatal("nil clock: want an error")
	}
}
