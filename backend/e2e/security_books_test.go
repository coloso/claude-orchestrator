package e2e_test

// Security regression tests for the books API (S-02): they pin the secure behavior of the wired
// application (docs/security/S02-02.6-books-implementation.md). Requests are raw net/http, because
// the generated client cannot send malformed input. Every top-level identifier is prefixed with
// "sec". The tests run against the wired application on MongoDB (see e2eMongoConfig).
//
// A test that starts with t.Skip("SECURITY <id>: ...") pins behavior that does not hold yet; the
// follow-up named there removes the skip.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"orch-test/backend/internal/bootstrap"
)

// secEnv is a running application on its own MongoDB database.
type secEnv struct {
	url string
	app *bootstrap.App
	db  *mongo.Database
}

// secStart starts a fresh application on a fresh MongoDB database, with generous rate limits.
func secStart(t *testing.T) secEnv {
	t.Helper()
	cfg, db := e2eMongoConfig(t)
	return secStartConfig(t, cfg, db)
}

func secStartConfig(t *testing.T, cfg bootstrap.Config, db *mongo.Database) secEnv {
	t.Helper()
	env := e2eServe(t, cfg, db)
	return secEnv{url: env.URL, app: env.App, db: env.DB}
}

type secResp struct {
	status int
	header http.Header
	body   []byte
}

func (r secResp) String() string { return fmt.Sprintf("%d %s", r.status, r.body) }

// errorMessage returns the "error" message of an Error response, failing if the body is not one.
func (r secResp) errorMessage(t *testing.T) string {
	t.Helper()
	var e struct {
		Error *string `json:"error"`
	}
	if err := json.Unmarshal(r.body, &e); err != nil || e.Error == nil {
		t.Fatalf("response %q is not an {\"error\": ...} body (%v)", r.body, err)
	}
	return *e.Error
}

var secClient = &http.Client{Timeout: 30 * time.Second}

