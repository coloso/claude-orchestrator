package e2e_test

// Security regression tests for the catalog (S-02): the books API, its MongoDB repository, demo
// seeding and the container files. They pin the secure behavior described in
// docs/security/S02-10-books-api-hardening.md and complement security_books_test.go, which covers
// the spec-level findings (body size, unknown fields, numbers, headers, timeouts).
//
// Requests are raw net/http, because the generated client cannot send malformed input. Every
// top-level identifier is prefixed with "securityCatalog". Every test runs against MongoDB, on its
// own database test_<random hex> that the shared e2e helper drops when the test ends; without
// MONGO_TEST_URI the tests fail.
//
// A test that starts with t.Skip("SECURITY <id>: ...") pins behavior that does not hold yet; the
// follow-up named there removes the skip.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"orch-test/backend/internal/bootstrap"
)

// --- harness ---------------------------------------------------------------------------------

// securityCatalogEnv is a running application on a fresh MongoDB test database db.
type securityCatalogEnv struct {
	url string
	app *bootstrap.App
	db  *mongo.Database
}

// securityCatalogServe starts the application for cfg behind an httptest server.
func securityCatalogServe(t *testing.T, cfg bootstrap.Config, db *mongo.Database) securityCatalogEnv {
	t.Helper()
	env := e2eServe(t, cfg, db)
	return securityCatalogEnv{url: env.URL, app: env.App, db: env.DB}
}

// securityCatalogStart starts a fresh application on a fresh MongoDB database.
func securityCatalogStart(t *testing.T) securityCatalogEnv {
	t.Helper()
	cfg, db := e2eMongoConfig(t)
	return securityCatalogServe(t, cfg, db)
}

type securityCatalogResp struct {
	status int
	header http.Header
	body   []byte
}

func (r securityCatalogResp) String() string { return fmt.Sprintf("%d %s", r.status, r.body) }

// message returns the "error" text of an Error response, failing if the body is not one.
func (r securityCatalogResp) message(t *testing.T) string {
	t.Helper()
	var e struct {
		Error *string `json:"error"`
	}
	if err := json.Unmarshal(r.body, &e); err != nil || e.Error == nil {
		t.Fatalf("response %.200q is not an {\"error\": ...} body (%v)", r.body, err)
	}
	return *e.Error
}

var securityCatalogClient = &http.Client{Timeout: 30 * time.Second}

// securityCatalogTry sends one request and returns the error instead of failing, so that it is
// safe to call from goroutines. An empty contentType sends none; a nil body sends none.
func securityCatalogTry(method, target, contentType string, body []byte) (securityCatalogResp, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, target, r)
	if err != nil {
		return securityCatalogResp{}, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := securityCatalogClient.Do(req)
	if err != nil {
		return securityCatalogResp{}, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return securityCatalogResp{}, err
	}
	return securityCatalogResp{status: res.StatusCode, header: res.Header, body: b}, nil
}

// securityCatalogSend is securityCatalogTry that fails the test on a transport error.
func securityCatalogSend(t *testing.T, method, target, contentType string, body []byte) securityCatalogResp {
	t.Helper()
	res, err := securityCatalogTry(method, target, contentType, body)
	if err != nil {
		t.Fatalf("%s %.100s: %v", method, target, err)
	}
	return res
}

func securityCatalogGet(t *testing.T, env securityCatalogEnv, path string) securityCatalogResp {
	t.Helper()
	return securityCatalogSend(t, http.MethodGet, env.url+path, "", nil)
}

func securityCatalogPost(t *testing.T, env securityCatalogEnv, body []byte) securityCatalogResp {
	t.Helper()
	return securityCatalogSend(t, http.MethodPost, env.url+"/api/books", "application/json", body)
}

func securityCatalogPut(t *testing.T, env securityCatalogEnv, id any, body []byte) securityCatalogResp {
	t.Helper()
	return securityCatalogSend(t, http.MethodPut, fmt.Sprintf("%s/api/books/%v", env.url, id), "application/json", body)
}

func securityCatalogDelete(t *testing.T, env securityCatalogEnv, id any) securityCatalogResp {
	t.Helper()
	return securityCatalogSend(t, http.MethodDelete, fmt.Sprintf("%s/api/books/%v", env.url, id), "", nil)
}

