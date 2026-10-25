package memory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"orch-test/backend/internal/catalog/domain"
)

var (
	ctx = context.Background()
	t0  = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
)

// isbnFor returns a valid ISBN-13 (978 + 9 digits derived from n + check digit).
func isbnFor(t *testing.T, n int) domain.ISBN {
	t.Helper()
	body := fmt.Sprintf("978%09d", n)
	sum := 0
	for i, c := range body {
		w := 1
		if i%2 == 1 {
			w = 3
		}
		sum += int(c-'0') * w
	}
	isbn, err := domain.ParseISBN(fmt.Sprintf("%s%d", body, (10-sum%10)%10))
	if err != nil {
		t.Fatal(err)
	}
	return isbn
}

func newBook(t *testing.T, n int, authors ...string) domain.Book {
	t.Helper()
	if len(authors) == 0 {
		authors = []string{"Author"}
	}
	b, err := domain.NewBook(domain.BookDraft{ISBN: isbnFor(t, n).String(), Title: fmt.Sprintf("Book %d", n), Authors: authors}, t0)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustAdd(t *testing.T, r *Repository, b domain.Book) domain.Book {
	t.Helper()
	stored, err := r.Add(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestAddAssignsIncreasingIDs(t *testing.T) {
	r := NewRepository()
	b := newBook(t, 1)
	b.ID = 99 // ignored
	first := mustAdd(t, r, b)
	second := mustAdd(t, r, newBook(t, 2))
	if first.ID != 1 || second.ID != 2 {
		t.Fatalf("IDs = %d, %d, want 1, 2", first.ID, second.ID)
	}
	b.ID = first.ID
	if !reflect.DeepEqual(first, b) {
		t.Fatalf("Add returned %+v, want %+v", first, b)
	}
}

func TestAddDuplicateISBN(t *testing.T) {
	r := NewRepository()
	mustAdd(t, r, newBook(t, 1))
	if _, err := r.Add(ctx, newBook(t, 1)); !errors.Is(err, domain.ErrDuplicateISBN) {
		t.Fatalf("err = %v, want ErrDuplicateISBN", err)
	}
	// a failed Add does not consume an ID
	if got := mustAdd(t, r, newBook(t, 2)); got.ID != 2 {
		t.Fatalf("ID = %d, want 2", got.ID)
	}
	if _, total, _ := r.List(ctx, domain.ListFilter{}, domain.Page{Limit: 10}); total != 2 {
		t.Fatalf("total = %d, want 2", total)
	}
}

func TestIDsAreNotReusedAfterRemove(t *testing.T) {
	r := NewRepository()
	mustAdd(t, r, newBook(t, 1))
	last := mustAdd(t, r, newBook(t, 2))
	if err := r.Remove(ctx, last.ID); err != nil {
		t.Fatal(err)
	}
	if got := mustAdd(t, r, newBook(t, 3)); got.ID != 3 {
		t.Fatalf("ID = %d, want 3 (IDs are never reused)", got.ID)
	}
	// the ISBN of a removed book is free again
	if got := mustAdd(t, r, newBook(t, 2)); got.ID != 4 {
		t.Fatalf("ID = %d, want 4", got.ID)
	}
}

func TestGetAndFindByISBN(t *testing.T) {
	r := NewRepository()
	stored := mustAdd(t, r, newBook(t, 1))

	got, err := r.Get(ctx, stored.ID)
	if err != nil || !reflect.DeepEqual(got, stored) {
		t.Fatalf("Get = %+v, %v; want %+v", got, err, stored)
	}
	got, err = r.FindByISBN(ctx, stored.ISBN)
	if err != nil || !reflect.DeepEqual(got, stored) {
		t.Fatalf("FindByISBN = %+v, %v; want %+v", got, err, stored)
	}
	if _, err := r.Get(ctx, 42); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get err = %v, want ErrNotFound", err)
	}
	if _, err := r.FindByISBN(ctx, isbnFor(t, 42)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("FindByISBN err = %v, want ErrNotFound", err)
	}
}

func TestListPagingAndOrder(t *testing.T) {
	r := NewRepository()
	for i := 1; i <= 5; i++ {
		mustAdd(t, r, newBook(t, i))
	}
	ids := func(books []domain.Book) []domain.BookID {
		out := []domain.BookID{}
		for _, b := range books {
			out = append(out, b.ID)
		}
		return out
	}
	tests := []struct {
		name string
		page domain.Page
		want []domain.BookID
	}{
		{"first page", domain.Page{Limit: 2}, []domain.BookID{1, 2}},
		{"second page", domain.Page{Limit: 2, Offset: 2}, []domain.BookID{3, 4}},
		{"partial last page", domain.Page{Limit: 2, Offset: 4}, []domain.BookID{5}},
		{"offset at end", domain.Page{Limit: 2, Offset: 5}, []domain.BookID{}},
		{"offset past end", domain.Page{Limit: 2, Offset: 50}, []domain.BookID{}},
		{"limit larger than total", domain.Page{Limit: 100}, []domain.BookID{1, 2, 3, 4, 5}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := r.List(ctx, domain.ListFilter{}, tc.page)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil {
				t.Fatal("items must not be nil")
			}
			if total != 5 {
				t.Fatalf("total = %d, want 5", total)
			}
			if !reflect.DeepEqual(ids(got), tc.want) {
				t.Fatalf("ids = %v, want %v", ids(got), tc.want)
			}
		})
	}
}

func TestListEmptyCatalog(t *testing.T) {
	got, total, err := NewRepository().List(ctx, domain.ListFilter{}, domain.Page{Limit: 10})
	if err != nil || got == nil || len(got) != 0 || total != 0 {
		t.Fatalf("List = %v (nil=%t), %d, %v", got, got == nil, total, err)
	}
}

func TestListFilterByAuthor(t *testing.T) {
	r := NewRepository()
	mustAdd(t, r, newBook(t, 1, "Ann", "Bob"))
	mustAdd(t, r, newBook(t, 2, "Bob"))
	mustAdd(t, r, newBook(t, 3, "Ann"))
	mustAdd(t, r, newBook(t, 4, "ann"))
	mustAdd(t, r, newBook(t, 5, "Annabel"))

	got, total, err := r.List(ctx, domain.ListFilter{Author: "Ann"}, domain.Page{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("total = %d, want 2 (exact, case-sensitive match)", total)
	}
	if len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("items = %+v, want book 3", got)
	}
	got, total, _ = r.List(ctx, domain.ListFilter{Author: "Nobody"}, domain.Page{Limit: 10})
	if got == nil || len(got) != 0 || total != 0 {
		t.Fatalf("List(Nobody) = %v, %d", got, total)
	}
}

func TestUpdate(t *testing.T) {
	r := NewRepository()
	stored := mustAdd(t, r, newBook(t, 1))

	revised := stored
	if err := revised.Revise(domain.BookDraft{ISBN: isbnFor(t, 9).String(), Title: "Changed", Authors: []string{"New"}}, 1, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := r.Update(ctx, revised, 1); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, stored.ID)
	if err != nil || !reflect.DeepEqual(got, revised) {
		t.Fatalf("Get = %+v, %v; want %+v", got, err, revised)
	}
	// the ISBN index follows the change
	if _, err := r.FindByISBN(ctx, stored.ISBN); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("old ISBN still found: %v", err)
	}
	if got, err := r.FindByISBN(ctx, revised.ISBN); err != nil || got.ID != stored.ID {
		t.Fatalf("FindByISBN(new) = %+v, %v", got, err)
	}
	// the old ISBN is free for another book
	mustAdd(t, r, newBook(t, 1))
}

