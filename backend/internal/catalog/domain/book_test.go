package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

const goodISBN = "978-0-13-235088-4"

func intp(i int) *int       { return &i }
func strp(s string) *string { return &s }

func validDraft() BookDraft {
	return BookDraft{ISBN: goodISBN, Title: "Clean Code", Authors: []string{"Robert C. Martin"}}
}

func authors(n int) []string {
	a := make([]string, n)
	for i := range a {
		a[i] = "Author"
	}
	return a
}

func TestNewBook(t *testing.T) {
	book, err := NewBook(BookDraft{
		ISBN:          goodISBN,
		Title:         "  Clean Code  ",
		Authors:       []string{" Robert C. Martin ", "Co Author"},
		PublishedYear: intp(2008),
		Language:      strp("en"),
		Pages:         intp(464),
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	want := Book{
		ISBN:          "9780132350884",
		Title:         "Clean Code",
		Authors:       []string{"Robert C. Martin", "Co Author"},
		PublishedYear: intp(2008),
		Language:      strp("en"),
		Pages:         intp(464),
		CreatedAt:     t0,
		UpdatedAt:     t0,
		Version:       1,
	}
	if !reflect.DeepEqual(book, want) {
		t.Fatalf("NewBook = %+v, want %+v", book, want)
	}
}

func TestNewBook_OptionalFieldsAbsent(t *testing.T) {
	book, err := NewBook(validDraft(), t0)
	if err != nil {
		t.Fatal(err)
	}
	if book.ID != 0 || book.PublishedYear != nil || book.Language != nil || book.Pages != nil {
		t.Fatalf("unexpected book %+v", book)
	}
}

func TestNewBook_CopiesDraft(t *testing.T) {
	d := validDraft()
	d.PublishedYear, d.Language, d.Pages = intp(2008), strp("en"), intp(464)
	book, err := NewBook(d, t0)
	if err != nil {
		t.Fatal(err)
	}
	d.Authors[0] = "Changed"
	*d.PublishedYear, *d.Language, *d.Pages = 1, "xx", 1
	if book.Authors[0] != "Robert C. Martin" || *book.PublishedYear != 2008 || *book.Language != "en" || *book.Pages != 464 {
		t.Fatalf("draft changes leaked into book: %+v", book)
	}
}

func TestNewBook_Validation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(d *BookDraft)
		field  string // "" = valid
	}{
		{"isbn invalid", func(d *BookDraft) { d.ISBN = "123" }, "isbn"},

		{"title 1 rune", func(d *BookDraft) { d.Title = "x" }, ""},
		{"title 300 runes", func(d *BookDraft) { d.Title = strings.Repeat("x", 300) }, ""},
		{"title 301 runes", func(d *BookDraft) { d.Title = strings.Repeat("x", 301) }, "title"},
		{"title 300 multibyte runes", func(d *BookDraft) { d.Title = strings.Repeat("ä", 300) }, ""},
		{"title 301 multibyte runes", func(d *BookDraft) { d.Title = strings.Repeat("ä", 301) }, "title"},
		{"title empty", func(d *BookDraft) { d.Title = "" }, "title"},
		{"title whitespace only", func(d *BookDraft) { d.Title = " \t\n " }, "title"},
		{"title 300 runes plus padding", func(d *BookDraft) { d.Title = "  " + strings.Repeat("x", 300) + "  " }, ""},

		{"title invalid UTF-8", func(d *BookDraft) { d.Title = "Clean\xffCode" }, "title"},
		{"title truncated UTF-8", func(d *BookDraft) { d.Title = "Clean Code\xc3" }, "title"},
		{"title NUL", func(d *BookDraft) { d.Title = "Clean\x00Code" }, "title"},
		{"title leading NUL", func(d *BookDraft) { d.Title = "\x00Clean Code" }, "title"},
		{"title newline inside", func(d *BookDraft) { d.Title = "Clean\nCode" }, "title"},
		{"title trailing newline", func(d *BookDraft) { d.Title = "Clean Code\n" }, "title"},
		{"title leading tab", func(d *BookDraft) { d.Title = "\tClean Code" }, "title"},
		{"title DEL", func(d *BookDraft) { d.Title = "Clean\x7fCode" }, "title"},
		{"title C1 control", func(d *BookDraft) { d.Title = "Clean\U00000085Code" }, "title"},
		{"title multibyte valid UTF-8", func(d *BookDraft) { d.Title = "Café 日本語 🚀" }, ""},

		{"title zero width space only", func(d *BookDraft) { d.Title = "\U0000200b" }, "title"},
		{"title byte order mark only", func(d *BookDraft) { d.Title = "\U0000feff" }, "title"},
		{"title soft hyphen only", func(d *BookDraft) { d.Title = "\U000000ad" }, "title"},
		{"title word joiner only", func(d *BookDraft) { d.Title = "\U00002060" }, "title"},
		{"title left-to-right mark only", func(d *BookDraft) { d.Title = "\U0000200e" }, "title"},
		{"title right-to-left override only", func(d *BookDraft) { d.Title = "\U0000202e" }, "title"},
		{"title invisibles with spaces", func(d *BookDraft) { d.Title = " \U0000200b \U0000feff\U0000200d " }, "title"},
		{"title no-break space only", func(d *BookDraft) { d.Title = "\U000000a0\U000000a0" }, "title"},
		{"title invisible next to a letter", func(d *BookDraft) { d.Title = "Go\U0000200b" }, ""},
		{"title emoji sequence with a joiner", func(d *BookDraft) { d.Title = "\U0001F469\U0000200d\U0001F4BB" }, ""},

		{"authors 0", func(d *BookDraft) { d.Authors = nil }, "authors"},
		{"author invalid UTF-8", func(d *BookDraft) { d.Authors = []string{"A", "Bob\xff"} }, "authors"},
		{"author NUL", func(d *BookDraft) { d.Authors = []string{"Bo\x00b"} }, "authors"},
		{"author newline inside", func(d *BookDraft) { d.Authors = []string{"Bo\nb"} }, "authors"},
		{"author trailing newline", func(d *BookDraft) { d.Authors = []string{"Bob\n"} }, "authors"},
		{"author leading tab", func(d *BookDraft) { d.Authors = []string{"\tBob"} }, "authors"},
		{"author DEL", func(d *BookDraft) { d.Authors = []string{"Bob\x7f"} }, "authors"},
		{"author multibyte valid UTF-8", func(d *BookDraft) { d.Authors = []string{"Åsa Müller 李"} }, ""},
		{"author zero width space only", func(d *BookDraft) { d.Authors = []string{"\U0000200b"} }, "authors"},
		{"author byte order mark only", func(d *BookDraft) { d.Authors = []string{"Bob", "\U0000feff"} }, "authors"},
		{"author joiner and spaces", func(d *BookDraft) { d.Authors = []string{" \U0000200d "} }, "authors"},
		{"author invisible next to a letter", func(d *BookDraft) { d.Authors = []string{"Bob\U0000200b"} }, ""},
		{"authors 1", func(d *BookDraft) { d.Authors = authors(1) }, ""},
		{"authors 20", func(d *BookDraft) { d.Authors = authors(20) }, ""},
		{"authors 21", func(d *BookDraft) { d.Authors = authors(21) }, "authors"},
		{"author empty", func(d *BookDraft) { d.Authors = []string{"A", ""} }, "authors"},
		{"author whitespace", func(d *BookDraft) { d.Authors = []string{"  "} }, "authors"},
		{"author 200 runes", func(d *BookDraft) { d.Authors = []string{strings.Repeat("ä", 200)} }, ""},
		{"author 201 runes", func(d *BookDraft) { d.Authors = []string{strings.Repeat("ä", 201)} }, "authors"},

		{"year 1449", func(d *BookDraft) { d.PublishedYear = intp(1449) }, "publishedYear"},
		{"year 1450", func(d *BookDraft) { d.PublishedYear = intp(1450) }, ""},
		{"year 2100", func(d *BookDraft) { d.PublishedYear = intp(2100) }, ""},
		{"year 2101", func(d *BookDraft) { d.PublishedYear = intp(2101) }, "publishedYear"},

		{"language en", func(d *BookDraft) { d.Language = strp("en") }, ""},
		{"language EN", func(d *BookDraft) { d.Language = strp("EN") }, "language"},
		{"language eng", func(d *BookDraft) { d.Language = strp("eng") }, "language"},
		{"language e", func(d *BookDraft) { d.Language = strp("e") }, "language"},
		{"language e1", func(d *BookDraft) { d.Language = strp("e1") }, "language"},
		{"language empty", func(d *BookDraft) { d.Language = strp("") }, "language"},
		{"language trailing newline", func(d *BookDraft) { d.Language = strp("en\n") }, "language"},
		{"language leading space", func(d *BookDraft) { d.Language = strp(" en") }, "language"},
		{"language trailing NUL", func(d *BookDraft) { d.Language = strp("e\x00") }, "language"},
		{"language invalid UTF-8", func(d *BookDraft) { d.Language = strp("\xff\xff") }, "language"},
		{"language non-ASCII", func(d *BookDraft) { d.Language = strp("éé") }, "language"},

		{"pages 0", func(d *BookDraft) { d.Pages = intp(0) }, "pages"},
		{"pages 1", func(d *BookDraft) { d.Pages = intp(1) }, ""},
		{"pages 10000", func(d *BookDraft) { d.Pages = intp(10000) }, ""},
		{"pages 10001", func(d *BookDraft) { d.Pages = intp(10001) }, "pages"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := validDraft()
			tc.mutate(&d)
			_, err := NewBook(d, t0)
			if tc.field == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			assertValidation(t, err, tc.field)
		})
	}
}

