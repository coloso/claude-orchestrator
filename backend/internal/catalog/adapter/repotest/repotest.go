// Package repotest is the contract suite of domain.Repository. Every repository
// adapter runs it from its own tests, so all adapters behave the same.
package repotest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"orch-test/backend/internal/catalog/domain"
)

// now is a fixed UTC time with millisecond precision (MongoDB stores milliseconds).
var now = time.Date(2026, 1, 2, 3, 4, 5, 678_000_000, time.UTC).Truncate(time.Millisecond)

// Run runs the contract suite. newRepo returns a fresh, empty repository for each subtest;
// it may register cleanups.
func Run(t *testing.T, newRepo func(t *testing.T) domain.Repository) {
	cases := []struct {
		name string
		run  func(t *testing.T, r domain.Repository)
	}{
		{"add stores the book and Get returns it", testAdd},
		{"add keeps optional fields nil", testAddOptionalNil},
		{"add assigns increasing IDs", testAddIncreasingIDs},
		{"add with a duplicate ISBN fails", testAddDuplicateISBN},
		{"FindByISBN", testFindByISBN},
		{"get with an unknown ID fails", testGetUnknown},
		{"list of an empty repository", testListEmpty},
		{"list is sorted by ID", testListSorted},
		{"list pages with limit and offset", testListPaging},
		{"list filters by exact author", testListAuthorFilter},
		{"update with the stored version", testUpdate},
		{"update with a stale version fails", testUpdateStale},
		{"update with an unknown ID fails", testUpdateUnknown},
		{"update to another book's ISBN fails", testUpdateDuplicateISBN},
		{"update to a new ISBN frees the old one", testUpdateFreesISBN},
		{"remove", testRemove},
		{"remove with an unknown ID fails", testRemoveUnknown},
		{"IDs are never reused after remove", testIDsNotReused},
		{"concurrent updates: exactly one wins", testConcurrentUpdate},
		{"concurrent adds get distinct IDs", testConcurrentAdd},
		{"returned books do not alias stored state", testNoAliasing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.run(t, newRepo(t))
		})
	}
}

// isbnFor returns a valid ISBN-13: 978 + 9 digits derived from n + the check digit.
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
		t.Fatalf("isbnFor(%d): %v", n, err)
	}
	return isbn
}

func newBook(t *testing.T, n int, authors ...string) domain.Book {
	t.Helper()
	if len(authors) == 0 {
		authors = []string{"Author"}
	}
	b, err := domain.NewBook(domain.BookDraft{
		ISBN:    isbnFor(t, n).String(),
		Title:   fmt.Sprintf("Book %d", n),
		Authors: authors,
	}, now)
	if err != nil {
		t.Fatalf("NewBook: %v", err)
	}
	return b
}

// fullBook is a book with all optional fields set.
func fullBook(t *testing.T, n int) domain.Book {
	t.Helper()
	year, lang, pages := 1999, "de", 321
	b, err := domain.NewBook(domain.BookDraft{
		ISBN:          isbnFor(t, n).String(),
		Title:         "Full Book",
		Authors:       []string{"Ada Lovelace", "Alan Turing"},
		PublishedYear: &year,
		Language:      &lang,
		Pages:         &pages,
	}, now)
	if err != nil {
		t.Fatalf("NewBook: %v", err)
	}
	return b
}

func draftOf(b domain.Book) domain.BookDraft {
	return domain.BookDraft{
		ISBN:          b.ISBN.String(),
		Title:         b.Title,
		Authors:       slices.Clone(b.Authors),
		PublishedYear: b.PublishedYear,
		Language:      b.Language,
		Pages:         b.Pages,
	}
}

// revised returns a copy of b revised with draft at b's version. It fails the test if the draft is invalid.
func revised(t *testing.T, b domain.Book, draft domain.BookDraft, at time.Time) domain.Book {
	t.Helper()
	if err := b.Revise(draft, b.Version, at); err != nil {
		t.Fatalf("Revise: %v", err)
	}
	return b
}