// securityCatalogISBN returns a valid ISBN-13 (13 ASCII digits) that is distinct for every n.
func securityCatalogISBN(n int) string {
	digits := fmt.Sprintf("978%09d", n)
	sum := 0
	for i, r := range digits {
		d := int(r - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return digits + strconv.Itoa((10-sum%10)%10)
}

// securityCatalogBook builds a book body from valid defaults for book number n. A nil override
// removes the field; json.RawMessage values are sent as they are.
func securityCatalogBook(n int, over map[string]any) []byte {
	m := map[string]any{"isbn": securityCatalogISBN(n), "title": "Clean Code", "authors": []string{"Robert C. Martin"}}
	for k, v := range over {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

// securityCatalogUpdate is securityCatalogBook plus a version.
func securityCatalogUpdate(n int, version int64, over map[string]any) []byte {
	m := map[string]any{"version": version}
	for k, v := range over {
		m[k] = v
	}
	return securityCatalogBook(n, m)
}

type securityCatalogBookDoc struct {
	ID      int64    `json:"id"`
	Isbn    string   `json:"isbn"`
	Title   string   `json:"title"`
	Authors []string `json:"authors"`
	Version int64    `json:"version"`
}

type securityCatalogListDoc struct {
	Items []securityCatalogBookDoc `json:"items"`
	Total int64                    `json:"total"`
}

func (l securityCatalogListDoc) ids() []int64 {
	ids := make([]int64, len(l.Items))
	for i, b := range l.Items {
		ids[i] = b.ID
	}
	return ids
}

// securityCatalogCreate adds book number n with the given authors and returns it.
func securityCatalogCreate(t *testing.T, env securityCatalogEnv, n int, authors ...string) securityCatalogBookDoc {
	t.Helper()
	res := securityCatalogPost(t, env, securityCatalogBook(n, map[string]any{"authors": authors}))
	if res.status != http.StatusCreated {
		t.Fatalf("create book %d: %s", n, res)
	}
	var b securityCatalogBookDoc
	if err := json.Unmarshal(res.body, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func securityCatalogList(t *testing.T, env securityCatalogEnv, query string) securityCatalogListDoc {
	t.Helper()
	res := securityCatalogGet(t, env, "/api/books"+query)
	if res.status != http.StatusOK {
		t.Fatalf("GET /api/books%s: %s", query, res)
	}
	var l securityCatalogListDoc
	if err := json.Unmarshal(res.body, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

// securityCatalogRequireEmpty fails if the catalog holds any book.
func securityCatalogRequireEmpty(t *testing.T, env securityCatalogEnv) {
	t.Helper()
	if l := securityCatalogList(t, env, ""); l.Total != 0 || len(l.Items) != 0 {
		t.Fatalf("catalog has %d books (total %d), want none: a rejected request must not store anything", len(l.Items), l.Total)
	}
}

// securityCatalogLeaks are fragments of library, Go or database internals that must never reach a client.
var securityCatalogLeaks = []string{
	"strconv", "bookinput", "bookupdate", "callback", "operation ", "decode", "oas.", "ogen", "jx",
	"goroutine", "panic", "runtime", ".go:", "github.com", "orch-test/", "$ne", "$gt", "$where",
	"mongo", "e11000", "dup key", "duplicate key", "isbn_unique", "_id_", "index", "collection", "bson",
	"counters", "keyvalue", "context deadline", "disconnected", "errinfo", "validation",
}

// securityCatalogRequireClean fails if res is not a small {"error": ...} body free of internals.
func securityCatalogRequireClean(t *testing.T, what string, res securityCatalogResp) {
	t.Helper()
	if len(res.body) > 512 {
		t.Errorf("%s: %d byte response, want a short error: %.200s", what, len(res.body), res.body)
	}
	if ct := res.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("%s: Content-Type %q, want application/json", what, ct)
	}
	low := strings.ToLower(string(res.body))
	for _, leak := range securityCatalogLeaks {
		if strings.Contains(low, leak) {
			t.Errorf("%s: response leaks %q: %.300s", what, leak, res.body)
		}
	}
}

// securityCatalogRune returns the one-character string for code point r. Test sources avoid
// writing invisible characters literally.
func securityCatalogRune(r rune) string { return string(r) }

// --- typed input: no value of a request can become a query operator ---------------------------

func TestSecurityCatalogJSONOfTheWrongShapeIsRejected(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	type field struct {
		name   string
		values []json.RawMessage
	}
	operatorsAndShapes := []json.RawMessage{raw(`{"$ne":""}`), raw(`{"$gt":""}`), raw(`{}`), raw(`["x"]`), raw(`null`), raw(`1`), raw(`true`)}
	fields := []field{
		{"isbn", operatorsAndShapes},
		{"title", operatorsAndShapes},
		{"language", operatorsAndShapes},
		{"authors", []json.RawMessage{raw(`{"$ne":""}`), raw(`[{"$ne":""}]`), raw(`[["a"]]`), raw(`[null]`), raw(`[1]`), raw(`"Alice"`), raw(`null`), raw(`{}`)}},
		{"publishedYear", []json.RawMessage{raw(`{"$gt":0}`), raw(`[2000]`), raw(`"2000"`), raw(`null`), raw(`true`)}},
		{"pages", []json.RawMessage{raw(`{"$gt":0}`), raw(`[10]`), raw(`"10"`), raw(`null`), raw(`true`)}},
	}
	utf16Body := func(order func([]byte, uint16) []byte) []byte {
		var out []byte
		for _, u := range utf16.Encode([]rune(string(securityCatalogBook(1, nil)))) {
			out = order(out, u)
		}
		return out
	}
	bom := securityCatalogRune(0xFEFF)
	shapes := map[string][]byte{
		"JSON string":               []byte(`"isbn"`),
		"JSON array":                []byte(`[]`),
		"array of books":            append(append([]byte("["), securityCatalogBook(1, nil)...), ']'),
		"JSON number":               []byte(`1`),
		"null":                      []byte(`null`),
		"empty object":              []byte(`{}`),
		"byte order mark":           append([]byte(bom), securityCatalogBook(1, nil)...),
		"UTF-16LE":                  utf16Body(func(b []byte, u uint16) []byte { return append(b, byte(u), byte(u>>8)) }),
		"UTF-16BE":                  utf16Body(func(b []byte, u uint16) []byte { return append(b, byte(u>>8), byte(u)) }),
		"differently cased key":     securityCatalogBook(1, map[string]any{"ISBN": "x", "isbn": nil}),
		"extra differently cased":   securityCatalogBook(1, map[string]any{"Title": "x"}),
		"extra operator-named key":  securityCatalogBook(1, map[string]any{"$where": "sleep(5000)"}),
		"extra dotted key":          securityCatalogBook(1, map[string]any{"authors.0": "x"}),
		"extra nested object key":   securityCatalogBook(1, map[string]any{"title": "x", "meta": json.RawMessage(`{"$set":{"version":9}}`)}),
		"required field as null":    securityCatalogBook(1, map[string]any{"authors": json.RawMessage(`null`)}),
		"required field missing":    securityCatalogBook(1, map[string]any{"title": nil}),
		"required field is an obj":  securityCatalogBook(1, map[string]any{"isbn": json.RawMessage(`{"$ne":null}`)}),
		"truncated":                 securityCatalogBook(1, nil)[:30],
		"trailing second document":  append(securityCatalogBook(1, nil), securityCatalogBook(2, nil)...),
		"trailing garbage":          append(securityCatalogBook(1, nil), []byte(`xyz`)...),
		"NUL byte in the body":      append([]byte{0}, securityCatalogBook(1, nil)...),
		"duplicate key, bad second": []byte(`{"isbn":"` + securityCatalogISBN(1) + `","title":"x","authors":["a"],"title":{"$ne":1}}`),
	}
	env := securityCatalogStart(t)
	check := func(what string, res securityCatalogResp) {
		t.Helper()
		if res.status != http.StatusBadRequest {
			t.Errorf("%s: %.200s, want 400", what, res)
			return
		}
		securityCatalogRequireClean(t, what, res)
	}
	for _, f := range fields {
		for _, v := range f.values {
			check(fmt.Sprintf("POST %s=%s", f.name, v), securityCatalogPost(t, env, securityCatalogBook(1, map[string]any{f.name: v})))
		}
	}
	for what, body := range shapes {
		check("POST "+what, securityCatalogPost(t, env, body))
	}
	securityCatalogRequireEmpty(t, env)

	// The same on update, plus the version: a rejected update changes nothing.
	book := securityCatalogCreate(t, env, 1, "Alice")
	for _, f := range fields {
		for _, v := range f.values {
			check(fmt.Sprintf("PUT %s=%s", f.name, v), securityCatalogPut(t, env, book.ID, securityCatalogUpdate(1, 1, map[string]any{f.name: v})))
		}
	}
	for _, v := range []json.RawMessage{raw(`{"$gte":0}`), raw(`{"$ne":1}`), raw(`[1]`), raw(`"1"`), raw(`null`), raw(`1.5`), raw(`1e0`)} {
		check("PUT version="+string(v), securityCatalogPut(t, env, book.ID, securityCatalogUpdate(1, 1, map[string]any{"version": v})))
	}
	for what, body := range shapes {
		check("PUT "+what, securityCatalogPut(t, env, book.ID, body))
	}
	got := securityCatalogGet(t, env, fmt.Sprintf("/api/books/%d", book.ID))
	var b securityCatalogBookDoc
	if err := json.Unmarshal(got.body, &b); err != nil || b.Version != 1 || b.Title != "Clean Code" || !slices.Equal(b.Authors, []string{"Alice"}) {
		t.Errorf("rejected updates changed the book: %s", got)
	}
	if l := securityCatalogList(t, env, ""); l.Total != 1 {
		t.Errorf("total = %d, want 1", l.Total)
	}
}

func TestSecurityCatalogAuthorFilterNeverBecomesAnOperator(t *testing.T) {
	env := securityCatalogStart(t)
	// Three distinct authors, so that any operator reading of the filter selects a proper,
	// non-empty subset: $ne Alice -> {Bob, Carol}, $regex Al -> {Alice}, $in [Alice] -> {Alice}.
	alice := securityCatalogCreate(t, env, 1, "Alice")
	bob := securityCatalogCreate(t, env, 2, "Bob")
	carol := securityCatalogCreate(t, env, 3, "Carol")
	all := []int64{alice.ID, bob.ID, carol.ID}

	// A request may be rejected, may have its odd parameter ignored (all books), or may treat
	// the value as the literal author name. Nothing else is allowed.
	for _, tc := range []struct {
		query   string
		literal []int64 // the result of an exact match of the value, as the API means it
	}{
		{"?author[$ne]=Alice", all},
		{"?author[$regex]=Al", all},
		{"?author[$regex]=.*", all},
		{"?author[$in][]=Alice", all},
		{"?author[$in]=Alice", all},
		{"?author[$gt]=", all},
		{"?author[$exists]=true", all},
		{"?author.$ne=Alice", all},
		{"?author[0]=Alice", all},
		{"?$or[0][authors]=Alice", all},
		{"?$where=sleep(3000)", all},
		{"?authors=Alice", all},
		{"?author=Bob&author[$ne]=Bob", []int64{bob.ID}},
		{"?author=%24ne", nil},
		{"?author=%7B%22%24ne%22%3A%22Alice%22%7D", nil},
		{"?author=%7B%22%24regex%22%3A%22.%2A%22%7D", nil},
		{"?author=Al", nil},
		{"?author=.%2A", nil},
		{"?author=Alice%20", nil},
		{"?author=ALICE", nil},
	} {
		start := time.Now()
		res := securityCatalogGet(t, env, "/api/books"+tc.query)
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("GET %s took %v: the value looks like it was executed", tc.query, d)
		}
		if res.status == http.StatusBadRequest {
			securityCatalogRequireClean(t, tc.query, res)
			continue
		}
		if res.status != http.StatusOK {
			t.Errorf("GET %s: %.200s, want 200 or 400", tc.query, res)
			continue
		}
		var l securityCatalogListDoc
		if err := json.Unmarshal(res.body, &l); err != nil {
			t.Fatal(err)
		}
		got := l.ids()
		if !slices.Equal(got, all) && !slices.Equal(got, tc.literal) {
			t.Errorf("GET %s returned books %v, want all %v (parameter ignored) or %v (literal match)", tc.query, got, all, tc.literal)
		}
		if l.Total != int64(len(got)) {
			t.Errorf("GET %s: total %d with %d items", tc.query, l.Total, len(got))
		}
	}
}

func TestSecurityCatalogOperatorLookingAuthorNamesStayText(t *testing.T) {
	env := securityCatalogStart(t)
	names := []string{`{"$ne":""}`, `$where`, `$ne`, `.*`, `a.b`, `{"$gt":""}`, `Robert'); DROP TABLE books;--`}
	for i, name := range names {
		securityCatalogCreate(t, env, i+1, name, "Plain Author")
	}
	securityCatalogCreate(t, env, 100, "Someone Else")
	for i, name := range names {
		l := securityCatalogList(t, env, "?author="+url.QueryEscape(name))
		if l.Total != 1 || len(l.Items) != 1 || l.Items[0].Isbn != securityCatalogISBN(i+1) {
			t.Errorf("author %q matched %d books (%v), want exactly the book that has it", name, l.Total, l.ids())
		}
		if !slices.Contains(l.Items[0].Authors, name) {
			t.Errorf("author %q was stored as %q", name, l.Items[0].Authors)
		}
	}
	if l := securityCatalogList(t, env, "?author=Plain%20Author"); l.Total != int64(len(names)) {
		t.Errorf("Plain Author total = %d, want %d", l.Total, len(names))
	}
}

// --- the author filter and other list parameters ------------------------------------------------

// Valid but unusual filters. Filters that are not valid text are in
// TestSecurityCatalogAuthorFilterFollowsTheTextRules.
func TestSecurityCatalogOddAuthorFiltersAreNeverAServerError(t *testing.T) {
	env := securityCatalogStart(t)
	securityCatalogCreate(t, env, 1, "Alice")
	for name, query := range map[string]string{
		"zero width space":         "?author=%e2%80%8b",
		"percent-encoded operator": "?author=%7B%22%24ne%22%3A%22%22%7D",
		"200 two-byte characters":  "?author=" + strings.Repeat("%C3%A9", 200),
		"200 four-byte characters": "?author=" + strings.Repeat("%F0%9F%98%80", 200),
		"empty":                    "?author=",
		"offset at the limit":      "?offset=100000",
		"limit and offset at max":  "?limit=100&offset=100000&author=Alice",
	} {
		start := time.Now()
		res := securityCatalogGet(t, env, "/api/books"+query)
		if res.status != http.StatusOK && res.status != http.StatusBadRequest {
			t.Errorf("%s (GET %.60s): %.200s, want 200 or 400", name, query, res)
			continue
		}
		if res.status == http.StatusBadRequest {
			securityCatalogRequireClean(t, name, res)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("%s: took %v", name, d)
		}
	}
	// A name of the longest legal size can be looked up by exact match: the bound counts
	// characters, as the domain does, not bytes.
	long := strings.Repeat("é", 200)
	securityCatalogCreate(t, env, 2, long)
	if l := securityCatalogList(t, env, "?author="+strings.Repeat("%C3%A9", 200)); l.Total != 1 {
		t.Errorf("filter by a 200-character author matched %d books, want 1", l.Total)
	}
}

func TestSecurityCatalogAuthorFilterFollowsTheTextRules(t *testing.T) {
	env := securityCatalogStart(t)
	securityCatalogCreate(t, env, 1, "Alice")
	for name, query := range map[string]string{
		"invalid UTF-8":         "?author=%ff",
		"overlong UTF-8":        "?author=%c0%af",
		"lone surrogate (CESU)": "?author=%ed%a0%80",
		"NUL":                   "?author=a%00b",
		"newline":               "?author=Alice%0a",
		"CRLF":                  "?author=Alice%0d%0a",
		"DEL":                   "?author=Alice%7f",
	} {
		res := securityCatalogGet(t, env, "/api/books"+query)
		if res.status != http.StatusBadRequest {
			t.Errorf("%s: %.200s, want 400: no stored name can contain it, and it should not reach the database", name, res)
			continue
		}
		if msg := res.message(t); !strings.Contains(msg, "author") {
			t.Errorf("%s: message %q does not name the author parameter", name, msg)
		}
		securityCatalogRequireClean(t, name, res)
	}
}

// --- text that looks empty but is not ----------------------------------------------------------

func TestSecurityCatalogTextWithoutAVisibleCharacterIsRejected(t *testing.T) {
	invisible := map[string]string{
		"zero width space":            securityCatalogRune(0x200B),
		"zero width non-joiner":       securityCatalogRune(0x200C),
		"zero width joiner":           securityCatalogRune(0x200D),
		"word joiner":                 securityCatalogRune(0x2060),
		"byte order mark":             securityCatalogRune(0xFEFF),
		"soft hyphen":                 securityCatalogRune(0x00AD),
		"left-to-right mark":          securityCatalogRune(0x200E),
		"right-to-left override":      securityCatalogRune(0x202E),
		"several of them with spaces": " " + securityCatalogRune(0x200B) + " " + securityCatalogRune(0xFEFF) + securityCatalogRune(0x200D) + " ",
	}
	emoji := securityCatalogRune(0x1F469) + securityCatalogRune(0x200D) + securityCatalogRune(0x1F4BB)
	env := securityCatalogStart(t)
	n := 0
	for name, s := range invisible {
		for _, tc := range []struct {
			field string
			over  map[string]any
		}{
			{"title", map[string]any{"title": s}},
			{"authors", map[string]any{"authors": []string{s}}},
			{"authors", map[string]any{"authors": []string{"Visible Author", s}}},
		} {
			n++ // a distinct ISBN, so that one stored book cannot turn later cases into 409s
			res := securityCatalogPost(t, env, securityCatalogBook(n, tc.over))
			if res.status != http.StatusBadRequest {
				t.Errorf("POST %s as %s: %.200s, want 400", name, tc.field, res)
			} else if msg := res.message(t); !strings.Contains(msg, tc.field) {
				t.Errorf("POST %s as %s: message %q does not name the field", name, tc.field, msg)
			}
		}
	}
	securityCatalogRequireEmpty(t, env)

	book := securityCatalogCreate(t, env, 1, "Alice")
	for name, s := range invisible {
		if res := securityCatalogPut(t, env, book.ID, securityCatalogUpdate(1, 1, map[string]any{"title": s})); res.status != http.StatusBadRequest {
			t.Errorf("PUT %s as title: %.200s, want 400", name, res)
		}
		if res := securityCatalogPut(t, env, book.ID, securityCatalogUpdate(1, 1, map[string]any{"authors": []string{s}})); res.status != http.StatusBadRequest {
			t.Errorf("PUT %s as author: %.200s, want 400", name, res)
		}
	}

	// The rule is about text with nothing to see, not about format characters in general.
	for name, over := range map[string]map[string]any{
		"emoji sequence with a joiner": {"title": emoji},
		"invisible next to a letter":   {"title": "Go" + securityCatalogRune(0x200B)},
	} {
		res := securityCatalogPost(t, env, securityCatalogBook(2, over))
		if res.status != http.StatusCreated {
			t.Errorf("%s: %.200s, want 201", name, res)
		}
		var b securityCatalogBookDoc
		if err := json.Unmarshal(res.body, &b); err == nil {
			securityCatalogDelete(t, env, b.ID)
		}
	}
}

// --- ISBN: ASCII digits only, one spelling per book --------------------------------------------

func TestSecurityCatalogISBNAcceptsOnlyASCIIDigitsHyphensAndSpaces(t *testing.T) {
	base := securityCatalogISBN(1)
	inScript := func(zero rune) string {
		var b strings.Builder
		for _, c := range base {
			b.WriteRune(zero + (c - '0'))
		}
		return b.String()
	}
	withSeparator := func(sep string) string { return base[:3] + sep + base[3:] }
	bad := map[string]string{
		"Arabic-Indic digits":          inScript(0x0660),
		"extended Arabic-Indic digits": inScript(0x06F0),
		"Devanagari digits":            inScript(0x0966),
		"full-width digits":            inScript(0xFF10),
		"mathematical bold digits":     inScript(0x1D7CE),
		"mathematical double-struck":   inScript(0x1D7D8),
		"subscript digits":             inScript(0x2080),
		"one non-ASCII digit":          base[:12] + securityCatalogRune(0x0660+rune(base[12]-'0')),
		"no-break space":               withSeparator(securityCatalogRune(0x00A0)),
		"thin space":                   withSeparator(securityCatalogRune(0x2009)),
		"ideographic space":            withSeparator(securityCatalogRune(0x3000)),
		"unicode hyphen":               withSeparator(securityCatalogRune(0x2010)),
		"non-breaking hyphen":          withSeparator(securityCatalogRune(0x2011)),
		"en dash":                      withSeparator(securityCatalogRune(0x2013)),
		"minus sign":                   withSeparator(securityCatalogRune(0x2212)),
		"full-width hyphen":            withSeparator(securityCatalogRune(0xFF0D)),
		"tab":                          withSeparator("\t"),
		"newline":                      withSeparator("\n"),
		"underscore":                   withSeparator("_"),
		"dot":                          withSeparator("."),
		"slash":                        withSeparator("/"),
		"byte order mark prefix":       securityCatalogRune(0xFEFF) + base,
		"zero width space":             withSeparator(securityCatalogRune(0x200B)),
		"trailing newline":             base + "\n",
		"ISBN prefix":                  "ISBN " + base,
		"ISBN-10 spelling":             "0132350882",
		"14 digits":                    base + "0",
		"12 digits":                    base[:12],
		"only separators":              "- - -",
	}
	good := []string{base, withSeparator("-"), withSeparator(" "), " " + base + " ", base[:3] + "-" + base[3:4] + "-" + base[4:6] + "-" + base[6:12] + "-" + base[12:]}
	env := securityCatalogStart(t)
	for name, isbn := range bad {
		res := securityCatalogPost(t, env, securityCatalogBook(1, map[string]any{"isbn": isbn}))
		if res.status != http.StatusBadRequest {
			t.Errorf("POST %s: %.200s, want 400", name, res)
			continue
		}
		if msg := res.message(t); !strings.HasPrefix(msg, "isbn:") {
			t.Errorf("POST %s: message %q does not name the isbn field", name, msg)
		}
		securityCatalogRequireClean(t, name, res)
	}
	securityCatalogRequireEmpty(t, env)

	book := securityCatalogCreate(t, env, 2, "Alice")
	for name, isbn := range bad {
		if res := securityCatalogPut(t, env, book.ID, securityCatalogUpdate(2, 1, map[string]any{"isbn": isbn})); res.status != http.StatusBadRequest {
			t.Errorf("PUT %s: %.200s, want 400", name, res)
		}
	}
	if got := securityCatalogGet(t, env, fmt.Sprintf("/api/books/%d", book.ID)); !strings.Contains(string(got.body), securityCatalogISBN(2)) {
		t.Errorf("rejected updates changed the ISBN: %s", got)
	}

	// The accepted spellings are stored in the one canonical form, so they cannot be
	// told apart from each other later.
	securityCatalogDelete(t, env, book.ID)
	for i, isbn := range good {
		res := securityCatalogPost(t, env, securityCatalogBook(1, map[string]any{"isbn": isbn}))
		if i == 0 && res.status != http.StatusCreated {
			t.Fatalf("POST %q: %.200s, want 201", isbn, res)
		}
		if i > 0 && res.status != http.StatusConflict {
			t.Errorf("POST %q again: %.200s, want 409", isbn, res)
		}
		if res.status == http.StatusCreated {
			var b securityCatalogBookDoc
			_ = json.Unmarshal(res.body, &b)
			if b.Isbn != base {
				t.Errorf("stored isbn %q, want the canonical %q", b.Isbn, base)
			}
		}
	}
	if l := securityCatalogList(t, env, ""); l.Total != 1 {
		t.Errorf("total = %d after %d spellings of one ISBN, want 1", l.Total, len(good))
	}
}

func TestSecurityCatalogOneBookHasOneISBNWhateverTheSpelling(t *testing.T) {
	env := securityCatalogStart(t)
	first := securityCatalogCreate(t, env, 1, "Alice")
	second := securityCatalogCreate(t, env, 2, "Bob")
	isbn := securityCatalogISBN(1)
	spelled := isbn[:3] + "-" + isbn[3:4] + " " + isbn[4:]

	// PUT onto another book's ISBN in a different spelling is the same conflict.
	res := securityCatalogPut(t, env, second.ID, securityCatalogUpdate(2, 1, map[string]any{"isbn": spelled}))
	if res.status != http.StatusConflict {
		t.Errorf("PUT onto %q: %.200s, want 409", spelled, res)
	}
	// A book may keep its own ISBN, written another way.
	res = securityCatalogPut(t, env, first.ID, securityCatalogUpdate(1, 1, map[string]any{"isbn": spelled}))
	if res.status != http.StatusOK {
		t.Fatalf("PUT own ISBN respelled: %.200s, want 200", res)
	}
	var b securityCatalogBookDoc
	if err := json.Unmarshal(res.body, &b); err != nil || b.Isbn != isbn || b.Version != 2 {
		t.Errorf("respelled update returned %s, want isbn %s and version 2", res, isbn)
	}
	// Moving away frees the old ISBN for others; the new one is taken.
	moved := securityCatalogISBN(3)
	if res := securityCatalogPut(t, env, first.ID, securityCatalogUpdate(1, 2, map[string]any{"isbn": moved})); res.status != http.StatusOK {
		t.Fatalf("PUT move: %.200s, want 200", res)
	}
	if res := securityCatalogPost(t, env, securityCatalogBook(1, nil)); res.status != http.StatusCreated {
		t.Errorf("POST the freed ISBN: %.200s, want 201", res)
	}
	if res := securityCatalogPost(t, env, securityCatalogBook(3, nil)); res.status != http.StatusConflict {
		t.Errorf("POST the new ISBN of the moved book: %.200s, want 409", res)
	}
}

// --- error bodies --------------------------------------------------------------------------------

func TestSecurityCatalogErrorsAreFixedTextsThatNeverEchoInput(t *testing.T) {
	const marker = "MARKER-7c1e"
	env := securityCatalogStart(t)
	first := securityCatalogCreate(t, env, 1, "Alice")
	second := securityCatalogCreate(t, env, 2, "Bob")

	wantBody := func(what string, res securityCatalogResp, status int, message string) {
		t.Helper()
		if res.status != status {
			t.Errorf("%s: %.200s, want %d", what, res, status)
			return
		}
		if got := res.message(t); got != message {
			t.Errorf("%s: message %q, want %q", what, got, message)
		}
		securityCatalogRequireClean(t, what, res)
	}

	const duplicate = "a book with this ISBN already exists"
	const conflict = "version conflict: the book was changed since it was read"
	const notFound = "book not found"
	wantBody("POST duplicate", securityCatalogPost(t, env, securityCatalogBook(1, map[string]any{"title": marker})), 409, duplicate)
	wantBody("PUT onto another ISBN", securityCatalogPut(t, env, second.ID, securityCatalogUpdate(1, 1, map[string]any{"title": marker})), 409, duplicate)

	if res := securityCatalogPut(t, env, first.ID, securityCatalogUpdate(1, 1, map[string]any{"title": "Second edition"})); res.status != http.StatusOK {
		t.Fatalf("PUT: %.200s, want 200", res)
	}
	wantBody("PUT stale version", securityCatalogPut(t, env, first.ID, securityCatalogUpdate(1, 1, map[string]any{"title": marker})), 409, conflict)

	// An id that never existed and an id that was deleted look the same.
	securityCatalogDelete(t, env, second.ID)
	for _, id := range []int64{second.ID, 424242} {
		wantBody(fmt.Sprintf("GET %d", id), securityCatalogGet(t, env, fmt.Sprintf("/api/books/%d", id)), 404, notFound)
		wantBody(fmt.Sprintf("PUT %d", id), securityCatalogPut(t, env, id, securityCatalogUpdate(2, 1, nil)), 404, notFound)
		wantBody(fmt.Sprintf("DELETE %d", id), securityCatalogDelete(t, env, id), 404, notFound)
	}

	// Validation messages name the field and the rule, never the value.
	for name, over := range map[string]map[string]any{
		"isbn":     {"isbn": marker},
		"title":    {"title": strings.Repeat(marker, 40)},
		"authors":  {"authors": []string{marker, strings.Repeat(marker, 40)}},
		"language": {"language": marker},
	} {
		res := securityCatalogPost(t, env, securityCatalogBook(5, over))
		if res.status != http.StatusBadRequest {
			t.Errorf("POST bad %s: %.200s, want 400", name, res)
			continue
		}
		if msg := res.message(t); !strings.HasPrefix(msg, name+":") || strings.Contains(msg, marker) {
			t.Errorf("POST bad %s: message %q must start with the field name and not echo the value", name, msg)
		}
		securityCatalogRequireClean(t, "bad "+name, res)
	}
}

// A repository error that no adapter maps must come out as the generic 500, whatever the driver or
// server said. This goes through errors the database itself produces, not a disconnected client
// (security_books_test.go covers that): a counter that is not a number, and a counter that is
// behind the data, which makes the next insert collide on _id.
func TestSecurityCatalogDatabaseErrorsBecomeAGeneric500(t *testing.T) {
	env := securityCatalogStart(t)
	db := env.db
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	counters := db.Collection("counters")
	book := securityCatalogCreate(t, env, 1, "Alice")

	const wantBody = `{"error":"internal server error"}`
	requireGeneric := func(what string, res securityCatalogResp) {
		t.Helper()
		if res.status != http.StatusInternalServerError {
			t.Errorf("%s: %.200s, want 500", what, res)
			return
		}
		if got := strings.TrimSpace(string(res.body)); got != wantBody {
			t.Errorf("%s: body %q, want %q", what, got, wantBody)
		}
		securityCatalogRequireClean(t, what, res)
	}

	// 1. The counter is not a number: $inc fails on the server (TypeMismatch).
	if _, err := counters.UpdateOne(ctx, bson.D{{Key: "_id", Value: "books"}}, bson.D{{Key: "$set", Value: bson.D{{Key: "seq", Value: "not a number"}}}}); err != nil {
		t.Fatal(err)
	}
	requireGeneric("create with a broken counter", securityCatalogPost(t, env, securityCatalogBook(2, nil)))
	if res := securityCatalogGet(t, env, fmt.Sprintf("/api/books/%d", book.ID)); res.status != http.StatusOK {
		t.Errorf("GET with a broken counter: %.200s, want 200: reads do not depend on it", res)
	}

	// 2. The counter is behind the data: the next ID is taken. The server reports a duplicate
	// key on _id, with the key value and the index name; it is not a duplicate ISBN.
	if _, err := counters.UpdateOne(ctx, bson.D{{Key: "_id", Value: "books"}}, bson.D{{Key: "$set", Value: bson.D{{Key: "seq", Value: int64(0)}}}}); err != nil {
		t.Fatal(err)
	}
	requireGeneric("create with the counter behind the data", securityCatalogPost(t, env, securityCatalogBook(3, nil)))
	if l := securityCatalogList(t, env, ""); l.Total != 1 {
		t.Errorf("total = %d, want 1: the failed create must not store anything", l.Total)
	}
	// The failed attempt used the ID, so the next create moves on.
	res := securityCatalogPost(t, env, securityCatalogBook(3, nil))
	if res.status != http.StatusCreated {
		t.Errorf("create after the collision: %.200s, want 201", res)
	}
}

// --- consistency ---------------------------------------------------------------------------------

func TestSecurityCatalogConcurrentCreatesKeepIDsUniqueAndISBNsOnce(t *testing.T) {
	env := securityCatalogStart(t)
	// A fresh catalog: the very first creates race to make the counter document.
	const books, racers = 30, 2
	var (
		mu        sync.Mutex
		created   = map[int64]int{} // ID -> ISBN number
		conflicts int
		wg        sync.WaitGroup
	)
	for n := 1; n <= books; n++ {
		for r := 0; r < racers; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := securityCatalogTry(http.MethodPost, env.url+"/api/books", "application/json", securityCatalogBook(n, nil))
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					t.Errorf("create %d: %v", n, err)
					return
				}
				switch res.status {
				case http.StatusCreated:
					var b securityCatalogBookDoc
					if err := json.Unmarshal(res.body, &b); err != nil {
						t.Errorf("create %d: %v", n, err)
						return
					}
					if prev, dup := created[b.ID]; dup {
						t.Errorf("ID %d was given to ISBN %d and ISBN %d", b.ID, prev, n)
					}
					created[b.ID] = n
				case http.StatusConflict:
					conflicts++
				default:
					t.Errorf("create %d: %.200s, want 201 or 409", n, res)
				}
			}()
		}
	}
	wg.Wait()
	if len(created) != books || conflicts != books*(racers-1) {
		t.Fatalf("%d created and %d conflicts, want %d and %d", len(created), conflicts, books, books*(racers-1))
	}
	l := securityCatalogList(t, env, "?limit=100")
	if l.Total != books {
		t.Errorf("total = %d, want %d", l.Total, books)
	}
	highest := int64(0)
	for id := range created {
		if id < 1 {
			t.Errorf("ID %d is not positive", id)
		}
		highest = max(highest, id)
	}

	// A removed ID is never given out again, not even when it was the highest.
	if res := securityCatalogDelete(t, env, highest); res.status != http.StatusNoContent {
		t.Fatalf("DELETE %d: %.200s", highest, res)
	}
	next := securityCatalogCreate(t, env, books+1, "Alice")
	if next.ID <= highest {
		t.Errorf("new book got ID %d after ID %d was removed, want a greater one", next.ID, highest)
	}
}

// A process that dies between taking an ID from the counter and inserting the book leaves a gap,
// nothing else: no duplicate, no reuse, no error for the next caller.
func TestSecurityCatalogACrashAfterTheCounterOnlyLeavesAGap(t *testing.T) {
	env := securityCatalogStart(t)
	db := env.db
	first := securityCatalogCreate(t, env, 1, "Alice")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// What a crashed instance leaves behind: the counter moved on, the insert never happened.
	if _, err := db.Collection("counters").UpdateOne(ctx, bson.D{{Key: "_id", Value: "books"}}, bson.D{{Key: "$inc", Value: bson.D{{Key: "seq", Value: int64(5)}}}}); err != nil {
		t.Fatal(err)
	}
	next := securityCatalogCreate(t, env, 2, "Bob")
	if next.ID != first.ID+6 {
		t.Errorf("ID after 5 lost IDs = %d, want %d (a gap, no reuse)", next.ID, first.ID+6)
	}
	for _, id := range []int64{first.ID + 1, first.ID + 5} {
		if res := securityCatalogGet(t, env, fmt.Sprintf("/api/books/%d", id)); res.status != http.StatusNotFound {
			t.Errorf("GET %d (in the gap): %.200s, want 404", id, res)
		}
	}
	if l := securityCatalogList(t, env, ""); l.Total != 2 || !slices.Equal(l.ids(), []int64{first.ID, next.ID}) {
		t.Errorf("list = %v (total %d), want exactly the two books", l.ids(), l.Total)
	}
}

// --- seeding -----------------------------------------------------------------------------------

func TestSecurityCatalogDemoSeedingIsOptIn(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false}, {"false", false}, {"0", false}, {"no", false}, {"yes", false}, {"on", false},
		{"enabled", false}, {" true", false}, {"true ", false}, {"garbage", false}, {"2", false},
		{"true", true}, {"1", true},
	} {
		t.Setenv("SEED_DEMO_DATA", tc.value)
		if got := bootstrap.ConfigFromEnv().SeedDemoData; got != tc.want {
			t.Errorf("SEED_DEMO_DATA=%q: SeedDemoData = %v, want %v", tc.value, got, tc.want)
		}
	}
	t.Setenv("SEED_DEMO_DATA", "")
	if got := bootstrap.ConfigFromEnv().SeedDemoData; got {
		t.Error("SeedDemoData is on without SEED_DEMO_DATA")
	}

	// A config that leaves SeedDemoData off serves an empty catalog.
	cfg, db := e2eMongoConfig(t)
	if cfg.SeedDemoData {
		t.Fatal("e2eMongoConfig turns demo seeding on")
	}
	securityCatalogRequireEmpty(t, securityCatalogServe(t, cfg, db))
}

func TestSecurityCatalogSeedingLogsOnlyASummaryAtInfo(t *testing.T) {
	var logs securityCatalogLogBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, db := e2eMongoConfig(t)
	cfg.SeedDemoData, cfg.Logger = true, logger
	env := securityCatalogServe(t, cfg, db)
	if l := securityCatalogList(t, env, "?limit=100"); l.Total != 12 {
		t.Fatalf("seeded %d books, want 12", l.Total)
	}
	out := logs.String()
	if !strings.Contains(out, "seeded 12 demo books, skipped 0") {
		t.Errorf("no seeding summary in the log: %q", out)
	}
	for _, detail := range []string{"Domain-Driven Design", "Clean Code", "Eric Evans", "9780132350884"} {
		if strings.Contains(out, detail) {
			t.Errorf("the Info log carries the book detail %q: %q", detail, out)
		}
	}
}

// Two or more instances starting at once, as with a scaled-out deployment, all start and end up
// with one copy of each demo book.
func TestSecurityCatalogConcurrentSeedingConvergesOnOneDemoCatalog(t *testing.T) {
	cfg, db := e2eMongoConfig(t)
	// Migrate first; migrating concurrently is the schema lock's job and tested elsewhere.
	securityCatalogServe(t, cfg, db)

	cfg.SeedDemoData = true
	const instances = 4
	var wg sync.WaitGroup
	apps := make([]*bootstrap.App, instances)
	errs := make([]error, instances)
	for i := range instances {
		wg.Add(1)
		go func() {
			defer wg.Done()
			apps[i], errs[i] = bootstrap.New(context.Background(), cfg)
		}()
	}
	wg.Wait()
	for i := range instances {
		if errs[i] != nil {
			t.Errorf("instance %d did not start: %v", i, errs[i])
		} else {
			t.Cleanup(func() { _ = apps[i].Close(context.Background()) })
		}
	}
	if t.Failed() {
		return
	}
	cfg.SeedDemoData = false
	env := securityCatalogServe(t, cfg, db)
	l := securityCatalogList(t, env, "?limit=100")
	seen := map[string]bool{}
	ids := map[int64]bool{}
	for _, b := range l.Items {
		if seen[b.Isbn] {
			t.Errorf("ISBN %s was seeded twice", b.Isbn)
		}
		if ids[b.ID] {
			t.Errorf("ID %d was given twice", b.ID)
		}
		seen[b.Isbn], ids[b.ID] = true, true
	}
	if l.Total != 12 || len(l.Items) != 12 {
		t.Errorf("catalog has %d books (total %d) after %d concurrent seedings, want 12", len(l.Items), l.Total, instances)
	}
}

// A seeding failure stops the start-up with an error. It does not serve a half-seeded catalog
// silently. What was already added stays (every book is added on its own), and the next start
// finishes the job without duplicates.
func TestSecurityCatalogSeedFailureStopsStartupAndARestartCompletes(t *testing.T) {
	cfg, db := e2eMongoConfig(t)
	securityCatalogServe(t, cfg, db) // migrates

	// A foreign book sits on the ID that the third demo book would get.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := db.Collection("counters").InsertOne(ctx, bson.D{{Key: "_id", Value: "books"}, {Key: "seq", Value: int64(2)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Collection("books").InsertOne(ctx, bson.D{
		{Key: "_id", Value: int64(5)}, {Key: "isbn", Value: securityCatalogISBN(900)}, {Key: "title", Value: "Squatter"},
		{Key: "authors", Value: bson.A{"Someone"}}, {Key: "createdAt", Value: now}, {Key: "updatedAt", Value: now}, {Key: "version", Value: int64(1)},
	}); err != nil {
		t.Fatal(err)
	}

	cfg.SeedDemoData = true
	app, err := bootstrap.New(context.Background(), cfg)
	if err == nil {
		_ = app.Close(context.Background())
		t.Fatal("bootstrap.New succeeded although seeding failed; want a start-up error")
	}
	if app != nil {
		t.Error("bootstrap.New returned an app together with the error")
	}
	if !strings.Contains(err.Error(), "seed demo data") {
		t.Errorf("error %q does not say that seeding failed", err)
	}

	cfg.SeedDemoData = false
	env := securityCatalogServe(t, cfg, db)
	if l := securityCatalogList(t, env, "?limit=100"); l.Total != 3 {
		t.Errorf("after the failed start the catalog has %d books, want the 2 seeded before the failure plus the foreign one", l.Total)
	}

	cfg.SeedDemoData = true
	env = securityCatalogServe(t, cfg, db)
	l := securityCatalogList(t, env, "?limit=100")
	seen := map[string]int{}
	for _, b := range l.Items {
		seen[b.Isbn]++
	}
	for isbn, n := range seen {
		if n != 1 {
			t.Errorf("ISBN %s appears %d times", isbn, n)
		}
	}
	if l.Total != 13 || len(seen) != 13 {
		t.Errorf("after the restart the catalog has %d books (%d distinct ISBNs), want 12 demo books plus the foreign one", l.Total, len(seen))
	}
}

