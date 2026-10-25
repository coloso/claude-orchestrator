package app

import (
	"context"
	"errors"
	"fmt"

	"orch-test/backend/internal/catalog/domain"
)

// SeedReport tells what SeedDemo did.
type SeedReport struct {
	Seeded  []domain.Book // the books added, in demo-catalog order, with their IDs
	Skipped []domain.ISBN // the demo books already in the catalog
}

// SeedDemo adds every demo book whose ISBN is not in the catalog yet, through
// AddBook, so every demo book passes the domain rules. It is idempotent.
//
// Seeding decides by ISBN only; there are no tombstones. A demo book that was
// removed is added again, a demo book that was revised but kept its ISBN is
// left as it is, and a demo book whose ISBN was changed by an edit is seeded
// again.
//
// FindByISBN runs before AddBook so that existing books don't consume an ID
// (with MongoDB a failed Add still burns a counter value). If AddBook reports
// ErrDuplicateISBN anyway (another instance seeded the book at the same
// moment), the book counts as skipped. Any other error stops seeding and is
// returned, wrapped with the ISBN, together with the report so far.
//
// SeedDemo is not part of UseCases because it is not an HTTP operation. It
// doesn't log; the caller does.
func (s *Service) SeedDemo(ctx context.Context) (SeedReport, error) {
	var rep SeedReport
	for _, draft := range domain.DemoCatalog() {
		isbn, err := domain.ParseISBN(draft.ISBN)
		if err != nil {
			return rep, fmt.Errorf("seed demo book %s: %w", draft.ISBN, err)
		}
		_, err = s.repo.FindByISBN(ctx, isbn)
		switch {
		case err == nil:
			rep.Skipped = append(rep.Skipped, isbn)
			continue
		case !errors.Is(err, domain.ErrNotFound):
			return rep, fmt.Errorf("seed demo book %s: %w", isbn, err)
		}
		book, err := s.AddBook(ctx, draft)
		switch {
		case err == nil:
			rep.Seeded = append(rep.Seeded, book)
		case errors.Is(err, domain.ErrDuplicateISBN):
			rep.Skipped = append(rep.Skipped, isbn)
		default:
			return rep, fmt.Errorf("seed demo book %s: %w", isbn, err)
		}
	}
	return rep, nil
}