func mustAdd(t *testing.T, r domain.Repository, b domain.Book) domain.Book {
	t.Helper()
	stored, err := r.Add(context.Background(), b)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	return stored
}

func mustGet(t *testing.T, r domain.Repository, id domain.BookID) domain.Book {
	t.Helper()
	b, err := r.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("Get(%d): %v", id, err)
	}
	return b
}

func total(t *testing.T, r domain.Repository) int64 {
	t.Helper()
	_, n, err := r.List(context.Background(), domain.ListFilter{}, domain.Page{Limit: domain.MaxLimit})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return n
}

func ids(books []domain.Book) []domain.BookID {
	out := make([]domain.BookID, len(books))
	for i, b := range books {
		out[i] = b.ID
	}
	return out
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func ptrEqual[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// assertSame fails unless got and want are equal in every field (times via Equal).
func assertSame(t *testing.T, got, want domain.Book) {
	t.Helper()
	ok := got.ID == want.ID &&
		got.ISBN == want.ISBN &&
		got.Title == want.Title &&
		slices.Equal(got.Authors, want.Authors) &&
		ptrEqual(got.PublishedYear, want.PublishedYear) &&
		ptrEqual(got.Language, want.Language) &&
		ptrEqual(got.Pages, want.Pages) &&
		got.CreatedAt.Equal(want.CreatedAt) &&
		got.UpdatedAt.Equal(want.UpdatedAt) &&
		got.Version == want.Version
	if !ok {
		t.Fatalf("book = %+v, want %+v", got, want)
	}
}

func testAdd(t *testing.T, r domain.Repository) {
	in := fullBook(t, 1)
	in.ID = 99 // ignored by Add
	stored := mustAdd(t, r, in)
	if stored.ID <= 0 {
		t.Fatalf("ID = %d, want > 0", stored.ID)
	}
	want := in
	want.ID = stored.ID
	assertSame(t, stored, want)
	assertSame(t, mustGet(t, r, stored.ID), want)
}

func testAddOptionalNil(t *testing.T, r domain.Repository) {
	stored := mustAdd(t, r, newBook(t, 1))
	got := mustGet(t, r, stored.ID)
	if got.PublishedYear != nil || got.Language != nil || got.Pages != nil {
		t.Fatalf("optional fields = %v, %v, %v, want all nil", got.PublishedYear, got.Language, got.Pages)
	}
	assertSame(t, got, stored)
}

func testAddIncreasingIDs(t *testing.T, r domain.Repository) {
	var last domain.BookID
	for n := 1; n <= 5; n++ {
		b := mustAdd(t, r, newBook(t, n))
		if b.ID <= last {
			t.Fatalf("ID %d is not greater than the previous ID %d", b.ID, last)
		}
		last = b.ID
	}
}

func testAddDuplicateISBN(t *testing.T, r domain.Repository) {
	first := mustAdd(t, r, newBook(t, 1))
	dup := newBook(t, 1)
	dup.Title = "Other title"
	_, err := r.Add(context.Background(), dup)
	wantErr(t, err, domain.ErrDuplicateISBN)
	if n := total(t, r); n != 1 {
		t.Fatalf("total = %d, want 1", n)
	}
	assertSame(t, mustGet(t, r, first.ID), first)
}

func testFindByISBN(t *testing.T, r domain.Repository) {
	mustAdd(t, r, newBook(t, 1))
	want := mustAdd(t, r, fullBook(t, 2))
	got, err := r.FindByISBN(context.Background(), want.ISBN)
	if err != nil {
		t.Fatalf("FindByISBN: %v", err)
	}
	assertSame(t, got, want)

	_, err = r.FindByISBN(context.Background(), isbnFor(t, 3))
	wantErr(t, err, domain.ErrNotFound)
}

func testGetUnknown(t *testing.T, r domain.Repository) {
	_, err := r.Get(context.Background(), 12345)
	wantErr(t, err, domain.ErrNotFound)
}

func testListEmpty(t *testing.T, r domain.Repository) {
	items, n, err := r.List(context.Background(), domain.ListFilter{}, domain.Page{Limit: 20})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if items == nil || len(items) != 0 || n != 0 {
		t.Fatalf("List = %#v, %d; want a non-nil empty slice, 0", items, n)
	}
}

func testListSorted(t *testing.T, r domain.Repository) {
	for n := 1; n <= 5; n++ {
		mustAdd(t, r, newBook(t, 10-n)) // ISBNs descend, IDs ascend
	}
	items, n, err := r.List(context.Background(), domain.ListFilter{}, domain.Page{Limit: 20})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if n != 5 || len(items) != 5 {
		t.Fatalf("List returned %d items, total %d; want 5, 5", len(items), n)
	}
	if !slices.IsSortedFunc(items, func(a, b domain.Book) int { return int(a.ID - b.ID) }) {
		t.Fatalf("IDs = %v, want ascending", ids(items))
	}
}

func testListPaging(t *testing.T, r domain.Repository) {
	var all []domain.Book
	for n := 1; n <= 25; n++ {
		all = append(all, mustAdd(t, r, newBook(t, n)))
	}
	ctx := context.Background()

	items, n, err := r.List(ctx, domain.ListFilter{}, domain.Page{Limit: 20})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if n != 25 || !slices.Equal(ids(items), ids(all[:20])) {
		t.Fatalf("limit 20: IDs %v, total %d; want %v, 25", ids(items), n, ids(all[:20]))
	}
	assertSame(t, items[0], all[0])

	items, n, err = r.List(ctx, domain.ListFilter{}, domain.Page{Limit: 20, Offset: 20})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if n != 25 || !slices.Equal(ids(items), ids(all[20:])) {
		t.Fatalf("offset 20: IDs %v, total %d; want %v, 25", ids(items), n, ids(all[20:]))
	}

	items, n, err = r.List(ctx, domain.ListFilter{}, domain.Page{Limit: 20, Offset: 30})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if items == nil || len(items) != 0 || n != 25 {
		t.Fatalf("offset 30: %#v, total %d; want a non-nil empty slice, 25", items, n)
	}
}

func testListAuthorFilter(t *testing.T, r domain.Repository) {
	a := mustAdd(t, r, newBook(t, 1, "Ann"))
	both := mustAdd(t, r, newBook(t, 2, "Bob", "Ann"))
	mustAdd(t, r, newBook(t, 3, "Annabel"))
	mustAdd(t, r, newBook(t, 4, "Joann"))
	mustAdd(t, r, newBook(t, 5, "ann"))
	bob := mustAdd(t, r, newBook(t, 6, "Bob"))
	ctx := context.Background()

	items, n, err := r.List(ctx, domain.ListFilter{Author: "Ann"}, domain.Page{Limit: 20})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []domain.BookID{a.ID, both.ID}; n != 2 || !slices.Equal(ids(items), want) {
		t.Fatalf("author Ann: IDs %v, total %d; want %v, 2", ids(items), n, want)
	}

	items, n, err = r.List(ctx, domain.ListFilter{Author: "Bob"}, domain.Page{Limit: 20})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []domain.BookID{both.ID, bob.ID}; n != 2 || !slices.Equal(ids(items), want) {
		t.Fatalf("author Bob: IDs %v, total %d; want %v, 2", ids(items), n, want)
	}

	// total counts all matches, not the page
	items, n, err = r.List(ctx, domain.ListFilter{Author: "Bob"}, domain.Page{Limit: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if n != 2 || !slices.Equal(ids(items), []domain.BookID{both.ID}) {
		t.Fatalf("author Bob, limit 1: IDs %v, total %d; want [%d], 2", ids(items), n, both.ID)
	}

	items, n, err = r.List(ctx, domain.ListFilter{Author: "Nobody"}, domain.Page{Limit: 20})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if items == nil || len(items) != 0 || n != 0 {
		t.Fatalf("author Nobody: %#v, total %d; want a non-nil empty slice, 0", items, n)
	}
}

func testUpdate(t *testing.T, r domain.Repository) {
	stored := mustAdd(t, r, fullBook(t, 1))
	later := now.Add(time.Hour)
	year := 2001
	draft := draftOf(stored)
	draft.Title = "Revised"
	draft.Authors = []string{"Grace Hopper"}
	draft.PublishedYear = &year
	draft.Language = nil // removed
	draft.Pages = nil    // removed
	next := revised(t, stored, draft, later)

	if err := r.Update(context.Background(), next, stored.Version); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := mustGet(t, r, stored.ID)
	assertSame(t, got, next)
	if got.Version != stored.Version+1 || got.Language != nil || got.Pages != nil {
		t.Fatalf("book = %+v, want version %d and no language and pages", got, stored.Version+1)
	}
	if !got.CreatedAt.Equal(now) || !got.UpdatedAt.Equal(later) {
		t.Fatalf("times = %v, %v; want %v, %v", got.CreatedAt, got.UpdatedAt, now, later)
	}
}

func testUpdateStale(t *testing.T, r domain.Repository) {
	stored := mustAdd(t, r, newBook(t, 1))
	draft := draftOf(stored)
	draft.Title = "Revised"
	next := revised(t, stored, draft, now.Add(time.Hour))

	err := r.Update(context.Background(), next, stored.Version+5)
	wantErr(t, err, domain.ErrVersionConflict)
	assertSame(t, mustGet(t, r, stored.ID), stored)
}

func testUpdateUnknown(t *testing.T, r domain.Repository) {
	b := newBook(t, 1)
	b.ID = 4242
	wantErr(t, r.Update(context.Background(), b, b.Version), domain.ErrNotFound)
	if n := total(t, r); n != 0 {
		t.Fatalf("total = %d, want 0", n)
	}
}

func testUpdateDuplicateISBN(t *testing.T, r domain.Repository) {
	first := mustAdd(t, r, newBook(t, 1))
	second := mustAdd(t, r, newBook(t, 2))
	draft := draftOf(second)
	draft.ISBN = first.ISBN.String()
	draft.Title = "Revised"
	next := revised(t, second, draft, now.Add(time.Hour))

	err := r.Update(context.Background(), next, second.Version)
	wantErr(t, err, domain.ErrDuplicateISBN)
	assertSame(t, mustGet(t, r, second.ID), second)
	assertSame(t, mustGet(t, r, first.ID), first)
	found, err := r.FindByISBN(context.Background(), first.ISBN)
	if err != nil || found.ID != first.ID {
		t.Fatalf("FindByISBN = %+v, %v; want book %d", found, err, first.ID)
	}
}

func testUpdateFreesISBN(t *testing.T, r domain.Repository) {
	stored := mustAdd(t, r, newBook(t, 1))
	draft := draftOf(stored)
	draft.ISBN = isbnFor(t, 2).String()
	next := revised(t, stored, draft, now.Add(time.Hour))
	if err := r.Update(context.Background(), next, stored.Version); err != nil {
		t.Fatalf("Update: %v", err)
	}

	again := mustAdd(t, r, newBook(t, 1)) // the old ISBN is free
	if again.ID == stored.ID {
		t.Fatalf("ID %d was handed out twice", again.ID)
	}
	moved, err := r.FindByISBN(context.Background(), next.ISBN)
	if err != nil || moved.ID != stored.ID {
		t.Fatalf("FindByISBN(new) = %+v, %v; want book %d", moved, err, stored.ID)
	}
	old, err := r.FindByISBN(context.Background(), isbnFor(t, 1))
	if err != nil || old.ID != again.ID {
		t.Fatalf("FindByISBN(old) = %+v, %v; want book %d", old, err, again.ID)
	}
}

func testRemove(t *testing.T, r domain.Repository) {
	keep := mustAdd(t, r, newBook(t, 1))
	gone := mustAdd(t, r, newBook(t, 2))
	if err := r.Remove(context.Background(), gone.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	_, err := r.Get(context.Background(), gone.ID)
	wantErr(t, err, domain.ErrNotFound)
	_, err = r.FindByISBN(context.Background(), gone.ISBN)
	wantErr(t, err, domain.ErrNotFound)
	assertSame(t, mustGet(t, r, keep.ID), keep)
	if n := total(t, r); n != 1 {
		t.Fatalf("total = %d, want 1", n)
	}
}

func testRemoveUnknown(t *testing.T, r domain.Repository) {
	wantErr(t, r.Remove(context.Background(), 777), domain.ErrNotFound)
}

func testIDsNotReused(t *testing.T, r domain.Repository) {
	var maxID domain.BookID
	var last domain.Book
	for n := 1; n <= 3; n++ {
		last = mustAdd(t, r, newBook(t, n))
		maxID = max(maxID, last.ID)
	}
	if err := r.Remove(context.Background(), last.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	next := mustAdd(t, r, newBook(t, 4))
	if next.ID <= maxID {
		t.Fatalf("ID = %d, want > %d (IDs are never reused)", next.ID, maxID)
	}
}

func testConcurrentUpdate(t *testing.T, r domain.Repository) {
	const workers = 10
	stored := mustAdd(t, r, newBook(t, 1))
	errs := make([]error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range workers {
		draft := draftOf(stored)
		draft.Title = fmt.Sprintf("Title %d", i)
		next := revised(t, stored, draft, now.Add(time.Duration(i+1)*time.Minute))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = r.Update(context.Background(), next, stored.Version)
		}()
	}
	close(start)
	wg.Wait()

	winners := 0
	for _, err := range errs {
		switch {
		case err == nil:
			winners++
		case !errors.Is(err, domain.ErrVersionConflict):
			t.Fatalf("err = %v, want nil or ErrVersionConflict", err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d updates succeeded, want exactly 1 (errors: %v)", winners, errs)
	}
	if got := mustGet(t, r, stored.ID); got.Version != stored.Version+1 {
		t.Fatalf("version = %d, want %d", got.Version, stored.Version+1)
	}
}

func testConcurrentAdd(t *testing.T, r domain.Repository) {
	const workers = 10
	books := make([]domain.Book, workers)
	for i := range workers {
		books[i] = newBook(t, i+1)
	}
	got := make([]domain.Book, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got[i], errs[i] = r.Add(context.Background(), books[i])
		}()
	}
	close(start)
	wg.Wait()

	seen := map[domain.BookID]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
		if got[i].ID <= 0 || seen[got[i].ID] {
			t.Fatalf("ID %d is not positive and distinct (all: %v)", got[i].ID, ids(got))
		}
		seen[got[i].ID] = true
	}
	if n := total(t, r); n != workers {
		t.Fatalf("total = %d, want %d", n, workers)
	}
}

func testNoAliasing(t *testing.T, r domain.Repository) {
	in := fullBook(t, 1)
	stored := mustAdd(t, r, in)
	want := mustGet(t, r, stored.ID)

	// mutating what Add returned, what was passed in, and what Get/List/FindByISBN return
	// must not change the stored book
	stored.Authors[0] = "Mallory"
	*stored.Pages = 1
	in.Authors[1] = "Mallory"
	*in.Language = "xx"

	got := mustGet(t, r, want.ID)
	got.Authors[0] = "Mallory"
	*got.PublishedYear = 1

	items, _, err := r.List(context.Background(), domain.ListFilter{}, domain.Page{Limit: 20})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	items[0].Authors[0] = "Mallory"
	*items[0].Pages = 1

	byISBN, err := r.FindByISBN(context.Background(), want.ISBN)
	if err != nil {
		t.Fatalf("FindByISBN: %v", err)
	}
	byISBN.Authors[1] = "Mallory"

	assertSame(t, mustGet(t, r, want.ID), want)
}
