package domain

import "context"

// Repository stores the books of the catalog (outbound port).
type Repository interface {
	// Add stores b (b.ID is ignored), assigns the next ID and returns the stored book. IDs are > 0,
	// increasing and never reused; gaps are allowed. ErrDuplicateISBN if another book has b.ISBN (then nothing is stored).
	Add(ctx context.Context, b Book) (Book, error)
	Get(ctx context.Context, id BookID) (Book, error)        // ErrNotFound
	FindByISBN(ctx context.Context, isbn ISBN) (Book, error) // ErrNotFound
	// List returns one page of the books matching f, sorted by ID ascending (never nil), and the number of all matching books.
	List(ctx context.Context, f ListFilter, p Page) ([]Book, int64, error)
	// Update replaces the stored book b.ID with b if its stored version is still expectedVersion.
	// ErrNotFound, ErrVersionConflict, or ErrDuplicateISBN (another book has b.ISBN); on error nothing changes.
	Update(ctx context.Context, b Book, expectedVersion int64) error
	Remove(ctx context.Context, id BookID) error // ErrNotFound
}
