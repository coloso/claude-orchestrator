package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"orch-test/backend/internal/catalog/app"
	"orch-test/backend/internal/catalog/domain"
	"orch-test/backend/internal/oas"
)

// fakeUseCases records its inputs and returns the configured results.
type fakeUseCases struct {
	book domain.Book
	page app.BookPage
	err  error

	draft   domain.BookDraft
	id      domain.BookID
	version int64
	query   app.ListQuery
}

var _ app.UseCases = (*fakeUseCases)(nil)

func (f *fakeUseCases) AddBook(_ context.Context, d domain.BookDraft) (domain.Book, error) {
	f.draft = d
	return f.book, f.err
}

func (f *fakeUseCases) GetBook(_ context.Context, id domain.BookID) (domain.Book, error) {
	f.id = id
	return f.book, f.err
}

func (f *fakeUseCases) ListBooks(_ context.Context, q app.ListQuery) (app.BookPage, error) {
	f.query = q
	return f.page, f.err
}

func (f *fakeUseCases) ReviseBook(_ context.Context, id domain.BookID, d domain.BookDraft, v int64) (domain.Book, error) {
	f.id, f.draft, f.version = id, d, v
	return f.book, f.err
}

func (f *fakeUseCases) RemoveBook(_ context.Context, id domain.BookID) error {
	f.id = id
	return f.err
}

func ptr[T any](v T) *T { return &v }

var (
	t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	t1 = t0.Add(time.Hour)
)

func fullBook() domain.Book {
	return domain.Book{
		ID: 7, ISBN: "9780132350884", Title: "Clean Code", Authors: []string{"Robert C. Martin"},
		PublishedYear: ptr(2008), Language: ptr("en"), Pages: ptr(464),
		CreatedAt: t0, UpdatedAt: t1, Version: 3,
	}
}

func fullOASBook() *oas.Book {
	return &oas.Book{
		ID: 7, Isbn: "9780132350884", Title: "Clean Code", Authors: []string{"Robert C. Martin"},
		PublishedYear: oas.NewOptInt(2008), Language: oas.NewOptString("en"), Pages: oas.NewOptInt(464),
		CreatedAt: t0, UpdatedAt: t1, Version: 3,
	}
}

func TestCreateBook(t *testing.T) {
	t.Run("all fields", func(t *testing.T) {
		f := &fakeUseCases{book: fullBook()}
		got, err := NewCatalogHandler(f).CreateBook(context.Background(), &oas.BookInput{
			Isbn: "978-0-13-235088-4", Title: "Clean Code", Authors: []string{"Robert C. Martin"},
			PublishedYear: oas.NewOptInt(2008), Language: oas.NewOptString("en"), Pages: oas.NewOptInt(464),
		})
		if err != nil {
			t.Fatal(err)
		}
		want := domain.BookDraft{
			ISBN: "978-0-13-235088-4", Title: "Clean Code", Authors: []string{"Robert C. Martin"},
			PublishedYear: ptr(2008), Language: ptr("en"), Pages: ptr(464),
		}
		if !reflect.DeepEqual(f.draft, want) {
			t.Fatalf("draft = %+v, want %+v", f.draft, want)
		}
		if !reflect.DeepEqual(got, fullOASBook()) {
			t.Fatalf("book = %+v, want %+v", got, fullOASBook())
		}
	})

	t.Run("optional fields absent", func(t *testing.T) {
		f := &fakeUseCases{book: domain.Book{ID: 1, ISBN: "9780132350884", Title: "T", Authors: []string{"A"}, CreatedAt: t0, UpdatedAt: t0, Version: 1}}
		got, err := NewCatalogHandler(f).CreateBook(context.Background(), &oas.BookInput{Isbn: "x", Title: "T", Authors: []string{"A"}})
		if err != nil {
			t.Fatal(err)
		}
		if f.draft.PublishedYear != nil || f.draft.Language != nil || f.draft.Pages != nil {
			t.Fatalf("draft = %+v, want nil optional fields", f.draft)
		}
		if got.PublishedYear.IsSet() || got.Language.IsSet() || got.Pages.IsSet() {
			t.Fatalf("book = %+v, want unset optional fields", got)
		}
	})

	t.Run("error", func(t *testing.T) {
		f := &fakeUseCases{err: domain.ErrDuplicateISBN}
		got, err := NewCatalogHandler(f).CreateBook(context.Background(), &oas.BookInput{})
		if got != nil || err == nil {
			t.Fatalf("got %v, %v; want error", got, err)
		}
	})
}

