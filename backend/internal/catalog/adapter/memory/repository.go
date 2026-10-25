// Package memory is an in-memory catalog repository for tests and STORAGE=memory.
package memory

import (
	"context"
	"slices"
	"sync"

	"orch-test/backend/internal/catalog/domain"
)

// Repository implements domain.Repository. It is safe for concurrent use.
type Repository struct {
	mu     sync.Mutex
	books  map[domain.BookID]domain.Book
	byISBN map[domain.ISBN]domain.BookID
	seq    domain.BookID
}

var _ domain.Repository = (*Repository)(nil)

// NewRepository returns an empty repository.
func NewRepository() *Repository {
	return &Repository{
		books:  make(map[domain.BookID]domain.Book),
		byISBN: make(map[domain.ISBN]domain.BookID),
	}
}

func (r *Repository) Add(_ context.Context, b domain.Book) (domain.Book, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, taken := r.byISBN[b.ISBN]; taken {
		return domain.Book{}, domain.ErrDuplicateISBN
	}
	r.seq++
	b = clone(b)
	b.ID = r.seq
	r.books[b.ID] = b
	r.byISBN[b.ISBN] = b.ID
	return clone(b), nil
}

func (r *Repository) Get(_ context.Context, id domain.BookID) (domain.Book, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.books[id]
	if !ok {
		return domain.Book{}, domain.ErrNotFound
	}
	return clone(b), nil
}

func (r *Repository) FindByISBN(_ context.Context, isbn domain.ISBN) (domain.Book, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byISBN[isbn]
	if !ok {
		return domain.Book{}, domain.ErrNotFound
	}
	return clone(r.books[id]), nil
}

func (r *Repository) List(_ context.Context, f domain.ListFilter, p domain.Page) ([]domain.Book, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var matches []domain.Book
	for _, b := range r.books {
		if f.Author == "" || slices.Contains(b.Authors, f.Author) {
			matches = append(matches, b)
		}
	}
	slices.SortFunc(matches, func(a, b domain.Book) int { return int(a.ID - b.ID) })
	total := int64(len(matches))

	items := []domain.Book{}
	if p.Offset < len(matches) {
		end := min(p.Offset+p.Limit, len(matches))
		for _, b := range matches[p.Offset:end] {
			items = append(items, clone(b))
		}
	}
	return items, total, nil
}

func (r *Repository) Update(_ context.Context, b domain.Book, expectedVersion int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.books[b.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if stored.Version != expectedVersion {
		return domain.ErrVersionConflict
	}
	if owner, taken := r.byISBN[b.ISBN]; taken && owner != b.ID {
		return domain.ErrDuplicateISBN
	}
	if stored.ISBN != b.ISBN {
		delete(r.byISBN, stored.ISBN)
		r.byISBN[b.ISBN] = b.ID
	}
	r.books[b.ID] = clone(b)
	return nil
}

func (r *Repository) Remove(_ context.Context, id domain.BookID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.books[id]
	if !ok {
		return domain.ErrNotFound
	}
	delete(r.books, id)
	delete(r.byISBN, b.ISBN)
	return nil
}

// clone deep-copies b, so stored books never share memory with callers.
func clone(b domain.Book) domain.Book {
	b.Authors = slices.Clone(b.Authors)
	b.PublishedYear = copyPtr(b.PublishedYear)
	b.Language = copyPtr(b.Language)
	b.Pages = copyPtr(b.Pages)
	return b
}

func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
