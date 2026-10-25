package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"orch-test/backend/internal/oas"
)

// crudServer starts a fresh, fully wired server on its own MongoDB test database and returns a
// generated client and its base URL.
func crudServer(t *testing.T) (*oas.Client, string) {
	t.Helper()
	env := e2eStart(t)
	return env.Client, env.URL
}

// crudISBNWithPrefix returns a 13-digit ISBN made of prefix, 9 digits from n and the check digit.
func crudISBNWithPrefix(prefix string, n int) string {
	digits := fmt.Sprintf("%s%09d", prefix, n)
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

// crudISBN returns a valid, normalized ISBN-13 that is distinct for every n.
func crudISBN(n int) string { return crudISBNWithPrefix("978", n) }

// crudBook returns a complete, valid book input with the ISBN derived from n.
func crudBook(n int) *oas.BookInput {
	return &oas.BookInput{
		Isbn:          crudISBN(n),
		Title:         fmt.Sprintf("Book %d", n),
		Authors:       []string{"Ada Author", "Bob Writer"},
		PublishedYear: oas.NewOptInt(2008),
		Language:      oas.NewOptString("en"),
		Pages:         oas.NewOptInt(464),
	}
}

func crudUpdateFrom(in *oas.BookInput, version int64) *oas.BookUpdate {
	return &oas.BookUpdate{
		Isbn:          in.Isbn,
		Title:         in.Title,
		Authors:       in.Authors,
		PublishedYear: in.PublishedYear,
		Language:      in.Language,
		Pages:         in.Pages,
		Version:       version,
	}
}

func crudCreate(t *testing.T, c *oas.Client, in *oas.BookInput) *oas.Book {
	t.Helper()
	book, err := c.CreateBook(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateBook(%q): %v", in.Isbn, err)
	}
	return book
}

func crudGet(t *testing.T, c *oas.Client, id int64) *oas.Book {
	t.Helper()
	book, err := c.GetBook(context.Background(), oas.GetBookParams{ID: id})
	if err != nil {
		t.Fatalf("GetBook(%d): %v", id, err)
	}
	return book
}

func crudList(t *testing.T, c *oas.Client, params oas.ListBooksParams) *oas.BookList {
	t.Helper()
	list, err := c.ListBooks(context.Background(), params)
	if err != nil {
		t.Fatalf("ListBooks(%+v): %v", params, err)
	}
	return list
}

func crudTotal(t *testing.T, c *oas.Client) int64 {
	t.Helper()
	return crudList(t, c, oas.ListBooksParams{}).Total
}

// crudRequireError asserts that err is an API error with the given status and returns its message.
func crudRequireError(t *testing.T, err error, status int) string {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error with status %d, got success", status)
	}
	var apiErr *oas.ErrorStatusCode
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *oas.ErrorStatusCode, got %T: %v", err, err)
	}
	if apiErr.StatusCode != status {
		t.Fatalf("status = %d (%q), want %d", apiErr.StatusCode, apiErr.Response.Error, status)
	}
	if apiErr.Response.Error == "" {
		t.Fatalf("status %d has an empty error message", status)
	}
	return apiErr.Response.Error
}

