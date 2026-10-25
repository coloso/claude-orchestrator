package app_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"orch-test/backend/internal/catalog/app"
	"orch-test/backend/internal/catalog/domain"
)

var ctx = context.Background()

// clock is a fake clock the tests move forward by hand.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// 2026-03-04 05:06:07.123456789 in UTC+2
var start = time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.FixedZone("CEST", 2*3600))

func newService(t *testing.T) (*app.Service, *clock) {
	t.Helper()
	c := &clock{now: start}
	return app.NewService(newFakeRepository(), app.WithClock(c.Now)), c
}

func draft(isbn string) domain.BookDraft {
	return domain.BookDraft{ISBN: isbn, Title: "Clean Code", Authors: []string{"Robert C. Martin"}}
}

const (
	isbnA = "978-0-13-235088-4"
	isbnB = "978-0-13-468599-1"
)

func TestAddBook(t *testing.T) {
	svc, _ := newService(t)
	got, err := svc.AddBook(ctx, draft(isbnA))
	if err != nil {
		t.Fatal(err)
	}
	wantTime := time.Date(2026, 3, 4, 3, 6, 7, 123000000, time.UTC)
	if got.ID != 1 || got.ISBN != "9780132350884" || got.Version != 1 {
		t.Fatalf("book = %+v", got)
	}
	if !got.CreatedAt.Equal(wantTime) || !got.UpdatedAt.Equal(wantTime) {
		t.Fatalf("timestamps = %v / %v, want %v", got.CreatedAt, got.UpdatedAt, wantTime)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Fatalf("CreatedAt location = %v, want UTC", got.CreatedAt.Location())
	}
}

func TestAddBook_RoundTrip(t *testing.T) {
	svc, _ := newService(t)
	added, err := svc.AddBook(ctx, draft(isbnA))
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.GetBook(ctx, added.ID)
	if err != nil || !reflect.DeepEqual(got, added) {
		t.Fatalf("GetBook = %+v, %v; want %+v", got, err, added)
	}
}

func TestAddBook_Invalid(t *testing.T) {
	svc, _ := newService(t)
	_, err := svc.AddBook(ctx, draft("123"))
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if page, _ := svc.ListBooks(ctx, app.ListQuery{Limit: 10}); page.Total != 0 {
		t.Fatalf("invalid book was stored")
	}
}

func TestAddBook_DuplicateISBN(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.AddBook(ctx, draft(isbnA)); err != nil {
		t.Fatal(err)
	}
	// same ISBN, written differently
	_, err := svc.AddBook(ctx, draft("9780132350884"))
	if !errors.Is(err, domain.ErrDuplicateISBN) {
		t.Fatalf("err = %v, want ErrDuplicateISBN", err)
	}
}