func TestUpdateKeepingOwnISBN(t *testing.T) {
	r := NewRepository()
	stored := mustAdd(t, r, newBook(t, 1))
	revised := stored
	if err := revised.Revise(domain.BookDraft{ISBN: stored.ISBN.String(), Title: "Changed", Authors: []string{"A"}}, 1, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := r.Update(ctx, revised, 1); err != nil {
		t.Fatalf("Update with own ISBN: %v", err)
	}
}

func TestUpdateErrors(t *testing.T) {
	r := NewRepository()
	first := mustAdd(t, r, newBook(t, 1))
	second := mustAdd(t, r, newBook(t, 2))

	changed := func(b domain.Book, isbn domain.ISBN) domain.Book {
		b = clone(b)
		b.ISBN, b.Title, b.Version = isbn, "Changed", b.Version+1
		return b
	}
	tests := []struct {
		name     string
		book     domain.Book
		expected int64
		want     error
	}{
		{"not found", changed(domain.Book{ID: 42, ISBN: isbnFor(t, 42), Version: 1}, isbnFor(t, 42)), 1, domain.ErrNotFound},
		{"stale version", changed(second, second.ISBN), 7, domain.ErrVersionConflict},
		{"duplicate ISBN", changed(second, first.ISBN), 1, domain.ErrDuplicateISBN},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := r.Update(ctx, tc.book, tc.expected); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			// nothing changed
			for _, want := range []domain.Book{first, second} {
				if got, _ := r.Get(ctx, want.ID); !reflect.DeepEqual(got, want) {
					t.Fatalf("book %d changed: %+v", want.ID, got)
				}
			}
			if got, err := r.FindByISBN(ctx, second.ISBN); err != nil || got.ID != second.ID {
				t.Fatalf("ISBN index changed: %+v, %v", got, err)
			}
		})
	}
}