func crudRequireSameBook(t *testing.T, got, want *oas.Book) {
	t.Helper()
	if got.ID != want.ID || got.Isbn != want.Isbn || got.Title != want.Title || got.Version != want.Version {
		t.Errorf("book = %+v, want %+v", got, want)
	}
	if strings.Join(got.Authors, "|") != strings.Join(want.Authors, "|") {
		t.Errorf("authors = %v, want %v", got.Authors, want.Authors)
	}
	if got.PublishedYear != want.PublishedYear || got.Language != want.Language || got.Pages != want.Pages {
		t.Errorf("optional fields = (%+v, %+v, %+v), want (%+v, %+v, %+v)",
			got.PublishedYear, got.Language, got.Pages, want.PublishedYear, want.Language, want.Pages)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("createdAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("updatedAt = %v, want %v", got.UpdatedAt, want.UpdatedAt)
	}
}

// crudRaw sends a raw request and returns the status and body.
func crudRaw(t *testing.T, method, url string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, data
}

// crudInvalidCase is an invalid book input and the API name of the offending field.
type crudInvalidCase struct {
	name   string
	field  string
	mutate func(in *oas.BookInput)
}

func crudInvalidCases() []crudInvalidCase {
	validISBN := crudISBN(1)
	return []crudInvalidCase{
		{"isbn with wrong check digit", "isbn", func(in *oas.BookInput) {
			last := validISBN[12]
			in.Isbn = validISBN[:12] + string('0'+(last-'0'+1)%10)
		}},
		{"isbn with 12 digits", "isbn", func(in *oas.BookInput) { in.Isbn = validISBN[:12] }},
		{"isbn with 14 digits", "isbn", func(in *oas.BookInput) { in.Isbn = validISBN + "0" }},
		{"isbn with letters", "isbn", func(in *oas.BookInput) { in.Isbn = "97801323508AB" }},
		{"isbn starting with 977", "isbn", func(in *oas.BookInput) { in.Isbn = crudISBNWithPrefix("977", 1) }},
		{"empty title", "title", func(in *oas.BookInput) { in.Title = "" }},
		{"whitespace-only title", "title", func(in *oas.BookInput) { in.Title = " \t  " }},
		{"301-character title", "title", func(in *oas.BookInput) { in.Title = strings.Repeat("t", 301) }},
		{"no authors", "authors", func(in *oas.BookInput) { in.Authors = []string{} }},
		{"21 authors", "authors", func(in *oas.BookInput) {
			in.Authors = nil
			for i := range 21 {
				in.Authors = append(in.Authors, fmt.Sprintf("Author %d", i))
			}
		}},
		{"empty author name", "authors", func(in *oas.BookInput) { in.Authors = []string{"Ada Author", ""} }},
		{"whitespace-only author name", "authors", func(in *oas.BookInput) { in.Authors = []string{"   "} }},
		{"201-character author name", "authors", func(in *oas.BookInput) { in.Authors = []string{strings.Repeat("a", 201)} }},
		{"year 1449", "publishedYear", func(in *oas.BookInput) { in.PublishedYear = oas.NewOptInt(1449) }},
		{"year 2101", "publishedYear", func(in *oas.BookInput) { in.PublishedYear = oas.NewOptInt(2101) }},
		{"pages 0", "pages", func(in *oas.BookInput) { in.Pages = oas.NewOptInt(0) }},
		{"pages 10001", "pages", func(in *oas.BookInput) { in.Pages = oas.NewOptInt(10001) }},
		{"language EN", "language", func(in *oas.BookInput) { in.Language = oas.NewOptString("EN") }},
		{"language eng", "language", func(in *oas.BookInput) { in.Language = oas.NewOptString("eng") }},
	}
}

// --- Create ---

// Given an empty catalog, when I create a complete, valid book, then I get 201 with `id`,
// `version` 1 and `createdAt` = `updatedAt`, and `GET /api/books/{id}` returns the same book.
func TestCrudCreateCompleteBook(t *testing.T) {
	c, base := crudServer(t)
	if total := crudTotal(t, c); total != 0 {
		t.Fatalf("catalog not empty: total = %d", total)
	}
	in := crudBook(1)
	in.Authors = []string{"Robert C. Martin"}

	// Raw request to check the 201 status.
	status, body := crudRaw(t, http.MethodPost, base+"/api/books", in)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", status, body)
	}
	var created oas.Book
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}

	if created.ID < 1 {
		t.Errorf("id = %d, want a positive id", created.ID)
	}
	if created.Version != 1 {
		t.Errorf("version = %d, want 1", created.Version)
	}
	if created.CreatedAt.IsZero() || !created.CreatedAt.Equal(created.UpdatedAt) {
		t.Errorf("createdAt = %v, updatedAt = %v, want equal and non-zero", created.CreatedAt, created.UpdatedAt)
	}
	if created.Isbn != in.Isbn || created.Title != in.Title || strings.Join(created.Authors, "|") != "Robert C. Martin" {
		t.Errorf("created = %+v, want the submitted values", created)
	}
	if created.PublishedYear != in.PublishedYear || created.Language != in.Language || created.Pages != in.Pages {
		t.Errorf("optional fields = (%+v, %+v, %+v), want %+v, %+v, %+v",
			created.PublishedYear, created.Language, created.Pages, in.PublishedYear, in.Language, in.Pages)
	}

	crudRequireSameBook(t, crudGet(t, c, created.ID), &created)
	if total := crudTotal(t, c); total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
}