func TestNewBook_ReportsFirstInvalidFieldInOrder(t *testing.T) {
	d := BookDraft{ISBN: "x", Title: "", Authors: nil, PublishedYear: intp(1), Language: strp("X"), Pages: intp(0)}
	order := []struct {
		field string
		fix   func(d *BookDraft)
	}{
		{"isbn", func(d *BookDraft) { d.ISBN = goodISBN }},
		{"title", func(d *BookDraft) { d.Title = "T" }},
		{"authors", func(d *BookDraft) { d.Authors = []string{"A"} }},
		{"publishedYear", func(d *BookDraft) { d.PublishedYear = nil }},
		{"language", func(d *BookDraft) { d.Language = nil }},
		{"pages", func(d *BookDraft) { d.Pages = nil }},
	}
	for _, step := range order {
		_, err := NewBook(d, t0)
		assertValidation(t, err, step.field)
		step.fix(&d)
	}
	if _, err := NewBook(d, t0); err != nil {
		t.Fatal(err)
	}
}

func newTestBook(t *testing.T) Book {
	t.Helper()
	d := validDraft()
	d.PublishedYear, d.Language, d.Pages = intp(2008), strp("en"), intp(464)
	b, err := NewBook(d, t0)
	if err != nil {
		t.Fatal(err)
	}
	b.ID = 7
	return b
}

