// Package middleware holds HTTP middleware for the API server.
package middleware

import (
	"container/list"
	"errors"
	"math"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"time"
)

// Limit is a token bucket: it refills at PerSecond tokens per second up to Burst tokens,
// and every request takes one token.
type Limit struct {
	PerSecond float64
	Burst     int
}

// RateLimitConfig configures a RateLimiter.
type RateLimitConfig struct {
	Read  Limit // GET and HEAD
	Write Limit // every other method
	// MaxClients caps the client table; when it is full, the least recently seen client is dropped.
	MaxClients int
	// IdleTimeout drops clients that sent no limited request for this long.
	IdleTimeout time.Duration
	// ExemptPaths are never limited (exact match on the URL path), e.g. the container health check.
	ExemptPaths []string
	// ExemptLoopback skips limiting for clients on the same host (127.0.0.0/8, ::1).
	ExemptLoopback bool
}

// DefaultRateLimitConfig returns the limits the API server uses.
func DefaultRateLimitConfig() RateLimitConfig {
	return RateLimitConfig{
		Read:        Limit{PerSecond: 50, Burst: 100},
		Write:       Limit{PerSecond: 5, Burst: 20},
		MaxClients:  10_000,
		IdleTimeout: 5 * time.Minute,
		ExemptPaths: []string{"/api/health"},
	}
}

// RateLimiter limits requests per client with one read and one write token bucket each.
// A client is the host part of r.RemoteAddr (IPv6 hosts are grouped by /64 prefix); forwarding headers such as X-Forwarded-For
// are client-controlled and ignored.
type RateLimiter struct {
	now func() time.Time
	cfg RateLimitConfig

	mu      sync.Mutex
	clients map[string]*list.Element // of *client
	lru     *list.List               // front: most recently seen
}

type client struct {
	key         string
	lastSeen    time.Time
	read, write bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter returns a RateLimiter that reads the time from now.
func NewRateLimiter(now func() time.Time, cfg RateLimitConfig) (*RateLimiter, error) {
	switch {
	case now == nil:
		return nil, errors.New("rate limiter: clock is nil")
	case !cfg.Read.valid() || !cfg.Write.valid():
		return nil, errors.New("rate limiter: rates and bursts must be positive")
	case cfg.MaxClients <= 0:
		return nil, errors.New("rate limiter: MaxClients must be positive")
	case cfg.IdleTimeout <= 0:
		return nil, errors.New("rate limiter: IdleTimeout must be positive")
	}
	cfg.ExemptPaths = slices.Clone(cfg.ExemptPaths)
	return &RateLimiter{
		now:     now,
		cfg:     cfg,
		clients: make(map[string]*list.Element),
		lru:     list.New(),
	}, nil
}

func (l Limit) valid() bool { return l.PerSecond > 0 && l.Burst > 0 }

// Wrap returns next guarded by the limiter. Limited requests get a 429 with Retry-After.
func (rl *RateLimiter) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := remoteHost(r.RemoteAddr)
		if slices.Contains(rl.cfg.ExemptPaths, r.URL.Path) || (rl.cfg.ExemptLoopback && isLoopback(host)) {
			next.ServeHTTP(w, r)
			return
		}
		if wait, ok := rl.allow(clientKey(host), isRead(r.Method)); !ok {
			tooManyRequests(w, wait)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// allow takes a token from the client's bucket, or reports how long until one is available.
func (rl *RateLimiter) allow(key string, read bool) (time.Duration, bool) {
	now := rl.now()
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.evictIdle(now)
	c := rl.client(key, now)
	c.lastSeen = now
	if read {
		return c.read.take(now, rl.cfg.Read)
	}
	return c.write.take(now, rl.cfg.Write)
}

// evictIdle drops clients from the back of the LRU list that have been idle too long.
func (rl *RateLimiter) evictIdle(now time.Time) {
	for e := rl.lru.Back(); e != nil; e = rl.lru.Back() {
		if now.Sub(e.Value.(*client).lastSeen) <= rl.cfg.IdleTimeout {
			return
		}
		rl.remove(e)
	}
}

// client returns the entry for key, creating one with full buckets if needed.
func (rl *RateLimiter) client(key string, now time.Time) *client {
	if e, ok := rl.clients[key]; ok {
		rl.lru.MoveToFront(e)
		return e.Value.(*client)
	}
	for len(rl.clients) >= rl.cfg.MaxClients {
		rl.remove(rl.lru.Back())
	}
	c := &client{
		key:   key,
		read:  bucket{tokens: float64(rl.cfg.Read.Burst), last: now},
		write: bucket{tokens: float64(rl.cfg.Write.Burst), last: now},
	}
	rl.clients[key] = rl.lru.PushFront(c)
	return c
}

func (rl *RateLimiter) remove(e *list.Element) {
	rl.lru.Remove(e)
	delete(rl.clients, e.Value.(*client).key)
}

func (rl *RateLimiter) clientCount() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.clients)
}

// take refills b for the time since its last use and takes one token.
func (b *bucket) take(now time.Time, lim Limit) (time.Duration, bool) {
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = math.Min(float64(lim.Burst), b.tokens+elapsed.Seconds()*lim.PerSecond)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	return time.Duration((1 - b.tokens) / lim.PerSecond * float64(time.Second)), false
}

// clientKey is the rate limit key for host: IPv6 addresses share a bucket per /64 prefix,
// IPv4 and IPv4-mapped addresses are keyed per address.
func clientKey(host string) string {
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	addr = addr.WithZone("")
	if addr.Is4In6() {
		return addr.Unmap().String()
	}
	if addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func tooManyRequests(w http.ResponseWriter, wait time.Duration) {
	secs := max(1, int64(math.Ceil(wait.Seconds())))
	w.Header().Set("Retry-After", strconv.FormatInt(secs, 10))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"error":"too many requests"}` + "\n"))
}