// Given a book with only the required fields, when I create it, then I get 201 and the
// optional fields are absent from the response.
func TestCrudCreateBookWithOnlyRequiredFields(t *testing.T) {
	c, base := crudServer(t)
	in := &oas.BookInput{Isbn: crudISBN(2), Title: "Minimal", Authors: []string{"Solo Author"}}

	status, body := crudRaw(t, http.MethodPost, base+"/api/books", in)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", status, body)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"publishedYear", "language", "pages"} {
		if v, ok := fields[name]; ok {
			t.Errorf("optional field %s present in create response: %s", name, v)
		}
	}
	var created oas.Book
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}

	got := crudGet(t, c, created.ID)
	if got.PublishedYear.Set || got.Language.Set || got.Pages.Set {
		t.Errorf("optional fields set after get: %+v %+v %+v", got.PublishedYear, got.Language, got.Pages)
	}
	_, raw := crudRaw(t, http.MethodGet, fmt.Sprintf("%s/api/books/%d", base, created.ID), nil)
	fields = nil
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"publishedYear", "language", "pages"} {
		if v, ok := fields[name]; ok {
			t.Errorf("optional field %s present in get response: %s", name, v)
		}
	}
}

// The boundary values of the field rules are accepted.
func TestCrudCreateBookAtFieldLimits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(in *oas.BookInput)
	}{
		{"300-character title", func(in *oas.BookInput) { in.Title = strings.Repeat("t", 300) }},
		{"title is trimmed before counting", func(in *oas.BookInput) { in.Title = "  " + strings.Repeat("t", 300) + "  " }},
		{"20 authors", func(in *oas.BookInput) {
			in.Authors = nil
			for i := range 20 {
				in.Authors = append(in.Authors, fmt.Sprintf("Author %d", i))
			}
		}},
		{"200-character author name", func(in *oas.BookInput) { in.Authors = []string{strings.Repeat("a", 200)} }},
		{"year 1450", func(in *oas.BookInput) { in.PublishedYear = oas.NewOptInt(1450) }},
		{"year 2100", func(in *oas.BookInput) { in.PublishedYear = oas.NewOptInt(2100) }},
		{"pages 1", func(in *oas.BookInput) { in.Pages = oas.NewOptInt(1) }},
		{"pages 10000", func(in *oas.BookInput) { in.Pages = oas.NewOptInt(10000) }},
		{"prefix 979", func(in *oas.BookInput) { in.Isbn = crudISBNWithPrefix("979", 7) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := crudServer(t)
			in := crudBook(1)
			tt.mutate(in)
			book := crudCreate(t, c, in)
			if book.Isbn != strings.NewReplacer("-", "", " ", "").Replace(in.Isbn) {
				t.Errorf("isbn = %q", book.Isbn)
			}
		})
	}
}

// Given the ISBN `978-0-13-235088-4`, when I create a book, then it is stored and returned
// as `9780132350884`.
func TestCrudCreateNormalizesTheISBN(t *testing.T) {
	for _, spelled := range []string{"978-0-13-235088-4", "978 0 13 235088 4"} {
		t.Run(spelled, func(t *testing.T) {
			c, _ := crudServer(t)
			in := crudBook(1)
			in.Isbn = spelled

			created := crudCreate(t, c, in)
			if created.Isbn != "9780132350884" {
				t.Errorf("created isbn = %q, want 9780132350884", created.Isbn)
			}
			if got := crudGet(t, c, created.ID); got.Isbn != "9780132350884" {
				t.Errorf("stored isbn = %q, want 9780132350884", got.Isbn)
			}
			if list := crudList(t, c, oas.ListBooksParams{}); len(list.Items) != 1 || list.Items[0].Isbn != "9780132350884" {
				t.Errorf("listed = %+v, want one book with isbn 9780132350884", list.Items)
			}
		})
	}
}