func TestBook_Revise(t *testing.T) {
	b := newTestBook(t)
	later := t0.Add(time.Hour)
	d := BookDraft{ISBN: "9791034326228", Title: " New Title ", Authors: []string{" Someone "}, PublishedYear: intp(2020)}
	if err := b.Revise(d, 1, later); err != nil {
		t.Fatal(err)
	}
	want := Book{
		ID: 7, ISBN: "9791034326228", Title: "New Title", Authors: []string{"Someone"},
		PublishedYear: intp(2020), Language: nil, Pages: nil,
		CreatedAt: t0, UpdatedAt: later, Version: 2,
	}
	if !reflect.DeepEqual(b, want) {
		t.Fatalf("Revise result = %+v, want %+v", b, want)
	}
}

func TestBook_Revise_RemovesOptionalFields(t *testing.T) {
	b := newTestBook(t)
	if err := b.Revise(validDraft(), 1, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if b.PublishedYear != nil || b.Language != nil || b.Pages != nil {
		t.Fatalf("optional fields not removed: %+v", b)
	}
}

func TestBook_Revise_CopiesDraft(t *testing.T) {
	b := newTestBook(t)
	d := validDraft()
	d.Pages = intp(10)
	if err := b.Revise(d, 1, t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	d.Authors[0] = "Changed"
	*d.Pages = 99
	if b.Authors[0] != "Robert C. Martin" || *b.Pages != 10 {
		t.Fatalf("draft changes leaked into book: %+v", b)
	}
}

func TestBook_Revise_Rejected(t *testing.T) {
	tests := []struct {
		name    string
		draft   BookDraft
		version int64
		check   func(t *testing.T, err error)
	}{
		{"version conflict", validDraft(), 2, func(t *testing.T, err error) {
			if !errors.Is(err, ErrVersionConflict) {
				t.Fatalf("err = %v, want ErrVersionConflict", err)
			}
		}},
		{"invalid draft", BookDraft{ISBN: goodISBN, Title: "", Authors: []string{"A"}}, 1, func(t *testing.T, err error) {
			assertValidation(t, err, "title")
		}},
		{"invalid draft wins over stale version", BookDraft{ISBN: "x"}, 99, func(t *testing.T, err error) {
			assertValidation(t, err, "isbn")
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newTestBook(t)
			before := newTestBook(t)
			tc.check(t, b.Revise(tc.draft, tc.version, t0.Add(time.Hour)))
			if !reflect.DeepEqual(b, before) {
				t.Fatalf("book changed on error: %+v, want %+v", b, before)
			}
		})
	}
}

func TestNewPage(t *testing.T) {
	tests := []struct {
		name          string
		limit, offset int
		field         string // "" = valid
	}{
		{"default", DefaultLimit, 0, ""},
		{"limit 1", 1, 0, ""},
		{"limit max", MaxLimit, 0, ""},
		{"limit 0", 0, 0, "limit"},
		{"limit max+1", MaxLimit + 1, 0, "limit"},
		{"limit negative", -1, 0, "limit"},
		{"offset 0", 10, 0, ""},
		{"offset large", 10, 1000000, ""},
		{"offset negative", 10, -1, "offset"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewPage(tc.limit, tc.offset)
			if tc.field != "" {
				assertValidation(t, err, tc.field)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if p != (Page{Limit: tc.limit, Offset: tc.offset}) {
				t.Fatalf("page = %+v", p)
			}
		})
	}
}

func TestValidationError(t *testing.T) {
	err := error(&ValidationError{Field: "isbn", Reason: "invalid check digit"})
	if err.Error() != "isbn: invalid check digit" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatal("ValidationError must not match ErrNotFound")
	}
}
