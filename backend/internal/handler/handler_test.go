package handler

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// healthFixture is a Handler whose Ping counts calls and whose clock the test controls.
type healthFixture struct {
	h     *Handler
	pings atomic.Int32
	now   time.Time
	err   error // what Ping returns
}

func newHealthFixture() *healthFixture {
	f := &healthFixture{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	f.h = &Handler{
		Ping: func(context.Context) error {
			f.pings.Add(1)
			return f.err
		},
		now: func() time.Time { return f.now },
	}
	return f
}

func assertUnavailable(t *testing.T, res *oas.Health, err error) {
	t.Helper()
	var e *oas.ErrorStatusCode
	if !errors.As(err, &e) || e.StatusCode != http.StatusServiceUnavailable || e.Response.Error != "storage unavailable" {
		t.Fatalf("got %v, %v; want 503 storage unavailable", res, err)
	}
}

func TestHealthCachesAHealthyPing(t *testing.T) {
	f := newHealthFixture()
	for range 3 {
		if res, err := f.h.GetHealth(context.Background()); err != nil || res.Status != "ok" {
			t.Fatalf("got %v, %v; want ok", res, err)
		}
	}
	if n := f.pings.Load(); n != 1 {
		t.Fatalf("pings = %d, want 1", n)
	}
}

func TestHealthCachesAFailedPing(t *testing.T) {
	f := newHealthFixture()
	f.err = errors.New("connection refused")
	for range 3 {
		res, err := f.h.GetHealth(context.Background())
		assertUnavailable(t, res, err)
	}
	if n := f.pings.Load(); n != 1 {
		t.Fatalf("pings = %d, want 1", n)
	}
}

func TestHealthPingsAgainOnceTheResultIsOneSecondOld(t *testing.T) {
	f := newHealthFixture()
	_, _ = f.h.GetHealth(context.Background())

	f.now = f.now.Add(healthCacheTTL - time.Millisecond)
	_, _ = f.h.GetHealth(context.Background())
	if n := f.pings.Load(); n != 1 {
		t.Fatalf("pings just before the TTL = %d, want 1", n)
	}

	f.now = f.now.Add(time.Millisecond)
	f.err = errors.New("connection refused")
	res, err := f.h.GetHealth(context.Background())
	assertUnavailable(t, res, err)
	if n := f.pings.Load(); n != 2 {
		t.Fatalf("pings at the TTL = %d, want 2", n)
	}

	f.now = f.now.Add(healthCacheTTL)
	f.err = nil
	if res, err := f.h.GetHealth(context.Background()); err != nil || res.Status != "ok" {
		t.Fatalf("got %v, %v; want ok after recovery", res, err)
	}
}

func TestHealthConcurrentCallersShareOnePing(t *testing.T) {
	var pings atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	h := &Handler{Ping: func(context.Context) error {
		if pings.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}}

	const callers = 20
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res, err := h.GetHealth(context.Background()); err != nil || res.Status != "ok" {
				t.Errorf("got %v, %v; want ok", res, err)
			}
		}()
	}
	<-started
	time.Sleep(20 * time.Millisecond) // let the other callers queue up behind the ping
	close(release)
	wg.Wait()

	if n := pings.Load(); n != 1 {
		t.Fatalf("pings = %d, want 1", n)
	}
}

func TestHealthDoesNotCacheACancelledCaller(t *testing.T) {
	f := newHealthFixture()
	ctx, cancel := context.WithCancel(context.Background())
	f.h.Ping = func(ctx context.Context) error {
		f.pings.Add(1)
		cancel()
		return ctx.Err()
	}
	res, err := f.h.GetHealth(ctx)
	assertUnavailable(t, res, err)

	f.h.Ping = func(context.Context) error { f.pings.Add(1); return nil }
	if res, err := f.h.GetHealth(context.Background()); err != nil || res.Status != "ok" {
		t.Fatalf("got %v, %v; want ok: a caller's cancellation must not be cached", res, err)
	}
}

func TestUnmappedErrorsBecome500(t *testing.T) {
	e := (&Handler{}).NewError(context.Background(), errors.New("boom"))
	if e.StatusCode != http.StatusInternalServerError || e.Response.Error == "boom" {
		t.Fatalf("got %+v; want 500 without leaking the cause", e)
	}
}