// Given a book whose ISBN is already in the catalog, also in a different spelling (with
// hyphens), when I create it, then I get 409 and the catalog is unchanged.
func TestCrudCreateRejectsADuplicateISBN(t *testing.T) {
	tests := []struct {
		name string
		isbn string
	}{
		{"same spelling", "9780132350884"},
		{"hyphenated spelling", "978-0-13-235088-4"},
		{"spaced spelling", "978 0132 350884"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := crudServer(t)
			in := crudBook(1)
			in.Isbn = "9780132350884"
			existing := crudCreate(t, c, in)

			dup := crudBook(2)
			dup.Isbn = tt.isbn
			_, err := c.CreateBook(context.Background(), dup)
			crudRequireError(t, err, http.StatusConflict)

			if total := crudTotal(t, c); total != 1 {
				t.Errorf("total = %d, want 1", total)
			}
			crudRequireSameBook(t, crudGet(t, c, existing.ID), existing)
		})
	}
}

// Given an ISBN with a wrong check digit, 12 or 14 digits, letters, or starting with `977`,
// when I create a book, then I get 400 with a message naming `isbn`.
//
// Given an empty or whitespace-only title, a 301-character title, no authors, 21 authors,
// an empty author name, year 1449 or 2101, pages 0 or 10001, or language `EN` or `eng`,
// when I create a book, then I get 400 with a message naming the field.
func TestCrudCreateRejectsInvalidFields(t *testing.T) {
	for _, tt := range crudInvalidCases() {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := crudServer(t)
			in := crudBook(1)
			tt.mutate(in)

			_, err := c.CreateBook(context.Background(), in)
			msg := crudRequireError(t, err, http.StatusBadRequest)
			if !strings.Contains(msg, tt.field) {
				t.Errorf("message %q does not name field %q", msg, tt.field)
			}
			if total := crudTotal(t, c); total != 0 {
				t.Errorf("total = %d after rejected create, want 0", total)
			}
		})
	}
}

// --- Read and list ---

// Given no book with ID 999, when I get it, then I get 404.
func TestCrudGettingAnUnknownBook(t *testing.T) {
	c, base := crudServer(t)
	_, err := c.GetBook(context.Background(), oas.GetBookParams{ID: 999})
	crudRequireError(t, err, http.StatusNotFound)

	status, body := crudRaw(t, http.MethodGet, base+"/api/books/999", nil)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	var errBody map[string]any
	if err := json.Unmarshal(body, &errBody); err != nil {
		t.Fatalf("error body %s is not JSON: %v", body, err)
	}
	if msg, ok := errBody["error"].(string); !ok || msg == "" || len(errBody) != 1 {
		t.Errorf("error body = %s, want {\"error\": \"<message>\"}", body)
	}
}