// --- logs ----------------------------------------------------------------------------------------

type securityCatalogLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *securityCatalogLogBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *securityCatalogLogBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// Values that reach a log line must not be able to start a new, forged line. No t.Parallel: the
// test replaces the process-wide default logger.
func TestSecurityCatalogHostileValuesCannotForgeLogLines(t *testing.T) {
	var logs securityCatalogLogBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	forged := "time=2099-01-01T00:00:00.000Z level=ERROR msg=FORGED-LOG-LINE"
	escaped := strings.NewReplacer("\n", "%0a", "\r", "%0d", " ", "%20").Replace("x\r\n" + forged)
	line := securityCatalogRune(0x2028)

	env := securityCatalogStart(t)
	for _, path := range []string{
		"/api/books/" + escaped,
		"/api/books?limit=" + escaped,
		"/api/books?offset=" + escaped,
		"/api/books?author=" + escaped,
		"/api/" + escaped,
	} {
		securityCatalogGet(t, env, path)
	}
	for _, body := range [][]byte{
		securityCatalogBook(1, map[string]any{"title": "x\n" + forged}),
		securityCatalogBook(1, map[string]any{"authors": []string{"x\r\n" + forged}}),
		securityCatalogBook(1, map[string]any{"title": "x" + line + forged, "publishedYear": json.RawMessage(`"x"`)}),
		securityCatalogBook(1, map[string]any{"x\n" + forged: 1}),
		[]byte("x\n" + forged),
	} {
		securityCatalogPost(t, env, body)
		securityCatalogPut(t, env, 1, body)
	}

	out := logs.String()
	if !strings.Contains(out, "transport error") {
		t.Fatalf("the log capture saw no transport error; the test would not notice a forged line: %q", out)
	}
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.HasPrefix(l, "time=") || !strings.Contains(l, " level=") {
			t.Errorf("log line does not start like a record, a value broke out of its record: %.200q", l)
		}
		if strings.HasPrefix(l, "time=2099") {
			t.Errorf("forged log line: %.200q", l)
		}
	}
}