func TestRemove(t *testing.T) {
	r := NewRepository()
	stored := mustAdd(t, r, newBook(t, 1))
	if err := r.Remove(ctx, stored.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(ctx, stored.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get after Remove: %v", err)
	}
	if _, err := r.FindByISBN(ctx, stored.ISBN); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("FindByISBN after Remove: %v", err)
	}
	if err := r.Remove(ctx, stored.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second Remove: %v", err)
	}
}

func intp(i int) *int       { return &i }
func strp(s string) *string { return &s }

func TestStoredBooksAreNotAliased(t *testing.T) {
	r := NewRepository()
	in := newBook(t, 1, "Ann")
	in.PublishedYear, in.Language, in.Pages = intp(2000), strp("en"), intp(100)
	want := in
	want.ID = 1
	want = clone(want)

	stored := mustAdd(t, r, in)
	mutate := func(b domain.Book) {
		b.Authors[0] = "X"
		*b.PublishedYear, *b.Language, *b.Pages = 1, "xx", 1
	}
	mutate(in)     // the input of Add
	mutate(stored) // the result of Add

	check := func(label string, got domain.Book) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: stored book was mutated through an alias: %+v", label, got)
		}
	}
	got, _ := r.Get(ctx, 1)
	check("Get", got)
	mutate(got) // the result of Get
	got, _ = r.FindByISBN(ctx, stored.ISBN)
	check("FindByISBN", got)
	mutate(got)
	list, _, _ := r.List(ctx, domain.ListFilter{}, domain.Page{Limit: 10})
	check("List", list[0])
	mutate(list[0])

	// the input of Update
	upd, _ := r.Get(ctx, 1)
	upd.Version = 2
	if err := r.Update(ctx, upd, 1); err != nil {
		t.Fatal(err)
	}
	want.Version = 2
	mutate(upd)
	got, _ = r.Get(ctx, 1)
	check("Get after Update", got)
}

func TestConcurrentUse(t *testing.T) {
	r := NewRepository()
	const n = 50
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := newBook(t, i)
			stored, err := r.Add(ctx, b)
			if err != nil {
				t.Error(err)
				return
			}
			_, _, _ = r.List(ctx, domain.ListFilter{}, domain.Page{Limit: 10})
			_, _ = r.Get(ctx, stored.ID)
		}()
	}
	wg.Wait()
	_, total, _ := r.List(ctx, domain.ListFilter{}, domain.Page{Limit: 1})
	if total != n {
		t.Fatalf("total = %d, want %d", total, n)
	}
	seen := map[domain.BookID]bool{}
	all, _, _ := r.List(ctx, domain.ListFilter{}, domain.Page{Limit: 100})
	for _, b := range all {
		if seen[b.ID] || b.ID < 1 || b.ID > n {
			t.Fatalf("bad or duplicate ID %d", b.ID)
		}
		seen[b.ID] = true
	}
}