// Given 25 books, when I list without parameters, then I get the first 20 by ID and `total`
// 25; with `offset=20` I get the remaining 5.
func TestCrudListingPagesThroughTheCatalogByID(t *testing.T) {
	c, _ := crudServer(t)
	created := make([]*oas.Book, 25)
	for i := range created {
		created[i] = crudCreate(t, c, crudBook(i+1))
		if i > 0 && created[i].ID <= created[i-1].ID {
			t.Fatalf("ids do not ascend in creation order: %d then %d", created[i-1].ID, created[i].ID)
		}
	}

	ids := func(items []oas.Book) []int64 {
		out := make([]int64, len(items))
		for i, b := range items {
			out[i] = b.ID
		}
		return out
	}
	expectIDs := func(from, to int) []int64 {
		out := make([]int64, 0, to-from)
		for _, b := range created[from:to] {
			out = append(out, b.ID)
		}
		return out
	}

	tests := []struct {
		name         string
		params       oas.ListBooksParams
		wantFrom, to int
	}{
		{"defaults", oas.ListBooksParams{}, 0, 20},
		{"explicit defaults", oas.ListBooksParams{Limit: oas.NewOptInt(20), Offset: oas.NewOptInt(0)}, 0, 20},
		{"offset 20", oas.ListBooksParams{Offset: oas.NewOptInt(20)}, 20, 25},
		{"limit 5", oas.ListBooksParams{Limit: oas.NewOptInt(5)}, 0, 5},
		{"limit 5 offset 10", oas.ListBooksParams{Limit: oas.NewOptInt(5), Offset: oas.NewOptInt(10)}, 10, 15},
		{"limit 100", oas.ListBooksParams{Limit: oas.NewOptInt(100)}, 0, 25},
		{"limit 1", oas.ListBooksParams{Limit: oas.NewOptInt(1)}, 0, 1},
		{"offset past the end", oas.ListBooksParams{Offset: oas.NewOptInt(25)}, 25, 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list := crudList(t, c, tt.params)
			if list.Total != 25 {
				t.Errorf("total = %d, want 25", list.Total)
			}
			got, want := ids(list.Items), expectIDs(tt.wantFrom, tt.to)
			if fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("ids = %v, want %v", got, want)
			}
		})
	}
}

// Given books by several authors, when I list with `author=Martin Fowler`, then I get only
// books that have exactly that author, and `total` counts only those.
func TestCrudListingFiltersByExactAuthor(t *testing.T) {
	c, _ := crudServer(t)
	authorsByBook := [][]string{
		{"Martin Fowler"},                      // match
		{"Kent Beck"},                          // other author
		{"martin fowler"},                      // near miss: case
		{"Martin Fowler Jr."},                  // near miss: suffix
		{"Kent Beck", "Martin Fowler", "Ward"}, // match: one of several authors
		{"Martin"},                             // near miss: prefix
		{"Martin  Fowler"},                     // near miss: inner whitespace
		{"Martin Fowler", "Kent Beck"},         // match
	}
	var wantIDs []int64
	for i, authors := range authorsByBook {
		in := crudBook(i + 1)
		in.Authors = authors
		book := crudCreate(t, c, in)
		for _, a := range authors {
			if a == "Martin Fowler" {
				wantIDs = append(wantIDs, book.ID)
				break
			}
		}
	}

	t.Run("matching books only", func(t *testing.T) {
		list := crudList(t, c, oas.ListBooksParams{Author: oas.NewOptString("Martin Fowler")})
		var got []int64
		for _, b := range list.Items {
			got = append(got, b.ID)
			if !containsString(b.Authors, "Martin Fowler") {
				t.Errorf("book %d with authors %v does not have the author", b.ID, b.Authors)
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(wantIDs) {
			t.Errorf("ids = %v, want %v", got, wantIDs)
		}
		if list.Total != int64(len(wantIDs)) {
			t.Errorf("total = %d, want %d", list.Total, len(wantIDs))
		}
	})

	t.Run("total ignores limit and offset", func(t *testing.T) {
		list := crudList(t, c, oas.ListBooksParams{
			Author: oas.NewOptString("Martin Fowler"),
			Limit:  oas.NewOptInt(1),
			Offset: oas.NewOptInt(1),
		})
		if list.Total != int64(len(wantIDs)) {
			t.Errorf("total = %d, want %d", list.Total, len(wantIDs))
		}
		if len(list.Items) != 1 || list.Items[0].ID != wantIDs[1] {
			t.Errorf("items = %+v, want only book %d", list.Items, wantIDs[1])
		}
	})

	t.Run("unknown author", func(t *testing.T) {
		list := crudList(t, c, oas.ListBooksParams{Author: oas.NewOptString("Nobody")})
		if list.Total != 0 || len(list.Items) != 0 {
			t.Errorf("list = %+v, want empty", list)
		}
	})
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Given `limit=0`, `limit=101` or `offset=-1`, when I list, then I get 400.
func TestCrudListingRejectsInvalidPaging(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{"limit 0", "limit=0"},
		{"limit 101", "limit=101"},
		{"limit -1", "limit=-1"},
		{"limit not a number", "limit=abc"},
		{"offset -1", "offset=-1"},
		{"offset not a number", "offset=abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, base := crudServer(t)
			crudCreate(t, c, crudBook(1))

			status, body := crudRaw(t, http.MethodGet, base+"/api/books?"+tt.query, nil)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body %s", status, body)
			}
			var errBody oas.Error
			if err := json.Unmarshal(body, &errBody); err != nil || errBody.Error == "" {
				t.Errorf("error body = %s, want {\"error\": \"<message>\"}", body)
			}
		})
	}

	t.Run("through the generated client", func(t *testing.T) {
		c, _ := crudServer(t)
		for _, params := range []oas.ListBooksParams{
			{Limit: oas.NewOptInt(0)},
			{Limit: oas.NewOptInt(101)},
			{Offset: oas.NewOptInt(-1)},
		} {
			_, err := c.ListBooks(context.Background(), params)
			if err == nil {
				t.Errorf("ListBooks(%+v) succeeded, want an error", params)
				continue
			}
			var apiErr *oas.ErrorStatusCode
			if errors.As(err, &apiErr) && apiErr.StatusCode != http.StatusBadRequest {
				t.Errorf("ListBooks(%+v) status = %d, want 400", params, apiErr.StatusCode)
			}
		}
	})
}