// secSend sends one request. An empty contentType sends none. A body that is not a bytes.Reader
// is sent with chunked transfer encoding (unknown length).
func secSend(t *testing.T, method, target, contentType string, body io.Reader) secResp {
	t.Helper()
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := secClient.Do(req)
	if err != nil {
		t.Fatalf("%s %.100s: %v", method, target, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %.100s: read body: %v", method, target, err)
	}
	return secResp{status: res.StatusCode, header: res.Header, body: b}
}

func secGet(t *testing.T, env secEnv, path string) secResp {
	t.Helper()
	return secSend(t, http.MethodGet, env.url+path, "", nil)
}

func secPost(t *testing.T, env secEnv, body []byte) secResp {
	t.Helper()
	return secSend(t, http.MethodPost, env.url+"/api/books", "application/json", bytes.NewReader(body))
}

func secPut(t *testing.T, env secEnv, id any, body []byte) secResp {
	t.Helper()
	return secSend(t, http.MethodPut, fmt.Sprintf("%s/api/books/%v", env.url, id), "application/json", bytes.NewReader(body))
}

// secISBN returns a valid ISBN-13 that is distinct for every n.
func secISBN(n int) string {
	digits := fmt.Sprintf("978%09d", n)
	sum := 0
	for i, r := range digits {
		d := int(r - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return digits + fmt.Sprint((10-sum%10)%10)
}

// secJSON builds a book body from valid defaults. Each override is raw JSON text and replaces the
// default; an empty override removes the field. Extra keys are added.
func secJSON(over map[string]string) []byte {
	fields := map[string]string{
		"isbn":    `"` + secISBN(1) + `"`,
		"title":   `"Clean Code"`,
		"authors": `["Robert C. Martin"]`,
	}
	for k, v := range over {
		if v == "" {
			delete(fields, k)
		} else {
			fields[k] = v
		}
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:%s", k, fields[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

type secBook struct {
	ID      int64    `json:"id"`
	Isbn    string   `json:"isbn"`
	Title   string   `json:"title"`
	Authors []string `json:"authors"`
	Version int64    `json:"version"`
}

type secList struct {
	Items []secBook `json:"items"`
	Total int64     `json:"total"`
}

// secCreate adds the book number n and returns it.
func secCreate(t *testing.T, env secEnv, n int, authors ...string) secBook {
	t.Helper()
	a, _ := json.Marshal(authors)
	res := secPost(t, env, secJSON(map[string]string{"isbn": `"` + secISBN(n) + `"`, "authors": string(a)}))
	if res.status != http.StatusCreated {
		t.Fatalf("create book %d: %s", n, res)
	}
	var b secBook
	if err := json.Unmarshal(res.body, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func secListBooks(t *testing.T, env secEnv, query string) secList {
	t.Helper()
	res := secGet(t, env, "/api/books"+query)
	if res.status != http.StatusOK {
		t.Fatalf("GET /api/books%s: %s", query, res)
	}
	var l secList
	if err := json.Unmarshal(res.body, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func secIDs(l secList) []int64 {
	ids := make([]int64, len(l.Items))
	for i, b := range l.Items {
		ids[i] = b.ID
	}
	return ids
}

// secRequireNoBooks fails if the catalog holds any book.
func secRequireNoBooks(t *testing.T, env secEnv) {
	t.Helper()
	if l := secListBooks(t, env, ""); l.Total != 0 || len(l.Items) != 0 {
		t.Fatalf("catalog has %d books (total %d), want none: a rejected request must not store anything", len(l.Items), l.Total)
	}
}

// secLeaks are fragments of library, Go or database internals that must never reach a client.
var secLeaks = []string{
	"strconv", "BookInput", "BookUpdate", "callback", "operation ", "decode", "oas.", "ogen", "jx",
	"goroutine", "panic", "runtime", ".go:", "github.com", "orch-test/",
	"mongo", "E11000", "dup key", "isbn_unique", "collection", "bson", "context deadline", "disconnected",
}

func secRequireNoLeak(t *testing.T, what string, res secResp) {
	t.Helper()
	low := strings.ToLower(string(res.body))
	for _, leak := range secLeaks {
		if strings.Contains(low, strings.ToLower(leak)) {
			t.Errorf("%s: response leaks %q: %.300s", what, leak, res.body)
		}
	}
}

// --- mass assignment and unknown fields ------------------------------------------------------

func TestSecurityBooksUnknownPropertiesAreRejected(t *testing.T) {
	env := secStart(t)
	for _, field := range []string{"id", "version", "createdAt", "updatedAt", "_id", "$set", "foo"} {
		res := secPost(t, env, secJSON(map[string]string{field: `5`}))
		if res.status != http.StatusBadRequest {
			t.Errorf("POST with %q: %s, want 400", field, res)
		}
	}
	res := secPost(t, env, secJSON(map[string]string{"createdAt": `"2000-01-01T00:00:00Z"`, "id": `7`}))
	if res.status != http.StatusBadRequest {
		t.Errorf("POST with createdAt and id: %s, want 400", res)
	}
	secRequireNoBooks(t, env)

	book := secCreate(t, env, 1, "Alice")
	for _, field := range []string{"id", "createdAt", "updatedAt", "_id", "foo"} {
		res := secPut(t, env, book.ID, secJSON(map[string]string{"version": "1", "title": `"Changed"`, field: `5`}))
		if res.status != http.StatusBadRequest {
			t.Errorf("PUT with %q: %s, want 400", field, res)
		}
	}
	got := secGet(t, env, fmt.Sprintf("/api/books/%d", book.ID))
	var b secBook
	if err := json.Unmarshal(got.body, &b); err != nil || b.Title != "Clean Code" || b.Version != 1 {
		t.Errorf("rejected PUTs changed the book: %s", got)
	}
}

// --- request size ----------------------------------------------------------------------------

// secChunked hides the length of r, so the client sends chunked transfer encoding.
type secChunked struct{ io.Reader }

func secHugeAuthors(entries int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"isbn":"` + secISBN(1) + `","title":"x","authors":[`)
	for i := 0; i < entries; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"a"`)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

func TestSecurityBooksOversizedBodiesAreRejectedWith413(t *testing.T) {
	env := secStart(t)
	const limit = 64 << 10
	oversized := secJSON(map[string]string{"title": `"` + strings.Repeat("a", limit) + `"`})

	t.Run("declared length", func(t *testing.T) {
		if res := secPost(t, env, oversized); res.status != http.StatusRequestEntityTooLarge {
			t.Errorf("POST: %s, want 413", res)
		}
		book := secCreate(t, env, 1, "Alice")
		if res := secPut(t, env, book.ID, secJSON(map[string]string{"version": "1", "title": `"` + strings.Repeat("a", limit) + `"`})); res.status != http.StatusRequestEntityTooLarge {
			t.Errorf("PUT: %s, want 413", res)
		}
	})
	t.Run("chunked, length unknown", func(t *testing.T) {
		res := secSend(t, http.MethodPost, env.url+"/api/books", "application/json", secChunked{bytes.NewReader(oversized)})
		if res.status != http.StatusRequestEntityTooLarge {
			t.Errorf("POST: %s, want 413", res)
		}
	})
	t.Run("2 million authors", func(t *testing.T) {
		body := secHugeAuthors(2_000_000)
		if len(body) < 6<<20 {
			t.Fatalf("test body is only %d bytes", len(body))
		}
		for name, r := range map[string]io.Reader{"declared": bytes.NewReader(body), "chunked": secChunked{bytes.NewReader(body)}} {
			res := secSend(t, http.MethodPost, env.url+"/api/books", "application/json", r)
			if res.status != http.StatusRequestEntityTooLarge {
				t.Errorf("%s: %.200s, want 413", name, res)
			}
			if len(res.body) > 200 {
				t.Errorf("%s: 413 response is %d bytes", name, len(res.body))
			}
		}
	})
	t.Run("the largest legal book still fits", func(t *testing.T) {
		authors := make([]string, 20)
		for i := range authors {
			authors[i] = strings.Repeat("é", 200)
		}
		a, _ := json.Marshal(authors)
		body := secJSON(map[string]string{"title": `"` + strings.Repeat("ü", 300) + `"`, "authors": string(a), "isbn": `"` + secISBN(2) + `"`})
		if res := secPost(t, env, body); res.status != http.StatusCreated {
			t.Errorf("max legal book (%d bytes): %.200s, want 201", len(body), res)
		}
	})
}

// --- numbers and identifiers -----------------------------------------------------------------

func TestSecurityBooksNumbersAreBounded(t *testing.T) {
	env := secStart(t)
	book := secCreate(t, env, 1, "Alice")

	for _, v := range []string{"0", "-1", "9223372036854775808", "1.0", "1e2", "null", `"1"`, "true"} {
		res := secPut(t, env, book.ID, secJSON(map[string]string{"version": v}))
		if res.status != http.StatusBadRequest {
			t.Errorf("PUT version %s: %s, want 400", v, res)
		}
	}
	bad := map[string][]string{
		"publishedYear": {"1.5", "1e3", "1.0", "99999999999999999999", "-99999999999999999999", `"2000"`, "true", "[]", "-1", "0", "1449", "2101"},
		"pages":         {"1.5", "1e3", "1.0", "99999999999999999999", "-99999999999999999999", `"20"`, "true", "[]", "-1", "0", "10001"},
	}
	for field, values := range bad {
		for _, v := range values {
			post := secPost(t, env, secJSON(map[string]string{field: v, "isbn": `"` + secISBN(5) + `"`}))
			if post.status != http.StatusBadRequest {
				t.Errorf("POST %s %s: %s, want 400", field, v, post)
			}
			put := secPut(t, env, book.ID, secJSON(map[string]string{field: v, "version": "1"}))
			if put.status != http.StatusBadRequest {
				t.Errorf("PUT %s %s: %s, want 400", field, v, put)
			}
		}
	}
	if got := secListBooks(t, env, ""); got.Total != 1 {
		t.Errorf("total = %d after rejected writes, want 1", got.Total)
	}
}

func TestSecurityBooksPathIDsAreValidated(t *testing.T) {
	env := secStart(t)
	secCreate(t, env, 1, "Alice")
	for _, id := range []string{"0", "-1", "abc", "1.0", "1e2", "%00", "1%2F2", "9223372036854775808", "99999999999999999999"} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			var body io.Reader
			ct := ""
			if method == http.MethodPut {
				body, ct = bytes.NewReader(secJSON(map[string]string{"version": "1"})), "application/json"
			}
			res := secSend(t, method, env.url+"/api/books/"+id, ct, body)
			if res.status != http.StatusBadRequest && res.status != http.StatusNotFound {
				t.Errorf("%s /api/books/%s: %s, want 400 (or 404)", method, id, res)
			}
			if res.status >= 500 {
				t.Errorf("%s /api/books/%s: server error %s", method, id, res)
			}
		}
	}
	for _, id := range []string{"0", "abc", "9223372036854775808"} {
		if res := secGet(t, env, "/api/books/"+id); res.status != http.StatusBadRequest {
			t.Errorf("GET /api/books/%s: %s, want exactly 400", id, res)
		}
	}
	// The largest valid id is a plain miss, not an overflow or a server error.
	if res := secGet(t, env, "/api/books/9223372036854775807"); res.status != http.StatusNotFound {
		t.Errorf("GET max int64 id: %s, want 404", res)
	}
	if l := secListBooks(t, env, ""); l.Total != 1 {
		t.Errorf("total = %d, want 1: a bad id must not delete or change anything", l.Total)
	}
}

func TestSecurityBooksListQueryIsBounded(t *testing.T) {
	env := secStart(t)
	secCreate(t, env, 1, "Alice")
	for _, q := range []string{
		"?offset=100001", "?offset=9223372036854775807", "?offset=9223372036854775808", "?offset=-1", "?offset=1e2", "?offset=",
		"?limit=101", "?limit=0", "?limit=-1", "?limit=abc", "?limit=1&limit=2", "?limit=",
		"?author=" + strings.Repeat("a", 201),
		"?author=" + strings.Repeat("%C3%A9", 201),
	} {
		if res := secGet(t, env, "/api/books"+q); res.status != http.StatusBadRequest {
			t.Errorf("GET /api/books%.60s: %.200s, want 400", q, res)
		}
	}
	for _, q := range []string{
		"?offset=100000", "?limit=100", "?limit=1", "?foo=bar", "?$where=1",
		"?author=" + strings.Repeat("a", 200),
		"?author=" + strings.Repeat("%C3%A9", 200), // 200 characters, 400 bytes
	} {
		if res := secGet(t, env, "/api/books"+q); res.status != http.StatusOK {
			t.Errorf("GET /api/books%.60s: %.200s, want 200", q, res)
		}
	}
}

// --- NoSQL operator injection ----------------------------------------------------------------

func TestSecurityBooksAuthorFilterIsAPlainStringMatch(t *testing.T) {
	env := secStart(t)
	a := secCreate(t, env, 1, "Alice")
	b := secCreate(t, env, 2, "Bob")
	c := secCreate(t, env, 3, "Alice", "Carol")
	all := []int64{a.ID, b.ID, c.ID}

	// The value is compared as a string. Nothing is an operator, a regex or a pattern.
	for _, q := range []string{
		"?author=%24ne", "?author=%24where", "?author=%24gt", "?author=.*", "?author=Ali", "?author=alice", "?author=A.*",
		"?author=" + url.QueryEscape(`{"$ne":"x"}`), "?author=" + url.QueryEscape(`{"$regex":".*"}`),
		"?author=" + url.QueryEscape(`[]`),
	} {
		l := secListBooks(t, env, q)
		if len(l.Items) != 0 || l.Total != 0 {
			t.Errorf("GET /api/books%s matched %v (total %d), want no books", q, secIDs(l), l.Total)
		}
	}

	// Bracket syntax is not parsed into an operator document: the extra parameter is ignored.
	for _, q := range []string{"?author[$ne]=x", "?author[$regex]=.*", "?author[$ne]=Alice", "?author[$in][]=Bob", "?%24ne=x"} {
		if got := secIDs(secListBooks(t, env, q)); !slices.Equal(got, all) {
			t.Errorf("GET /api/books%s = %v, want the unfiltered list %v (parameter ignored)", q, got, all)
		}
	}
	for _, q := range []string{"?author=Alice&author[$ne]=Alice", "?author[$ne]=Alice&author=Alice"} {
		want := []int64{a.ID, c.ID}
		if got := secIDs(secListBooks(t, env, q)); !slices.Equal(got, want) {
			t.Errorf("GET /api/books%s = %v, want %v", q, got, want)
		}
	}

	if l := secListBooks(t, env, "?author=Alice"); l.Total != 2 || !slices.Equal(secIDs(l), []int64{a.ID, c.ID}) {
		t.Errorf("author=Alice = %v (total %d), want [%d %d]", secIDs(l), l.Total, a.ID, c.ID)
	}
	// A repeated author parameter is rejected or takes a single value; it never becomes a list.
	res := secGet(t, env, "/api/books?author=Bob&author=Alice")
	if res.status != http.StatusBadRequest {
		var l secList
		if err := json.Unmarshal(res.body, &l); res.status != http.StatusOK || err != nil || l.Total > 2 || (len(l.Items) == 3) {
			t.Errorf("repeated author: %s", res)
		}
	}
}

func TestSecurityBooksAuthorsWithOperatorLookingNamesAreStoredAsText(t *testing.T) {
	env := secStart(t)
	// A name is data: the book is stored and found by exactly that name, whatever it looks like.
	names := []string{"$ne", `{"$gt":""}`, "$where: sleep(1000)", "a.b", "<script>alert(1)</script>"}
	for i, name := range names {
		secCreate(t, env, i+1, name)
	}
	secCreate(t, env, 100, "Plain")
	for i, name := range names {
		l := secListBooks(t, env, "?author="+url.QueryEscape(name))
		if len(l.Items) != 1 || l.Items[0].Isbn != secISBN(i+1) {
			t.Errorf("author=%q matched %v, want exactly book %d", name, secIDs(l), i+1)
		}
	}
}

// --- Unicode and control characters ----------------------------------------------------------

func TestSecurityBooksRejectInvalidTextWithTheFieldName(t *testing.T) {
	cases := []struct {
		name  string
		field string
		body  []byte
	}{
		{"invalid UTF-8 in title", "title", bytes.Replace(secJSON(map[string]string{"title": `"bad@@"`}), []byte("@@"), []byte{0xff, 0xfe}, 1)},
		{"truncated UTF-8 sequence in title", "title", bytes.Replace(secJSON(map[string]string{"title": `"bad@"`}), []byte("@"), []byte{0xe2, 0x82}, 1)},
		{"invalid UTF-8 in author", "authors", bytes.Replace(secJSON(map[string]string{"authors": `["bad@@"]`}), []byte("@@"), []byte{0xc0, 0xaf}, 1)},
		{"NUL in title", "title", secJSON(map[string]string{"title": `"a\u0000b"`})},
		{"NUL in author", "authors", secJSON(map[string]string{"authors": `["a\u0000b"]`})},
		{"newline in title", "title", secJSON(map[string]string{"title": `"a\nb"`})},
		{"trailing newline in title", "title", secJSON(map[string]string{"title": `"a\n"`})},
		{"tab in author", "authors", secJSON(map[string]string{"authors": `["a\tb"]`})},
		{"DEL in title", "title", secJSON(map[string]string{"title": `"a\u007fb"`})},
		{"C1 control in title", "title", secJSON(map[string]string{"title": `"a\u0085b"`})},
		{"blank title", "title", secJSON(map[string]string{"title": `"   "`})},
		{"301-character title", "title", secJSON(map[string]string{"title": `"` + strings.Repeat("é", 301) + `"`})},
		{"21 authors", "authors", secJSON(map[string]string{"authors": `[` + strings.TrimSuffix(strings.Repeat(`"a",`, 21), ",") + `]`})},
		{"no authors", "authors", secJSON(map[string]string{"authors": `[]`})},
		{"201-character author", "authors", secJSON(map[string]string{"authors": `["` + strings.Repeat("é", 201) + `"]`})},
		{"Arabic-Indic digits in isbn", "isbn", secJSON(map[string]string{"isbn": `"٩٧٨٠١٣٢٣٥٠٨٨٤"`})},
		{"full-width digits in isbn", "isbn", secJSON(map[string]string{"isbn": `"９７８０１３２３５０８８４"`})},
		{"newline in isbn", "isbn", secJSON(map[string]string{"isbn": `"9780132350884\n"`})},
		{"NUL in isbn", "isbn", secJSON(map[string]string{"isbn": `"9780132350884\u0000"`})},
		{"letters in isbn", "isbn", secJSON(map[string]string{"isbn": `"978013235088X"`})},
		{"empty isbn", "isbn", secJSON(map[string]string{"isbn": `""`})},
		{"bad check digit", "isbn", secJSON(map[string]string{"isbn": `"9780132350885"`})},
		{"newline after language", "language", secJSON(map[string]string{"language": `"en\n"`})},
		{"uppercase language", "language", secJSON(map[string]string{"language": `"EN"`})},
		{"three-letter language", "language", secJSON(map[string]string{"language": `"eng"`})},
		{"non-ASCII language", "language", secJSON(map[string]string{"language": `"é"`})},
		{"NUL in language", "language", secJSON(map[string]string{"language": `"e\u0000"`})},
		{"empty language", "language", secJSON(map[string]string{"language": `""`})},
		{"year out of range", "publishedYear", secJSON(map[string]string{"publishedYear": `1449`})},
		{"zero pages", "pages", secJSON(map[string]string{"pages": `0`})},
	}
	env := secStart(t)
	for _, tc := range cases {
		res := secPost(t, env, tc.body)
		if res.status != http.StatusBadRequest {
			t.Errorf("%s: %.200s, want 400", tc.name, res)
			continue
		}
		if msg := res.errorMessage(t); !strings.Contains(msg, tc.field) {
			t.Errorf("%s: message %q does not name %q", tc.name, msg, tc.field)
		}
		secRequireNoLeak(t, tc.name, res)
	}
	secRequireNoBooks(t, env)

	book := secCreate(t, env, 1, "Alice")
	for _, tc := range cases {
		// The PUT body carries the same bad field plus a valid version.
		body := bytes.Replace(tc.body, []byte("{"), []byte(`{"version":1,`), 1)
		res := secPut(t, env, book.ID, body)
		if res.status != http.StatusBadRequest {
			t.Errorf("PUT %s: %.200s, want 400", tc.name, res)
		}
	}
	got := secGet(t, env, fmt.Sprintf("/api/books/%d", book.ID))
	var b secBook
	if err := json.Unmarshal(got.body, &b); err != nil || b.Version != 1 || b.Title != "Clean Code" {
		t.Errorf("rejected PUTs changed the book: %s", got)
	}
}

func TestSecurityBooksOddButLegalTextIsNeverAServerError(t *testing.T) {
	env := secStart(t)
	bodies := map[string][]byte{
		"lone high surrogate escape": secJSON(map[string]string{"title": `"a\ud800b"`}),
		"lone low surrogate escape":  secJSON(map[string]string{"title": `"a\udc00b"`}),
		"emoji and ZWJ":              secJSON(map[string]string{"title": `"👩‍💻 Go"`}),
		"RTL override":               secJSON(map[string]string{"title": `"a‮b"`}),
		"300 astral characters":      secJSON(map[string]string{"title": `"` + strings.Repeat("😀", 300) + `"`}),
		"duplicate keys":             []byte(`{"isbn":"` + secISBN(1) + `","title":"x","title":"y","authors":["a"]}`),
	}
	for name, body := range bodies {
		res := secPost(t, env, body)
		if res.status != http.StatusCreated && res.status != http.StatusBadRequest {
			t.Errorf("%s: %.200s, want 201 or 400", name, res)
		}
		if res.status == http.StatusCreated {
			var b secBook
			if err := json.Unmarshal(res.body, &b); err != nil {
				t.Errorf("%s: %v", name, err)
			}
			// Stored text must be valid UTF-8 whichever way the decoder repaired the input.
			if got := secGet(t, env, fmt.Sprintf("/api/books/%d", b.ID)); !bytes.Equal(bytes.ToValidUTF8(got.body, nil), got.body) {
				t.Errorf("%s: stored text is not valid UTF-8: %q", name, got.body)
			}
		}
		// Free the ISBN for the next case.
		if res.status == http.StatusCreated {
			var b secBook
			_ = json.Unmarshal(res.body, &b)
			secSend(t, http.MethodDelete, fmt.Sprintf("%s/api/books/%d", env.url, b.ID), "", nil)
		}
	}
}

// --- malformed input and error bodies --------------------------------------------------------

func TestSecurityBooksErrorBodiesLeakNoInternals(t *testing.T) {
	env := secStart(t)
	secCreate(t, env, 1, "Alice")
	type req struct {
		name, method, path, ct string
		body                   []byte
	}
	reqs := []req{
		{"not JSON", "POST", "/api/books", "application/json", []byte(`not json`)},
		{"empty body", "POST", "/api/books", "application/json", nil},
		{"array body", "POST", "/api/books", "application/json", []byte(`[]`)},
		{"null body", "POST", "/api/books", "application/json", []byte(`null`)},
		{"truncated JSON", "POST", "/api/books", "application/json", []byte(`{"isbn":"97801`)},
		{"trailing data", "POST", "/api/books", "application/json", append(secJSON(nil), []byte(`{}`)...)},
		{"wrong type isbn", "POST", "/api/books", "application/json", secJSON(map[string]string{"isbn": `5`})},
		{"wrong type authors", "POST", "/api/books", "application/json", secJSON(map[string]string{"authors": `"Alice"`})},
		{"null authors", "POST", "/api/books", "application/json", secJSON(map[string]string{"authors": `null`})},
		{"numbers in authors", "POST", "/api/books", "application/json", secJSON(map[string]string{"authors": `[1,2]`})},
		{"float year", "POST", "/api/books", "application/json", secJSON(map[string]string{"publishedYear": `1.5`})},
		{"huge year", "POST", "/api/books", "application/json", secJSON(map[string]string{"publishedYear": `99999999999999999999`})},
		{"missing isbn", "POST", "/api/books", "application/json", secJSON(map[string]string{"isbn": ""})},
		{"unknown field", "POST", "/api/books", "application/json", secJSON(map[string]string{"id": `5`})},
		{"missing version", "PUT", "/api/books/1", "application/json", secJSON(nil)},
		{"huge version", "PUT", "/api/books/1", "application/json", secJSON(map[string]string{"version": `9223372036854775808`})},
		{"text/plain", "POST", "/api/books", "text/plain", secJSON(nil)},
		{"form", "POST", "/api/books", "application/x-www-form-urlencoded", []byte("isbn=1&title=x")},
		{"multipart", "POST", "/api/books", "multipart/form-data; boundary=x", []byte("--x--")},
		{"no content type", "POST", "/api/books", "", secJSON(nil)},
		{"bad content type", "POST", "/api/books", "application/json; charset", secJSON(nil)},
		{"bad id", "GET", "/api/books/abc", "", nil},
		{"overflow id", "DELETE", "/api/books/9223372036854775808", "", nil},
		{"bad limit", "GET", "/api/books?limit=abc", "", nil},
		{"huge offset", "GET", "/api/books?offset=9223372036854775808", "", nil},
		{"long author", "GET", "/api/books?author=" + strings.Repeat("a", 5000), "", nil},
		{"missing book", "GET", "/api/books/99", "", nil},
		{"unknown route", "GET", "/api/nope", "", nil},
		{"method not allowed", "PATCH", "/api/books/1", "", nil},
		{"too large", "POST", "/api/books", "application/json", secJSON(map[string]string{"title": `"` + strings.Repeat("a", 70000) + `"`})},
	}
	for _, r := range reqs {
		var body io.Reader
		if r.body != nil {
			body = bytes.NewReader(r.body)
		}
		res := secSend(t, r.method, env.url+r.path, r.ct, body)
		if res.status < 400 || res.status >= 500 {
			t.Errorf("%s: %.200s, want a 4xx", r.name, res)
			continue
		}
		if len(res.body) > 512 {
			t.Errorf("%s: error body is %d bytes", r.name, len(res.body))
		}
		if !strings.HasPrefix(res.header.Get("Content-Type"), "application/json") {
			t.Errorf("%s: Content-Type %q", r.name, res.header.Get("Content-Type"))
		}
		res.errorMessage(t)
		// 404 for an unknown route echoes the path; the other bodies must hold no input.
		if r.name != "unknown route" && r.name != "method not allowed" {
			secRequireNoLeak(t, r.name, res)
		}
	}
	// Bad media types never reach the decoder: a cross-site form post cannot create books.
	if l := secListBooks(t, env, ""); l.Total != 1 {
		t.Errorf("total = %d, want 1: rejected requests stored a book", l.Total)
	}
}

func TestSecurityBooksDeeplyNestedUnknownPropertyGetsASmallResponse(t *testing.T) {
	env := secStart(t)
	const maxSmall = 512
	cases := map[string][]byte{
		"200k nested arrays (over the body limit)": []byte(`{"isbn":"` + secISBN(1) + `","title":"x","authors":["a"],"extra":` + strings.Repeat("[", 200_000) + strings.Repeat("]", 200_000) + `}`),
		"200k unclosed arrays":                     []byte(`{"isbn":"` + secISBN(1) + `","title":"x","authors":["a"],"extra":` + strings.Repeat("[", 200_000)),
		"30k nested arrays (under the limit)":      []byte(`{"isbn":"` + secISBN(1) + `","title":"x","authors":["a"],"extra":` + strings.Repeat("[", 30_000) + strings.Repeat("]", 30_000) + `}`),
		"30k unclosed arrays (under the limit)":    []byte(`{"isbn":"` + secISBN(1) + `","title":"x","authors":["a"],"extra":` + strings.Repeat("[", 30_000)),
		"30k nested objects (under the limit)":     []byte(`{"isbn":"` + secISBN(1) + `","title":"x","authors":["a"],"extra":` + strings.Repeat(`{"a":`, 6_000) + `1` + strings.Repeat("}", 6_000) + `}`),
		"30k nested arrays in a known field":       []byte(`{"isbn":"` + secISBN(1) + `","title":"x","authors":` + strings.Repeat("[", 30_000) + `}`),
		"deep array as the whole body":             []byte(strings.Repeat("[", 60_000)),
	}
	for name, body := range cases {
		start := time.Now()
		res := secPost(t, env, body)
		if res.status != http.StatusBadRequest && res.status != http.StatusRequestEntityTooLarge {
			t.Errorf("%s: %.200s, want 400 or 413", name, res)
		}
		if len(res.body) > maxSmall {
			t.Errorf("%s: response is %d bytes for a %d byte request, want at most %d", name, len(res.body), len(body), maxSmall)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("%s: took %v", name, d)
		}
	}
	secRequireNoBooks(t, env)
}

// --- headers ---------------------------------------------------------------------------------

func TestSecurityBooksResponsesCarrySecurityHeaders(t *testing.T) {
	env := secStart(t)
	book := secCreate(t, env, 1, "Alice")
	bookPath := fmt.Sprintf("/api/books/%d", book.ID)
	type req struct {
		name, method, path, ct string
		body                   []byte
		want                   int
	}
	reqs := []req{
		{"200 list", "GET", "/api/books", "", nil, 200},
		{"200 get", "GET", bookPath, "", nil, 200},
		{"200 health", "GET", "/api/health", "", nil, 200},
		{"200 update", "PUT", bookPath, "application/json", secJSON(map[string]string{"version": "1"}), 200},
		{"201 create", "POST", "/api/books", "application/json", secJSON(map[string]string{"isbn": `"` + secISBN(2) + `"`}), 201},
		{"400 bad body", "POST", "/api/books", "application/json", []byte(`{`), 400},
		{"400 bad field", "POST", "/api/books", "application/json", secJSON(map[string]string{"title": `""`}), 400},
		{"400 bad id", "GET", "/api/books/abc", "", nil, 400},
		{"400 bad query", "GET", "/api/books?limit=101", "", nil, 400},
		{"404 missing book", "GET", "/api/books/99999", "", nil, 404},
		{"404 unknown route", "GET", "/api/nope", "", nil, 404},
		{"405 wrong method", "PATCH", bookPath, "", nil, 405},
		{"409 duplicate isbn", "POST", "/api/books", "application/json", secJSON(map[string]string{"isbn": `"` + secISBN(2) + `"`}), 409},
		{"413 too large", "POST", "/api/books", "application/json", secJSON(map[string]string{"title": `"` + strings.Repeat("a", 70000) + `"`}), 413},
		{"415 wrong type", "POST", "/api/books", "text/plain", []byte("x"), 415},
		{"204 delete", "DELETE", bookPath, "", nil, 204},
	}
	for _, r := range reqs {
		var body io.Reader
		if r.body != nil {
			body = bytes.NewReader(r.body)
		}
		res := secSend(t, r.method, env.url+r.path, r.ct, body)
		if res.status != r.want {
			t.Errorf("%s: %.200s, want %d", r.name, res, r.want)
		}
		for header, want := range map[string]string{
			"X-Content-Type-Options":  "nosniff",
			"Cache-Control":           "no-store",
			"Content-Security-Policy": "default-src 'none'",
		} {
			if got := res.header.Get(header); got != want {
				t.Errorf("%s: %s = %q, want %q", r.name, header, got, want)
			}
		}
		for h := range res.header {
			if strings.HasPrefix(strings.ToLower(h), "access-control-") {
				t.Errorf("%s: unexpected CORS header %s: no origin is allowed to read this API", r.name, h)
			}
		}
		if res.header.Get("Server") != "" || res.header.Get("X-Powered-By") != "" {
			t.Errorf("%s: response names the server software: %v", r.name, res.header)
		}
	}
}

func TestSecurityBooksCrossOriginRequestsAreNotAllowed(t *testing.T) {
	env := secStart(t)
	for _, method := range []string{http.MethodGet, http.MethodOptions, http.MethodPost, http.MethodDelete} {
		req, err := http.NewRequest(method, env.url+"/api/books/1", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Access-Control-Request-Method", "DELETE")
		res, err := secClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if v := res.Header.Get("Access-Control-Allow-Origin"); v != "" {
			t.Errorf("%s with a foreign Origin: Access-Control-Allow-Origin = %q, want none", method, v)
		}
	}
}

// --- conflicts and unmapped errors -----------------------------------------------------------

func TestSecurityBooksDuplicateISBNIsA409WithoutDriverText(t *testing.T) {
	env := secStart(t)
	secCreate(t, env, 1, "Alice")
	second := secCreate(t, env, 2, "Bob")

	dup := secPost(t, env, secJSON(map[string]string{"isbn": `"` + secISBN(1) + `"`, "title": `"Other"`}))
	if dup.status != http.StatusConflict {
		t.Fatalf("POST duplicate isbn: %s, want 409", dup)
	}
	// The same ISBN written differently is the same book.
	hyphenated := secISBN(1)[:3] + "-" + secISBN(1)[3:]
	if res := secPost(t, env, secJSON(map[string]string{"isbn": `"` + hyphenated + `"`})); res.status != http.StatusConflict {
		t.Errorf("POST hyphenated duplicate isbn: %s, want 409", res)
	}
	onto := secPut(t, env, second.ID, secJSON(map[string]string{"isbn": `"` + secISBN(1) + `"`, "version": "1"}))
	if onto.status != http.StatusConflict {
		t.Errorf("PUT onto another book's isbn: %s, want 409", onto)
	}
	for name, res := range map[string]secResp{"POST": dup, "PUT": onto} {
		if got := res.errorMessage(t); got != "a book with this ISBN already exists" {
			t.Errorf("%s conflict message = %q, want the fixed domain message", name, got)
		}
		secRequireNoLeak(t, name+" conflict", res)
	}

	// Many racing creates of one ISBN: exactly one wins, the others are clean 409s.
	const racers = 12
	var created, conflicts, other atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, env.url+"/api/books", bytes.NewReader(secJSON(map[string]string{"isbn": `"` + secISBN(50) + `"`})))
			req.Header.Set("Content-Type", "application/json")
			res, err := secClient.Do(req)
			if err != nil {
				other.Add(1)
				return
			}
			defer res.Body.Close()
			b, _ := io.ReadAll(res.Body)
			switch res.StatusCode {
			case http.StatusCreated:
				created.Add(1)
			case http.StatusConflict:
				conflicts.Add(1)
				if strings.Contains(strings.ToLower(string(b)), "e11000") || strings.Contains(string(b), "dup key") {
					t.Errorf("racing conflict leaks driver text: %s", b)
				}
			default:
				other.Add(1)
				t.Errorf("racing create: %d %s", res.StatusCode, b)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 || conflicts.Load() != racers-1 || other.Load() != 0 {
		t.Errorf("racing creates: %d created, %d conflicts, %d other; want 1, %d, 0", created.Load(), conflicts.Load(), other.Load(), racers-1)
	}
	if l := secListBooks(t, env, ""); l.Total != 3 {
		t.Errorf("total = %d, want 3", l.Total)
	}
}

func TestSecurityBooksUnmappedRepositoryErrorsBecomeAGeneric500(t *testing.T) {
	env := secStart(t)
	book := secCreate(t, env, 1, "Alice")

	// Disconnecting the client makes every repository call fail with a driver error that no
	// adapter maps.
	if err := env.app.Close(context.Background()); err != nil {
		t.Fatalf("close app: %v", err)
	}
	const wantBody = `{"error":"internal server error"}`
	for _, r := range []struct {
		name, method, path string
		body               []byte
	}{
		{"get", "GET", fmt.Sprintf("/api/books/%d", book.ID), nil},
		{"list", "GET", "/api/books", nil},
		{"list by author", "GET", "/api/books?author=Alice", nil},
		{"create", "POST", "/api/books", secJSON(map[string]string{"isbn": `"` + secISBN(2) + `"`})},
		{"update", "PUT", fmt.Sprintf("/api/books/%d", book.ID), secJSON(map[string]string{"version": "1"})},
		{"delete", "DELETE", fmt.Sprintf("/api/books/%d", book.ID), nil},
	} {
		var body io.Reader
		ct := ""
		if r.body != nil {
			body, ct = bytes.NewReader(r.body), "application/json"
		}
		res := secSend(t, r.method, env.url+r.path, ct, body)
		if res.status != http.StatusInternalServerError {
			t.Errorf("%s with a failing repository: %.200s, want 500", r.name, res)
			continue
		}
		if got := strings.TrimSpace(string(res.body)); got != wantBody {
			t.Errorf("%s: body %q, want %q", r.name, got, wantBody)
		}
		secRequireNoLeak(t, r.name, res)
	}
}

// --- logging ---------------------------------------------------------------------------------

// secLogBuffer is a bytes.Buffer that a server goroutine and the test can use at the same time.
type secLogBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *secLogBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *secLogBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// No t.Parallel: the test replaces the process-wide default logger.
func TestSecurityBooksRequestBodiesAreNeverLogged(t *testing.T) {
	var logs secLogBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	const marker = "SECRET-MARKER-7f3a9c"
	quoted := `"` + marker + `"`
	env := secStart(t)
	bodies := map[string][]byte{
		"wrong type next to the secret": secJSON(map[string]string{"title": quoted, "publishedYear": quoted}),
		"float next to the secret":      secJSON(map[string]string{"title": quoted, "publishedYear": `1.5`}),
		"unknown field":                 secJSON(map[string]string{"title": quoted, "id": quoted}),
		"trailing garbage":              append(secJSON(map[string]string{"title": quoted}), []byte(marker)...),
		"truncated":                     []byte(`{"isbn":"` + secISBN(1) + `","title":"` + marker),
		"not JSON":                      []byte(marker),
		"control character":             secJSON(map[string]string{"title": `"` + marker + `\u0000"`}),
		"bad author":                    secJSON(map[string]string{"authors": `["` + marker + `\n"]`}),
		"bad isbn":                      secJSON(map[string]string{"isbn": quoted}),
		"oversized":                     secJSON(map[string]string{"title": `"` + marker + strings.Repeat("a", 70000) + `"`}),
		"valid":                         secJSON(map[string]string{"title": quoted, "isbn": `"` + secISBN(9) + `"`}),
	}
	for _, body := range bodies {
		secPost(t, env, body)
		secPut(t, env, 1, bytes.Replace(body, []byte("{"), []byte(`{"version":1,`), 1))
	}
	if !strings.Contains(logs.String(), "transport error") {
		t.Errorf("the log capture saw no transport error; the test would not notice a leak: %q", logs.String())
	}
	if strings.Contains(logs.String(), marker) {
		i := strings.Index(logs.String(), marker)
		from := max(0, i-300)
		t.Errorf("a request body value reached the logs: ...%s...", logs.String()[from:min(len(logs.String()), i+100)])
	}
}

// --- seeding and health ----------------------------------------------------------------------

func TestSecurityBooksSeedingIsNotReachableFromTheAPI(t *testing.T) {
	env := secStart(t)
	secRequireNoBooks(t, env) // SEED_DEMO_DATA is off by default
	for _, path := range []string{
		"/api/seed", "/api/books/seed", "/api/books/seed-demo", "/api/demo", "/api/admin/seed", "/api/books/demo",
		"/api/books:seed", "/api/seed-demo-data", "/seed", "/api/books?seed=true", "/api/books?SEED_DEMO_DATA=true",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
			var body io.Reader
			ct := ""
			if method != http.MethodGet {
				body, ct = strings.NewReader(`{}`), "application/json"
			}
			res := secSend(t, method, env.url+path, ct, body)
			if res.status >= 500 {
				t.Errorf("%s %s: %s", method, path, res)
			}
			if method != http.MethodGet && res.status < 400 {
				t.Errorf("%s %s: %s, want an error", method, path, res)
			}
		}
	}
	secRequireNoBooks(t, env)
}

// --- MongoDB operation timeouts --------------------------------------------------------------

// secFreezableProxy forwards TCP to a MongoDB server until freeze is called; after that it keeps
// every connection open but forwards nothing, like a network partition without a reset.
type secFreezableProxy struct {
	ln     net.Listener
	target string
	frozen atomic.Bool
	mu     sync.Mutex
	conns  []net.Conn
}

func secNewFreezableProxy(t *testing.T, target string) *secFreezableProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &secFreezableProxy{ln: ln, target: target}
	t.Cleanup(p.close)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(c)
		}
	}()
	return p
}

func (p *secFreezableProxy) track(c net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.conns = append(p.conns, c)
}

func (p *secFreezableProxy) close() {
	_ = p.ln.Close()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.conns {
		_ = c.Close()
	}
}

func (p *secFreezableProxy) freeze() { p.frozen.Store(true) }

func (p *secFreezableProxy) serve(client net.Conn) {
	p.track(client)
	if p.frozen.Load() {
		return // accepted and ignored
	}
	upstream, err := net.Dial("tcp", p.target)
	if err != nil {
		_ = client.Close()
		return
	}
	p.track(upstream)
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 && !p.frozen.Load() {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go pipe(upstream, client)
	pipe(client, upstream)
}

// secMongoHost returns the host (and port) of a MongoDB URI. Its errors never quote the URI,
// because url.Parse's own error does, and the URI may carry credentials (SEC-S03-3).
func secMongoHost(uri string) (string, error) {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Host == "" {
		return "", errors.New("cannot find the host of MONGO_TEST_URI (the value is not shown: it may carry credentials)")
	}
	return parsed.Host, nil
}

func TestSecurityBooksMongoOperationsHaveATimeout(t *testing.T) {
	host, err := secMongoHost(e2eMongoURI(t))
	if err != nil {
		t.Fatal(err)
	}
	proxy := secNewFreezableProxy(t, host)

	cfg, db := e2eMongoConfig(t)
	cfg.MongoURI = "mongodb://" + proxy.ln.Addr().String() + "/?directConnection=true"
	env := secStartConfig(t, cfg, db)
	book := secCreate(t, env, 1, "Alice")

	// The database stops answering, but the connections stay open. Each request must come back
	// with an error within a few seconds instead of holding its goroutine and connection.
	proxy.freeze()
	const deadline = 10 * time.Second
	client := &http.Client{Timeout: deadline + 5*time.Second}
	for _, r := range []struct{ name, method, path string }{
		{"get", "GET", fmt.Sprintf("/api/books/%d", book.ID)},
		{"list", "GET", "/api/books"},
		{"delete", "DELETE", fmt.Sprintf("/api/books/%d", book.ID)},
	} {
		start := time.Now()
		req, _ := http.NewRequest(r.method, env.url+r.path, nil)
		res, err := client.Do(req)
		if err != nil {
			t.Errorf("%s: no answer after %v while MongoDB is unreachable: %v", r.name, time.Since(start).Round(time.Second), err)
			continue
		}
		res.Body.Close()
		if d := time.Since(start); d > deadline {
			t.Errorf("%s: answered after %v, want less than %v", r.name, d.Round(time.Second), deadline)
		}
		if res.StatusCode < 500 {
			t.Errorf("%s: status %d while MongoDB is unreachable, want a 5xx", r.name, res.StatusCode)
		}
	}
}
