package app

import (
	"context"
	"time"

	"orch-test/backend/internal/catalog/domain"
)

// Service implements UseCases on top of a domain.Repository.
type Service struct {
	repo  domain.Repository
	clock func() time.Time
}

var _ UseCases = (*Service)(nil)

// Option configures a Service.
type Option func(*Service)

// WithClock sets the clock the service reads timestamps from (default time.Now).
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.clock = now }
}

// NewService returns a Service that stores books in repo.
func NewService(repo domain.Repository, opts ...Option) *Service {
	s := &Service{repo: repo, clock: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// now reads the clock once per use case. MongoDB stores milliseconds in UTC,
// so a book read back equals the one returned on write.
func (s *Service) now() time.Time {
	return s.clock().UTC().Truncate(time.Millisecond)
}

func (s *Service) AddBook(ctx context.Context, draft domain.BookDraft) (domain.Book, error) {
	b, err := domain.NewBook(draft, s.now())
	if err != nil {
		return domain.Book{}, err
	}
	return s.repo.Add(ctx, b)
}

func (s *Service) GetBook(ctx context.Context, id domain.BookID) (domain.Book, error) {
	return s.repo.Get(ctx, id)
}

func (s *Service) ListBooks(ctx context.Context, q ListQuery) (BookPage, error) {
	page, err := domain.NewPage(q.Limit, q.Offset)
	if err != nil {
		return BookPage{}, err
	}
	filter, err := domain.NewListFilter(q.Author)
	if err != nil {
		return BookPage{}, err
	}
	items, total, err := s.repo.List(ctx, filter, page)
	if err != nil {
		return BookPage{}, err
	}
	if items == nil {
		items = []domain.Book{}
	}
	return BookPage{Items: items, Total: total}, nil
}

func (s *Service) ReviseBook(ctx context.Context, id domain.BookID, draft domain.BookDraft, version int64) (domain.Book, error) {
	b, err := s.repo.Get(ctx, id)
	if err != nil {
		return domain.Book{}, err
	}
	if err := b.Revise(draft, version, s.now()); err != nil {
		return domain.Book{}, err
	}
	if err := s.repo.Update(ctx, b, version); err != nil {
		return domain.Book{}, err
	}
	return b, nil
}

func (s *Service) RemoveBook(ctx context.Context, id domain.BookID) error {
	return s.repo.Remove(ctx, id)
}
