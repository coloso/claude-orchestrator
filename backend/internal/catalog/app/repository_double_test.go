package app_test

import (
	"context"
	"slices"
	"sync"

	"orch-test/backend/internal/catalog/domain"
)

// fakeRepository is a test double for domain.Repository. It is not a storage and isn't wired anywhere.
type fakeRepository struct {
	mu    sync.Mutex
	books map[domain.BookID]domain.Book
	seq   domain.BookID
}

var _ domain.Repository = (*fakeRepository)(nil)

func newFakeRepository() *fakeRepository {
	return &fakeRepository{books: make(map[domain.BookID]domain.Book)}
}

// ownerOf returns the ID of the book with the given ISBN. The caller holds the lock.
func (r *fakeRepository) ownerOf(isbn domain.ISBN) (domain.BookID, bool) {
	for id, b := range r.books {
		if b.ISBN == isbn {
			return id, true
		}
	}
	return 0, false
}

func (r *fakeRepository) Add(_ context.Context, b domain.Book) (domain.Book, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, taken := r.ownerOf(b.ISBN); taken {
		return domain.Book{}, domain.ErrDuplicateISBN
	}
	r.seq++
	b = cloneBook(b)
	b.ID = r.seq
	r.books[b.ID] = b
	return cloneBook(b), nil
}

func (r *fakeRepository) Get(_ context.Context, id domain.BookID) (domain.Book, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.books[id]
	if !ok {
		return domain.Book{}, domain.ErrNotFound
	}
	return cloneBook(b), nil
}

func (r *fakeRepository) FindByISBN(_ context.Context, isbn domain.ISBN) (domain.Book, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.ownerOf(isbn)
	if !ok {
		return domain.Book{}, domain.ErrNotFound
	}
	return cloneBook(r.books[id]), nil
}

func (r *fakeRepository) List(_ context.Context, f domain.ListFilter, p domain.Page) ([]domain.Book, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var matches []domain.Book
	for _, b := range r.books {
		if f.Author == "" || slices.Contains(b.Authors, f.Author) {
			matches = append(matches, b)
		}
	}
	slices.SortFunc(matches, func(a, b domain.Book) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	total := int64(len(matches))

	items := []domain.Book{}
	if p.Offset < len(matches) {
		end := min(p.Offset+p.Limit, len(matches))
		for _, b := range matches[p.Offset:end] {
			items = append(items, cloneBook(b))
		}
	}
	return items, total, nil
}

func (r *fakeRepository) Update(_ context.Context, b domain.Book, expectedVersion int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.books[b.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if stored.Version != expectedVersion {
		return domain.ErrVersionConflict
	}
	if owner, taken := r.ownerOf(b.ISBN); taken && owner != b.ID {
		return domain.ErrDuplicateISBN
	}
	r.books[b.ID] = cloneBook(b)
	return nil
}

func (r *fakeRepository) Remove(_ context.Context, id domain.BookID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.books[id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.books, id)
	return nil
}

// cloneBook deep-copies b, so stored books never share memory with callers.
func cloneBook(b domain.Book) domain.Book {
	b.Authors = slices.Clone(b.Authors)
	b.PublishedYear = clonePtr(b.PublishedYear)
	b.Language = clonePtr(b.Language)
	b.Pages = clonePtr(b.Pages)
	return b
}

func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