func TestGetBook_NotFound(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.GetBook(ctx, 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListBooks(t *testing.T) {
	svc, _ := newService(t)
	isbns := []string{"978-0-13-235088-4", "978-0-13-468599-1", "978-1-4028-9462-6", "978-0-306-40615-7"}
	for i, isbn := range isbns {
		d := draft(isbn)
		d.Authors = []string{"Common"}
		if i%2 == 1 {
			d.Authors = []string{"Common", "Even"}
		}
		if _, err := svc.AddBook(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	ids := func(p app.BookPage) []domain.BookID {
		out := []domain.BookID{}
		for _, b := range p.Items {
			out = append(out, b.ID)
		}
		return out
	}

	tests := []struct {
		name      string
		q         app.ListQuery
		wantIDs   []domain.BookID
		wantTotal int64
	}{
		{"first page", app.ListQuery{Limit: 3}, []domain.BookID{1, 2, 3}, 4},
		{"second page", app.ListQuery{Limit: 3, Offset: 3}, []domain.BookID{4}, 4},
		{"past the end", app.ListQuery{Limit: 3, Offset: 10}, []domain.BookID{}, 4},
		{"filtered", app.ListQuery{Author: "Even", Limit: 10}, []domain.BookID{2, 4}, 2},
		{"filtered and paged", app.ListQuery{Author: "Even", Limit: 1, Offset: 1}, []domain.BookID{4}, 2},
		{"filter without match", app.ListQuery{Author: "Nobody", Limit: 10}, []domain.BookID{}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page, err := svc.ListBooks(ctx, tc.q)
			if err != nil {
				t.Fatal(err)
			}
			if page.Items == nil {
				t.Fatal("Items must not be nil")
			}
			if !reflect.DeepEqual(ids(page), tc.wantIDs) || page.Total != tc.wantTotal {
				t.Fatalf("ids = %v, total = %d; want %v, %d", ids(page), page.Total, tc.wantIDs, tc.wantTotal)
			}
		})
	}
}

func TestListBooks_InvalidPage(t *testing.T) {
	svc, _ := newService(t)
	for name, q := range map[string]app.ListQuery{
		"limit 0":       {Limit: 0},
		"limit too big": {Limit: domain.MaxLimit + 1},
		"offset":        {Limit: 10, Offset: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.ListBooks(ctx, q); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestListBooks_AuthorFilterFollowsTheTextRules(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.AddBook(ctx, draft(isbnA)); err != nil {
		t.Fatal(err)
	}
	for name, author := range map[string]string{
		"NUL":     "Ro\x00bert",
		"newline": "Robert C. Martin\n",
		"invalid": "\xff",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.ListBooks(ctx, app.ListQuery{Author: author, Limit: 10})
			var ve *domain.ValidationError
			if !errors.As(err, &ve) || ve.Field != "author" {
				t.Fatalf("err = %v, want a ValidationError on author", err)
			}
		})
	}
}

func TestListBooks_AuthorFilterIsNotTrimmed(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.AddBook(ctx, draft(isbnA)); err != nil {
		t.Fatal(err)
	}
	page, err := svc.ListBooks(ctx, app.ListQuery{Author: " Robert C. Martin ", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("total = %d, want 0: the filter is an exact match", page.Total)
	}
}

func TestReviseBook(t *testing.T) {
	svc, c := newService(t)
	added, err := svc.AddBook(ctx, draft(isbnA))
	if err != nil {
		t.Fatal(err)
	}
	c.now = start.Add(time.Hour)

	d := draft(isbnB)
	d.Title = "Clean Architecture"
	pages := 432
	d.Pages = &pages
	got, err := svc.ReviseBook(ctx, added.ID, d, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != added.ID || got.ISBN != "9780134685991" || got.Title != "Clean Architecture" ||
		got.Version != 2 || got.Pages == nil || *got.Pages != 432 {
		t.Fatalf("revised = %+v", got)
	}
	if !got.CreatedAt.Equal(added.CreatedAt) {
		t.Fatalf("CreatedAt changed: %v -> %v", added.CreatedAt, got.CreatedAt)
	}
	wantUpdated := time.Date(2026, 3, 4, 4, 6, 7, 123000000, time.UTC)
	if !got.UpdatedAt.Equal(wantUpdated) {
		t.Fatalf("UpdatedAt = %v, want %v", got.UpdatedAt, wantUpdated)
	}
	stored, _ := svc.GetBook(ctx, added.ID)
	if !reflect.DeepEqual(stored, got) {
		t.Fatalf("stored = %+v, want %+v", stored, got)
	}

	// an optional field missing from the revision is removed
	got, err = svc.ReviseBook(ctx, added.ID, draft(isbnB), 2)
	if err != nil || got.Pages != nil || got.Version != 3 {
		t.Fatalf("second revision = %+v, %v", got, err)
	}
}

func TestReviseBook_Errors(t *testing.T) {
	svc, _ := newService(t)
	a, _ := svc.AddBook(ctx, draft(isbnA))
	if _, err := svc.AddBook(ctx, draft(isbnB)); err != nil {
		t.Fatal(err)
	}
	bad := draft("nope")
	tests := []struct {
		name    string
		id      domain.BookID
		draft   domain.BookDraft
		version int64
		want    error
	}{
		{"not found", 99, draft(isbnA), 1, domain.ErrNotFound},
		{"stale version", a.ID, draft(isbnA), 5, domain.ErrVersionConflict},
		{"invalid draft", a.ID, bad, 1, domain.ErrInvalid},
		{"duplicate ISBN", a.ID, draft(isbnB), 1, domain.ErrDuplicateISBN},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.ReviseBook(ctx, tc.id, tc.draft, tc.version); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if got, _ := svc.GetBook(ctx, a.ID); !reflect.DeepEqual(got, a) {
				t.Fatalf("book changed on error: %+v", got)
			}
		})
	}
}

// racingRepo simulates a concurrent writer: the book changes between Get and Update.
type racingRepo struct{ domain.Repository }

func (racingRepo) Update(context.Context, domain.Book, int64) error { return domain.ErrVersionConflict }

func TestReviseBook_ConcurrentWriter(t *testing.T) {
	mem := newFakeRepository()
	svc := app.NewService(racingRepo{mem})
	added, err := svc.AddBook(ctx, draft(isbnA))
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ReviseBook(ctx, added.ID, draft(isbnA), added.Version)
	if !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("err = %v, want ErrVersionConflict", err)
	}
}

func TestRemoveBook(t *testing.T) {
	svc, _ := newService(t)
	added, _ := svc.AddBook(ctx, draft(isbnA))
	if err := svc.RemoveBook(ctx, added.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetBook(ctx, added.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetBook after remove: %v", err)
	}
	if err := svc.RemoveBook(ctx, added.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second RemoveBook: %v", err)
	}
}

func TestNewService_DefaultClock(t *testing.T) {
	svc := app.NewService(newFakeRepository())
	before := time.Now().UTC().Truncate(time.Millisecond)
	added, err := svc.AddBook(ctx, draft(isbnA))
	if err != nil {
		t.Fatal(err)
	}
	if added.CreatedAt.Before(before) || time.Since(added.CreatedAt) > time.Minute {
		t.Fatalf("CreatedAt = %v, want about now", added.CreatedAt)
	}
}
