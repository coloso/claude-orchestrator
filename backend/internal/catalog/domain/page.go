package domain

const (
	// DefaultLimit is the page size when a client gives none.
	DefaultLimit = 20
	// MaxLimit is the largest page size.
	MaxLimit = 100
)

// Page selects a window of a list: Limit items, after skipping Offset.
type Page struct{ Limit, Offset int }

// NewPage validates limit (1..MaxLimit) and offset (>= 0).
func NewPage(limit, offset int) (Page, error) {
	if limit < 1 || limit > MaxLimit {
		return Page{}, &ValidationError{Field: "limit", Reason: "must be between 1 and 100"}
	}
	if offset < 0 {
		return Page{}, &ValidationError{Field: "offset", Reason: "must not be negative"}
	}
	return Page{Limit: limit, Offset: offset}, nil
}

// ListFilter narrows a list. The zero value matches all books.
type ListFilter struct {
	Author string // "" = all books, else books with an author exactly equal to Author
}

// NewListFilter validates author. "" means no filter. Any other value is kept
// as given, not trimmed, and must be plain text (see isPlainText).
func NewListFilter(author string) (ListFilter, error) {
	if !isPlainText(author) {
		return ListFilter{}, &ValidationError{Field: "author", Reason: "must be valid UTF-8 without control characters"}
	}
	return ListFilter{Author: author}, nil
}