// --- surface -------------------------------------------------------------------------------------

func TestSecurityCatalogOnlyTheDocumentedOperationsExist(t *testing.T) {
	env := securityCatalogStart(t)
	book := securityCatalogCreate(t, env, 1, "Alice")
	hostile := securityCatalogBook(2, map[string]any{"title": "Hostile"})
	id := fmt.Sprint(book.ID)

	// No bulk operations, and no way to reach a book by a path trick.
	for _, tc := range []struct{ method, path string }{
		{"DELETE", "/api/books"}, {"PUT", "/api/books"}, {"PATCH", "/api/books"}, {"OPTIONS", "/api/books"}, {"TRACE", "/api/books"},
		{"POST", "/api/books/" + id}, {"PATCH", "/api/books/" + id}, {"OPTIONS", "/api/books/" + id}, {"TRACE", "/api/books/" + id},
		{"DELETE", "/api/books/"}, {"DELETE", "/api/books/" + id + "/"}, {"DELETE", "/api/books//" + id},
		{"DELETE", "/api/books/" + id + "/extra"}, {"DELETE", "/api/books/%2e%2e/books/" + id}, {"DELETE", "/api/books/" + id + "%2f"},
		{"DELETE", "/api/books/" + id + ";x=1"}, {"DELETE", "/api/books/" + id + "%00"}, {"DELETE", "/API/books/" + id},
		{"DELETE", "/api/books/*"}, {"DELETE", "/api/books/all"}, {"DELETE", "/api/books?id=" + id},
		{"POST", "/api/books/" + id + "/delete"}, {"GET", "/api/books/" + id + "/versions"}, {"GET", "/api/books/export"},
		{"GET", "/api/counters"}, {"GET", "/api/admin"}, {"GET", "/metrics"}, {"GET", "/debug/pprof/"}, {"GET", "/debug/vars"},
	} {
		ct := ""
		var body []byte
		if tc.method == "POST" || tc.method == "PUT" || tc.method == "PATCH" {
			ct, body = "application/json", hostile
		}
		res := securityCatalogSend(t, tc.method, env.url+tc.path, ct, body)
		if res.status < 400 || res.status >= 500 {
			t.Errorf("%s %s: %.200s, want a client error", tc.method, tc.path, res)
			continue
		}
		securityCatalogRequireCleanRoute(t, tc.method+" "+tc.path, res)
	}
	if got := securityCatalogGet(t, env, "/api/books/"+id); got.status != http.StatusOK || !strings.Contains(string(got.body), `"Alice"`) {
		t.Errorf("the book after the probes: %s, want it untouched", got)
	}
	if l := securityCatalogList(t, env, ""); l.Total != 1 {
		t.Errorf("total = %d after the probes, want 1", l.Total)
	}
}