// Given an empty catalog, when I list, then I get 200 with `items: []` and `total` 0.
func TestCrudListingAnEmptyCatalog(t *testing.T) {
	c, base := crudServer(t)

	list := crudList(t, c, oas.ListBooksParams{})
	if list.Total != 0 || len(list.Items) != 0 {
		t.Errorf("list = %+v, want no items and total 0", list)
	}

	status, body := crudRaw(t, http.MethodGet, base+"/api/books", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw["items"])); got != "[]" {
		t.Errorf("items = %s, want [] (body %s)", got, body)
	}
	if got := strings.TrimSpace(string(raw["total"])); got != "0" {
		t.Errorf("total = %s, want 0", got)
	}
}

// --- Update ---

// Given a book with version 1, when I update it with version 1 and a new title, then I get
// 200 with the new title, version 2, an `updatedAt` later than or equal to before, and an
// unchanged `createdAt`.
func TestCrudUpdatingABook(t *testing.T) {
	c, base := crudServer(t)
	original := crudCreate(t, c, crudBook(1))
	if original.Version != 1 {
		t.Fatalf("version = %d, want 1", original.Version)
	}

	upd := crudUpdateFrom(crudBook(1), 1)
	upd.Title = "A New Title"
	revised, err := c.UpdateBook(context.Background(), upd, oas.UpdateBookParams{ID: original.ID})
	if err != nil {
		t.Fatal(err)
	}

	if revised.ID != original.ID {
		t.Errorf("id = %d, want %d", revised.ID, original.ID)
	}
	if revised.Title != "A New Title" {
		t.Errorf("title = %q, want A New Title", revised.Title)
	}
	if revised.Version != 2 {
		t.Errorf("version = %d, want 2", revised.Version)
	}
	if revised.UpdatedAt.Before(original.UpdatedAt) {
		t.Errorf("updatedAt = %v, before original %v", revised.UpdatedAt, original.UpdatedAt)
	}
	if !revised.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("createdAt = %v, want unchanged %v", revised.CreatedAt, original.CreatedAt)
	}
	crudRequireSameBook(t, crudGet(t, c, original.ID), revised)

	status, body := crudRaw(t, http.MethodPut, fmt.Sprintf("%s/api/books/%d", base, original.ID),
		crudUpdateFrom(crudBook(1), 2))
	if status != http.StatusOK {
		t.Errorf("raw PUT status = %d, want 200; body %s", status, body)
	}
}

