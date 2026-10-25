// Package server wraps an oas.Handler in the ogen server and renders transport-level errors.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ogen-go/ogen/ogenerrors"

	"orch-test/backend/internal/middleware"
	"orch-test/backend/internal/oas"
)

// maxBodyBytes caps request bodies. The largest legal book is 20 authors x 200
// + 300 title code points = 4,300 code points; even at 12 bytes of JSON escape
// each that is about 52 KB.
const maxBodyBytes = 64 << 10

const msgBodyTooLarge = "request body too large"

// Option configures New.
type Option func(*options)

type options struct {
	limits middleware.RateLimitConfig
}

// WithRateLimit replaces the default per-client rate limits. The config is used as given,
// so it has to be complete (see middleware.DefaultRateLimitConfig).
func WithRateLimit(cfg middleware.RateLimitConfig) Option {
	return func(o *options) { o.limits = cfg }
}

// New returns the root HTTP handler for h, rate limited per client.
//
// Without WithRateLimit it uses middleware.DefaultRateLimitConfig. No client is exempt
// by address unless the config says so: a reverse proxy on the same host would otherwise
// lift the limit for all of its users. Without a trusted-proxy setting they share one
// bucket, so such a deployment has to rate limit in the proxy or raise the limits.
func New(h oas.Handler, opts ...Option) (http.Handler, error) {
	o := options{limits: middleware.DefaultRateLimitConfig()}
	for _, opt := range opts {
		opt(&o)
	}
	return build(h, time.Now, o.limits)
}

// build wraps h in the middleware chain: security headers, rate limit, body limit, ogen server.
// Rate limiting runs before the body limit so that rejected oversized writes still use a token.
func build(h oas.Handler, now func() time.Time, limits middleware.RateLimitConfig) (http.Handler, error) {
	limiter, err := middleware.NewRateLimiter(now, limits)
	if err != nil {
		return nil, err
	}
	s, err := oas.NewServer(h,
		oas.WithErrorHandler(writeError),
		oas.WithNotFound(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusNotFound, "no route for "+r.Method+" "+r.URL.Path)
		}),
		oas.WithMethodNotAllowed(func(w http.ResponseWriter, r *http.Request, allowed string) {
			w.Header().Set("Allow", allowed)
			writeJSON(w, http.StatusMethodNotAllowed, r.Method+" not allowed, use "+allowed)
		}),
	)
	if err != nil {
		return nil, err
	}
	return securityHeaders(limiter.Wrap(limitBody(s))), nil
}

// securityHeaders sets the response headers of a JSON-only API on every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "default-src 'none'")
		next.ServeHTTP(w, r)
	})
}

// limitBody rejects declared oversized bodies up front and caps what is read otherwise.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > maxBodyBytes {
			writeJSON(w, http.StatusRequestEntityTooLarge, msgBodyTooLarge)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

// writeError renders request decoding/validation errors in the spec's Error format.
// The ogen error text can echo input and parser internals, so it is only logged.
func writeError(ctx context.Context, w http.ResponseWriter, _ *http.Request, err error) {
	slog.DebugContext(ctx, "transport error", "error", err)

	var (
		tooLarge *http.MaxBytesError
		param    *ogenerrors.DecodeParamError
	)
	code := ogenerrors.ErrorCode(err)
	switch {
	case errors.As(err, &tooLarge):
		writeJSON(w, http.StatusRequestEntityTooLarge, msgBodyTooLarge)
	case errors.As(err, &param):
		writeJSON(w, http.StatusBadRequest, fmt.Sprintf("invalid %s parameter %q", param.In, param.Name))
	case code == http.StatusBadRequest:
		writeJSON(w, code, "invalid request body")
	case code == http.StatusUnsupportedMediaType:
		writeJSON(w, code, "unsupported content type")
	default:
		writeJSON(w, code, strings.ToLower(http.StatusText(code)))
	}
}

func writeJSON(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(oas.Error{Error: msg})
}