// securityCatalogRequireCleanRoute is securityCatalogRequireClean for routing errors, which echo
// the method and path (JSON-escaped, bounded by the header limit).
func securityCatalogRequireCleanRoute(t *testing.T, what string, res securityCatalogResp) {
	t.Helper()
	if len(res.body) > 1024 {
		t.Errorf("%s: %d byte response, want a short error", what, len(res.body))
	}
	if ct := res.header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("%s: Content-Type %q, want application/json", what, ct)
	}
	var e struct {
		Error *string `json:"error"`
	}
	if err := json.Unmarshal(res.body, &e); err != nil || e.Error == nil {
		t.Errorf("%s: %.200q is not an {\"error\": ...} body", what, res.body)
	}
	low := strings.ToLower(string(res.body))
	for _, leak := range []string{"goroutine", "panic", ".go:", "github.com", "orch-test/", "mongo", "bson"} {
		if strings.Contains(low, leak) {
			t.Errorf("%s: response leaks %q: %.300s", what, leak, res.body)
		}
	}
}

// --- container files -----------------------------------------------------------------------------

// securityCatalogFile returns the non-comment, non-blank lines of a repository file.
func securityCatalogFile(t *testing.T, rel string) []string {
	t.Helper()
	raw, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		l = strings.TrimRight(l, " \t\r")
		if trimmed := strings.TrimSpace(l); trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lines = append(lines, l)
	}
	return lines
}