// Updating replaces the editable fields, so optional fields that are left out are removed.
func TestCrudUpdatingReplacesTheEditableFields(t *testing.T) {
	c, _ := crudServer(t)
	original := crudCreate(t, c, crudBook(1))

	upd := &oas.BookUpdate{
		Isbn:    "978-0-13-235088-4",
		Title:   "Replaced",
		Authors: []string{"Only Author"},
		Version: original.Version,
	}
	revised, err := c.UpdateBook(context.Background(), upd, oas.UpdateBookParams{ID: original.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := crudGet(t, c, original.ID)
	crudRequireSameBook(t, got, revised)
	if got.Isbn != "9780132350884" || got.Title != "Replaced" || strings.Join(got.Authors, "|") != "Only Author" {
		t.Errorf("book = %+v, want the replaced values", got)
	}
	if got.PublishedYear.Set || got.Language.Set || got.Pages.Set {
		t.Errorf("optional fields still set: %+v %+v %+v", got.PublishedYear, got.Language, got.Pages)
	}
}

// Keeping a book's own ISBN is not a conflict.
func TestCrudUpdatingABookKeepsItsOwnISBN(t *testing.T) {
	c, _ := crudServer(t)
	original := crudCreate(t, c, crudBook(1))

	upd := crudUpdateFrom(crudBook(1), 1)
	upd.Isbn = original.Isbn[:3] + "-" + original.Isbn[3:] // same ISBN, different spelling
	revised, err := c.UpdateBook(context.Background(), upd, oas.UpdateBookParams{ID: original.ID})
	if err != nil {
		t.Fatal(err)
	}
	if revised.Isbn != original.Isbn || revised.Version != 2 {
		t.Errorf("revised = %+v, want isbn %s and version 2", revised, original.Isbn)
	}
}

// Given a book now at version 2, when I update it with version 1, then I get 409 and the
// book is unchanged.
func TestCrudUpdatingWithAStaleVersion(t *testing.T) {
	c, _ := crudServer(t)
	original := crudCreate(t, c, crudBook(1))

	first := crudUpdateFrom(crudBook(1), 1)
	first.Title = "Second Edition"
	current, err := c.UpdateBook(context.Background(), first, oas.UpdateBookParams{ID: original.ID})
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != 2 {
		t.Fatalf("version = %d, want 2", current.Version)
	}

	stale := crudUpdateFrom(crudBook(1), 1)
	stale.Title = "Lost Update"
	_, err = c.UpdateBook(context.Background(), stale, oas.UpdateBookParams{ID: original.ID})
	crudRequireError(t, err, http.StatusConflict)

	got := crudGet(t, c, original.ID)
	crudRequireSameBook(t, got, current)
	if got.Title != "Second Edition" || got.Version != 2 {
		t.Errorf("book = %+v, want title Second Edition at version 2", got)
	}
	if total := crudTotal(t, c); total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
}

// Given two books, when I change the first one's ISBN to the second one's, then I get 409.
func TestCrudUpdatingToAnExistingISBN(t *testing.T) {
	for _, spelled := range []bool{false, true} {
		name := "normalized"
		if spelled {
			name = "hyphenated"
		}
		t.Run(name, func(t *testing.T) {
			c, _ := crudServer(t)
			first := crudCreate(t, c, crudBook(1))
			second := crudCreate(t, c, crudBook(2))

			upd := crudUpdateFrom(crudBook(1), first.Version)
			upd.Isbn = second.Isbn
			if spelled {
				upd.Isbn = second.Isbn[:3] + "-" + second.Isbn[3:]
			}
			_, err := c.UpdateBook(context.Background(), upd, oas.UpdateBookParams{ID: first.ID})
			crudRequireError(t, err, http.StatusConflict)

			crudRequireSameBook(t, crudGet(t, c, first.ID), first)
			crudRequireSameBook(t, crudGet(t, c, second.ID), second)
		})
	}
}

// Given no book with ID 999, when I update it, then I get 404. Invalid fields give 400, as
// for create.
func TestCrudUpdatingAnUnknownBook(t *testing.T) {
	c, _ := crudServer(t)
	_, err := c.UpdateBook(context.Background(), crudUpdateFrom(crudBook(1), 1), oas.UpdateBookParams{ID: 999})
	crudRequireError(t, err, http.StatusNotFound)
	if total := crudTotal(t, c); total != 0 {
		t.Errorf("total = %d, want 0: update must not create a book", total)
	}
}

// Given no book with ID 999, when I update it, then I get 404. Invalid fields give 400, as
// for create.
func TestCrudUpdatingRejectsInvalidFields(t *testing.T) {
	for _, tt := range crudInvalidCases() {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := crudServer(t)
			original := crudCreate(t, c, crudBook(1))

			in := crudBook(1)
			tt.mutate(in)
			_, err := c.UpdateBook(context.Background(), crudUpdateFrom(in, original.Version),
				oas.UpdateBookParams{ID: original.ID})
			msg := crudRequireError(t, err, http.StatusBadRequest)
			if !strings.Contains(msg, tt.field) {
				t.Errorf("message %q does not name field %q", msg, tt.field)
			}
			crudRequireSameBook(t, crudGet(t, c, original.ID), original)
		})
	}
}

