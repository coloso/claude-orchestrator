package domain

import "errors"

var (
	// ErrInvalid matches every *ValidationError via errors.Is.
	ErrInvalid = errors.New("invalid input")
	// ErrNotFound means there is no book with the requested ID or ISBN.
	ErrNotFound = errors.New("book not found")
	// ErrDuplicateISBN means another book in the catalog already has the ISBN.
	ErrDuplicateISBN = errors.New("a book with this ISBN already exists")
	// ErrVersionConflict means the book was changed after the client read it.
	ErrVersionConflict = errors.New("version conflict: the book was changed since it was read")
)

// ValidationError names the invalid field by its API name: isbn, title,
// authors, publishedYear, language, pages, limit, offset.
type ValidationError struct{ Field, Reason string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

// Is makes every ValidationError match ErrInvalid.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }
