package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"orch-test/backend/internal/middleware"
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

// stubHandler counts createBook calls so tests can assert the handler was (not) reached.
type stubHandler struct {
	oas.UnimplementedHandler
	created int
}

func (s *stubHandler) CreateBook(context.Context, *oas.BookInput) (*oas.Book, error) {
	s.created++
	return &oas.Book{}, nil
}

func (s *stubHandler) ListBooks(context.Context, oas.ListBooksParams) (*oas.BookList, error) {
	return &oas.BookList{Items: []oas.Book{}}, nil
}

const bodyLimit = 64 << 10

func newStubServer(t *testing.T) (http.Handler, *stubHandler) {
	t.Helper()
	stub := &stubHandler{}
	h, err := New(stub)
	if err != nil {
		t.Fatal(err)
	}
	return h, stub
}

func postBook(h http.Handler, body io.Reader, contentLength int64) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/books", body)
	req.ContentLength = contentLength
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorText(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body oas.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not an Error: %v", rec.Body.String(), err)
	}
	return body.Error
}

func bookJSONPaddedTo(n int) []byte {
	const book = `{"isbn":"9780306406157","title":"T","authors":["A"]}`
	return append([]byte(book), bytes.Repeat([]byte(" "), n-len(book))...)
}

func TestBodyOverLimitIsRejectedWith413(t *testing.T) {
	tests := []struct {
		name          string
		contentLength func(n int) int64
	}{
		{"declared content length", func(n int) int64 { return int64(n) }},
		{"unknown content length", func(int) int64 { return -1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, stub := newStubServer(t)
			body := bookJSONPaddedTo(bodyLimit + 1)
			rec := postBook(h, bytes.NewReader(body), tt.contentLength(len(body)))
			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, want 413", rec.Code)
			}
			if got := errorText(t, rec); got != "request body too large" {
				t.Fatalf("error = %q", got)
			}
			if stub.created != 0 {
				t.Fatal("handler was called")
			}
		})
	}
}

func TestBodyAtLimitStillDecodes(t *testing.T) {
	h, stub := newStubServer(t)
	body := bookJSONPaddedTo(bodyLimit)
	rec := postBook(h, bytes.NewReader(body), -1)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	if stub.created != 1 {
		t.Fatalf("handler calls = %d, want 1", stub.created)
	}
}

func TestHugeNestedBodyGetsSmallResponse(t *testing.T) {
	tests := []struct {
		name   string
		size   int
		status int
	}{
		{"10 MB", 10 << 20, http.StatusRequestEntityTooLarge},
		{"within limit", bodyLimit - 1, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, stub := newStubServer(t)
			body := bytes.Repeat([]byte("["), tt.size)
			rec := postBook(h, bytes.NewReader(body), int64(len(body)))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if rec.Body.Len() >= 1024 {
				t.Fatalf("response is %d bytes", rec.Body.Len())
			}
			if stub.created != 0 {
				t.Fatal("handler was called")
			}
		})
	}
}

func TestMalformedBodyMessageIsGeneric(t *testing.T) {
	h, _ := newStubServer(t)
	body := `{"isbn": 5, "secret-field-value": "hunter2"`
	rec := postBook(h, strings.NewReader(body), int64(len(body)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := errorText(t, rec); got != "invalid request body" {
		t.Fatalf("error = %q", got)
	}
}

func TestUnsupportedContentType(t *testing.T) {
	h, _ := newStubServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/books", strings.NewReader("x"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	if got := errorText(t, rec); got != "unsupported content type" {
		t.Fatalf("error = %q", got)
	}
}

func TestParamErrorsAreGeneric(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"path", "/api/books/abc", `invalid path parameter "id"`},
		{"query", "/api/books?limit=abc", `invalid query parameter "limit"`},
		{"query validation", "/api/books?limit=0", `invalid query parameter "limit"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newStubServer(t)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			got := errorText(t, rec)
			if got != tt.want {
				t.Fatalf("error = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "strconv") {
				t.Fatalf("error leaks parser text: %q", got)
			}
		})
	}
}

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		status int
	}{
		{"200", http.MethodGet, "/api/books", http.StatusOK},
		{"400", http.MethodGet, "/api/books/abc", http.StatusBadRequest},
		{"404", http.MethodGet, "/api/nope", http.StatusNotFound},
		{"405", http.MethodPost, "/api/health", http.StatusMethodNotAllowed},
	}
	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"Cache-Control":           "no-store",
		"Content-Security-Policy": "default-src 'none'",
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newStubServer(t)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tt.method, tt.path, nil))
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			for k, v := range want {
				if got := rec.Header().Get(k); got != v {
					t.Errorf("%s = %q, want %q", k, got, v)
				}
			}
		})
	}
}

func TestOversizedBodyResponseHasSecurityHeaders(t *testing.T) {
	h, _ := newStubServer(t)
	body := bookJSONPaddedTo(bodyLimit + 1)
	rec := postBook(h, bytes.NewReader(body), int64(len(body)))
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing security headers on 413")
	}
}

// newLimitedServer builds the server with the default limits and a clock that never moves.
func newLimitedServer(t *testing.T) (http.Handler, *stubHandler) {
	t.Helper()
	stub := &stubHandler{}
	frozen := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	h, err := build(stub, func() time.Time { return frozen }, middleware.DefaultRateLimitConfig())
	if err != nil {
		t.Fatal(err)
	}
	return h, stub
}

const smallBook = `{"isbn":"9780306406157","title":"T","authors":["A"]}`

func TestWritesBeyondTheBurstGet429(t *testing.T) {
	h, stub := newLimitedServer(t)
	for i := range 20 {
		if rec := postBook(h, strings.NewReader(smallBook), int64(len(smallBook))); rec.Code != http.StatusCreated {
			t.Fatalf("POST #%d: status = %d, want 201", i+1, rec.Code)
		}
	}
	rec := postBook(h, strings.NewReader(smallBook), int64(len(smallBook)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
	if got := errorText(t, rec); got != "too many requests" {
		t.Fatalf("error = %q", got)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing security headers on 429")
	}
	if stub.created != 20 {
		t.Fatalf("handler calls = %d, want 20", stub.created)
	}
}

func TestOversizedBodiesCountAgainstTheWriteLimit(t *testing.T) {
	h, _ := newLimitedServer(t)
	body := bookJSONPaddedTo(bodyLimit + 1)
	for range 20 {
		postBook(h, bytes.NewReader(body), int64(len(body)))
	}
	if rec := postBook(h, bytes.NewReader(body), int64(len(body))); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
}

func postFromLoopback(h http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/books", strings.NewReader(smallBook))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:50000"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestNewLimitsLoopbackClientsByDefault(t *testing.T) {
	h, err := New(&stubHandler{})
	if err != nil {
		t.Fatal(err)
	}
	for range 20 {
		postFromLoopback(h)
	}
	if rec := postFromLoopback(h); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rec.Code)
	}
}

func TestWithRateLimitReplacesTheLimits(t *testing.T) {
	limits := middleware.DefaultRateLimitConfig()
	limits.Write = middleware.Limit{PerSecond: 1000, Burst: 1000}
	h, err := New(&stubHandler{}, WithRateLimit(limits))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		if rec := postFromLoopback(h); rec.Code != http.StatusCreated {
			t.Fatalf("POST #%d: status = %d, want 201", i+1, rec.Code)
		}
	}
}

func TestWithRateLimitRejectsInvalidConfig(t *testing.T) {
	if _, err := New(&stubHandler{}, WithRateLimit(middleware.RateLimitConfig{})); err == nil {
		t.Fatal("want an error for an incomplete config")
	}
}

func TestHealthIsNotRateLimited(t *testing.T) {
	h, _ := newLimitedServer(t)
	for i := range 500 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("GET /api/health #%d: status = %d, want 501 from the stub", i+1, rec.Code)
		}
	}
}