func TestListBooks(t *testing.T) {
	tests := []struct {
		name   string
		params oas.ListBooksParams
		want   app.ListQuery
	}{
		{"defaults", oas.ListBooksParams{}, app.ListQuery{Limit: domain.DefaultLimit}},
		{"all set", oas.ListBooksParams{
			Author: oas.NewOptString("Martin"), Limit: oas.NewOptInt(5), Offset: oas.NewOptInt(10),
		}, app.ListQuery{Author: "Martin", Limit: 5, Offset: 10}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeUseCases{page: app.BookPage{Items: []domain.Book{fullBook()}, Total: 42}}
			got, err := NewCatalogHandler(f).ListBooks(context.Background(), tt.params)
			if err != nil {
				t.Fatal(err)
			}
			if f.query != tt.want {
				t.Fatalf("query = %+v, want %+v", f.query, tt.want)
			}
			want := &oas.BookList{Items: []oas.Book{*fullOASBook()}, Total: 42}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("list = %+v, want %+v", got, want)
			}
		})
	}

	t.Run("empty page renders a non-nil slice", func(t *testing.T) {
		for _, items := range [][]domain.Book{nil, {}} {
			f := &fakeUseCases{page: app.BookPage{Items: items}}
			got, err := NewCatalogHandler(f).ListBooks(context.Background(), oas.ListBooksParams{})
			if err != nil {
				t.Fatal(err)
			}
			if got.Items == nil || len(got.Items) != 0 || got.Total != 0 {
				t.Fatalf("list = %+v, want empty non-nil items", got)
			}
		}
	})

	t.Run("error", func(t *testing.T) {
		f := &fakeUseCases{err: &domain.ValidationError{Field: "limit", Reason: "too big"}}
		if got, err := NewCatalogHandler(f).ListBooks(context.Background(), oas.ListBooksParams{}); got != nil || err == nil {
			t.Fatalf("got %v, %v; want error", got, err)
		}
	})
}

func TestGetBook(t *testing.T) {
	f := &fakeUseCases{book: fullBook()}
	h := NewCatalogHandler(f)
	got, err := h.GetBook(context.Background(), oas.GetBookParams{ID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if f.id != 7 || !reflect.DeepEqual(got, fullOASBook()) {
		t.Fatalf("id = %d, book = %+v", f.id, got)
	}

	f.err = domain.ErrNotFound
	if got, err := h.GetBook(context.Background(), oas.GetBookParams{ID: 8}); got != nil || err == nil {
		t.Fatalf("got %v, %v; want error", got, err)
	}
}

func TestUpdateBook(t *testing.T) {
	f := &fakeUseCases{book: fullBook()}
	h := NewCatalogHandler(f)
	got, err := h.UpdateBook(context.Background(), &oas.BookUpdate{
		Isbn: "i", Title: "T", Authors: []string{"A"}, Pages: oas.NewOptInt(9), Version: 2,
	}, oas.UpdateBookParams{ID: 7})
	if err != nil {
		t.Fatal(err)
	}
	want := domain.BookDraft{ISBN: "i", Title: "T", Authors: []string{"A"}, Pages: ptr(9)}
	if f.id != 7 || f.version != 2 || !reflect.DeepEqual(f.draft, want) {
		t.Fatalf("id = %d, version = %d, draft = %+v", f.id, f.version, f.draft)
	}
	if !reflect.DeepEqual(got, fullOASBook()) {
		t.Fatalf("book = %+v", got)
	}

	f.err = domain.ErrVersionConflict
	if got, err := h.UpdateBook(context.Background(), &oas.BookUpdate{}, oas.UpdateBookParams{ID: 7}); got != nil || err == nil {
		t.Fatalf("got %v, %v; want error", got, err)
	}
}

func TestDeleteBook(t *testing.T) {
	f := &fakeUseCases{}
	h := NewCatalogHandler(f)
	if err := h.DeleteBook(context.Background(), oas.DeleteBookParams{ID: 7}); err != nil || f.id != 7 {
		t.Fatalf("err = %v, id = %d", err, f.id)
	}
	f.err = domain.ErrNotFound
	if err := h.DeleteBook(context.Background(), oas.DeleteBookParams{ID: 7}); err == nil {
		t.Fatal("want error")
	}
}

func TestMapError(t *testing.T) {
	other := errors.New("mongo: connection refused")
	tests := []struct {
		name   string
		err    error
		status int // 0 = returned unchanged
		msg    string
	}{
		{"validation", &domain.ValidationError{Field: "title", Reason: "must not be empty"}, http.StatusBadRequest, "title: must not be empty"},
		{"wrapped validation", fmt.Errorf("add: %w", &domain.ValidationError{Field: "isbn", Reason: "bad"}), http.StatusBadRequest, "isbn: bad"},
		{"not found", domain.ErrNotFound, http.StatusNotFound, domain.ErrNotFound.Error()},
		{"wrapped not found", fmt.Errorf("mongo find 7: %w", domain.ErrNotFound), http.StatusNotFound, domain.ErrNotFound.Error()},
		{"duplicate isbn", domain.ErrDuplicateISBN, http.StatusConflict, domain.ErrDuplicateISBN.Error()},
		{"wrapped duplicate isbn", fmt.Errorf("index e11000: %w", domain.ErrDuplicateISBN), http.StatusConflict, domain.ErrDuplicateISBN.Error()},
		{"version conflict", domain.ErrVersionConflict, http.StatusConflict, domain.ErrVersionConflict.Error()},
		{"wrapped version conflict", fmt.Errorf("replace: %w", domain.ErrVersionConflict), http.StatusConflict, domain.ErrVersionConflict.Error()},
		{"other", other, 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapError(tt.err)
			if tt.status == 0 {
				if got != other {
					t.Fatalf("got %v, want the error unchanged", got)
				}
				return
			}
			var e *oas.ErrorStatusCode
			if !errors.As(got, &e) {
				t.Fatalf("got %T, want *oas.ErrorStatusCode", got)
			}
			if e.StatusCode != tt.status || e.Response.Error != tt.msg {
				t.Fatalf("got %d %q, want %d %q", e.StatusCode, e.Response.Error, tt.status, tt.msg)
			}
		})
	}
}