func TestSecurityCatalogContainerFilesStayLockedDown(t *testing.T) {
	compose := securityCatalogFile(t, "../../docker-compose.yml")
	dockerfile := securityCatalogFile(t, "../Dockerfile")
	ignore := securityCatalogFile(t, "../.dockerignore")
	joined := strings.Join(compose, "\n")

	t.Run("demo seeding stays off", func(t *testing.T) {
		for _, l := range append(slices.Clone(compose), dockerfile...) {
			if !strings.Contains(l, "SEED_DEMO_DATA") {
				continue
			}
			value := strings.Trim(strings.TrimSpace(l[strings.Index(l, "SEED_DEMO_DATA")+len("SEED_DEMO_DATA"):]), `:="' `)
			if on, err := strconv.ParseBool(value); err != nil || on {
				t.Errorf("%q turns demo seeding on (or cannot be shown to be off); it is opt-in for whoever runs the stack", strings.TrimSpace(l))
			}
		}
	})

	t.Run("published ports are loopback only", func(t *testing.T) {
		portLine := regexp.MustCompile(`^\s*-\s*["']?[0-9.:]+["']?\s*(#.*)?$`)
		found := 0
		for _, l := range compose {
			if !portLine.MatchString(l) {
				continue
			}
			found++
			if !strings.HasPrefix(strings.Trim(strings.TrimSpace(l), `-"' `), "127.0.0.1:") {
				t.Errorf("port mapping %q is not bound to 127.0.0.1", strings.TrimSpace(l))
			}
		}
		if found == 0 {
			t.Error("found no port mapping; the check would not notice a public one")
		}
	})

	t.Run("no secrets in the files", func(t *testing.T) {
		secret := regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|private[_-]?key)\w*\s*[:=]\s*["']?[^\s$"'#]`)
		for _, l := range slices.Concat(compose, dockerfile, ignore) {
			if secret.MatchString(l) {
				t.Errorf("%q looks like a literal credential; read it from the environment", strings.TrimSpace(l))
			}
		}
		for _, l := range compose {
			if strings.Contains(l, "mongodb://") && strings.Contains(l, "@") {
				t.Errorf("%q has credentials in the connection string", strings.TrimSpace(l))
			}
		}
	})

	t.Run("backend container is restricted", func(t *testing.T) {
		for _, want := range []string{"read_only: true", "cap_drop: [ALL]", "no-new-privileges:true", "mem_limit:", "pids_limit:"} {
			if !strings.Contains(joined, want) {
				t.Errorf("docker-compose.yml has no %q for the backend service", want)
			}
		}
		for _, bad := range []string{"privileged: true", "network_mode: host", "pid: host", "/var/run/docker.sock", "cap_add:"} {
			if strings.Contains(joined, bad) {
				t.Errorf("docker-compose.yml contains %q", bad)
			}
		}
	})

	t.Run("image runs as a non-root distroless user", func(t *testing.T) {
		var finalFrom string
		for _, l := range dockerfile {
			if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(l)), "FROM ") {
				finalFrom = strings.TrimSpace(l)
			}
			if u := strings.ToUpper(strings.TrimSpace(l)); strings.HasPrefix(u, "USER ") && (strings.Contains(u, "ROOT") || strings.HasSuffix(u, " 0")) {
				t.Errorf("Dockerfile has %q", strings.TrimSpace(l))
			}
			if u := strings.ToUpper(strings.TrimSpace(l)); strings.HasPrefix(u, "ENV ") || strings.HasPrefix(u, "ARG ") {
				if secret := regexp.MustCompile(`(?i)pass|secret|token|key|seed`); secret.MatchString(l) {
					t.Errorf("Dockerfile sets %q; configuration comes from the environment at run time", strings.TrimSpace(l))
				}
			}
		}
		if !strings.Contains(finalFrom, "distroless") || !strings.Contains(finalFrom, ":nonroot") {
			t.Errorf("final stage is %q, want a distroless :nonroot image", finalFrom)
		}
		if !slices.ContainsFunc(dockerfile, func(l string) bool { return strings.HasPrefix(strings.TrimSpace(l), "HEALTHCHECK") }) {
			t.Error("Dockerfile has no HEALTHCHECK")
		}
	})

	t.Run("build context is an allow-list", func(t *testing.T) {
		if len(ignore) == 0 || strings.TrimSpace(ignore[0]) != "*" {
			t.Fatalf(".dockerignore starts with %q, want * (exclude everything, then allow)", ignore)
		}
		allowed := map[string]bool{"!go.mod": true, "!go.sum": true, "!api/": true, "!cmd/": true, "!internal/": true}
		for _, l := range ignore[1:] {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "!") && !allowed[l] {
				t.Errorf(".dockerignore re-includes %q; only the Go sources belong in the image", l)
			}
		}
	})
}