// --- Delete ---

// Given a book, when I delete it, then I get 204, and getting it afterwards gives 404.
func TestCrudDeletingABook(t *testing.T) {
	c, base := crudServer(t)
	book := crudCreate(t, c, crudBook(1))
	other := crudCreate(t, c, crudBook(2))

	status, body := crudRaw(t, http.MethodDelete, fmt.Sprintf("%s/api/books/%d", base, book.ID), nil)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body %s", status, body)
	}
	if len(body) != 0 {
		t.Errorf("204 response has a body: %s", body)
	}

	_, err := c.GetBook(context.Background(), oas.GetBookParams{ID: book.ID})
	crudRequireError(t, err, http.StatusNotFound)
	if total := crudTotal(t, c); total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	crudRequireSameBook(t, crudGet(t, c, other.ID), other)

	// A second delete finds nothing.
	err = c.DeleteBook(context.Background(), oas.DeleteBookParams{ID: book.ID})
	crudRequireError(t, err, http.StatusNotFound)
}

// A deleted book's ISBN can be used again.
func TestCrudDeletingABookFreesItsISBN(t *testing.T) {
	c, _ := crudServer(t)
	book := crudCreate(t, c, crudBook(1))
	if err := c.DeleteBook(context.Background(), oas.DeleteBookParams{ID: book.ID}); err != nil {
		t.Fatal(err)
	}
	again := crudCreate(t, c, crudBook(1))
	if again.Isbn != book.Isbn {
		t.Errorf("isbn = %q, want %q", again.Isbn, book.Isbn)
	}
}

// Given no book with ID 999, when I delete it, then I get 404.
func TestCrudDeletingAnUnknownBook(t *testing.T) {
	c, _ := crudServer(t)
	crudCreate(t, c, crudBook(1))

	err := c.DeleteBook(context.Background(), oas.DeleteBookParams{ID: 999})
	crudRequireError(t, err, http.StatusNotFound)
	if total := crudTotal(t, c); total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
}

// Given a deleted book, when I create a new book, then it gets a new ID, never the deleted
// one's.
func TestCrudIDsAreNeverReused(t *testing.T) {
	tests := []struct {
		name   string
		delete func(books []*oas.Book) *oas.Book
	}{
		{"deleting the newest book", func(b []*oas.Book) *oas.Book { return b[len(b)-1] }},
		{"deleting the oldest book", func(b []*oas.Book) *oas.Book { return b[0] }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := crudServer(t)
			var books []*oas.Book
			for i := 1; i <= 3; i++ {
				books = append(books, crudCreate(t, c, crudBook(i)))
			}
			deleted := tt.delete(books)
			if err := c.DeleteBook(context.Background(), oas.DeleteBookParams{ID: deleted.ID}); err != nil {
				t.Fatal(err)
			}

			fresh := crudCreate(t, c, crudBook(10))
			for _, b := range books {
				if fresh.ID == b.ID {
					t.Errorf("new book got ID %d, which was already used", fresh.ID)
				}
			}
			if fresh.ID <= books[len(books)-1].ID {
				t.Errorf("new ID %d is not greater than the previous highest %d", fresh.ID, books[len(books)-1].ID)
			}
			_, err := c.GetBook(context.Background(), oas.GetBookParams{ID: deleted.ID})
			crudRequireError(t, err, http.StatusNotFound)
		})
	}
}
