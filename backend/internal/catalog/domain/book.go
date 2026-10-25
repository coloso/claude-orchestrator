// Package domain is the model of the catalog bounded context.
//
// Glossary (ubiquitous language):
//   - Book: the aggregate. It has an ID, an ISBN, a title, one or more authors,
//     and optionally a published year, a language and a page count. It tracks
//     createdAt, updatedAt and a version.
//   - ISBN: a value object holding an ISBN-13. It is a book's natural key.
//   - Catalog: the set of all books. A client adds a book to it, revises a
//     book, and removes a book from it.
//
// The domain enforces its rules itself, so a request is rejected before it
// reaches the database. Lengths are counted in Unicode code points, as
// MongoDB's $jsonSchema does.
package domain

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// BookID identifies a book. It is > 0 once the repository added the book.
type BookID int64

// BookDraft is a book's editable fields as a client sends them, not
// validated yet.
type BookDraft struct {
	ISBN          string
	Title         string
	Authors       []string
	PublishedYear *int    // nil = not set
	Language      *string // nil = not set
	Pages         *int    // nil = not set
}

// Book is the aggregate. Only NewBook and Revise create or change one;
// repositories rebuild it from storage as is.
type Book struct {
	ID            BookID // 0 until the repository adds the book
	ISBN          ISBN
	Title         string
	Authors       []string
	PublishedYear *int
	Language      *string
	Pages         *int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Version       int64
}

// NewBook validates and normalizes draft. The book has ID 0, Version 1 and
// CreatedAt = UpdatedAt = now. The first invalid field (in the order isbn,
// title, authors, publishedYear, language, pages) is returned as a
// *ValidationError.
func NewBook(draft BookDraft, now time.Time) (Book, error) {
	f, err := normalize(draft)
	if err != nil {
		return Book{}, err
	}
	return Book{
		ISBN:          f.ISBN,
		Title:         f.Title,
		Authors:       f.Authors,
		PublishedYear: f.PublishedYear,
		Language:      f.Language,
		Pages:         f.Pages,
		CreatedAt:     now,
		UpdatedAt:     now,
		Version:       1,
	}, nil
}

// Revise replaces all editable fields with draft; an optional field that is
// nil in draft is removed. The draft is validated first, then version must
// equal the book's current version (else ErrVersionConflict). On error the
// book is unchanged. On success Version is incremented and UpdatedAt = now.
func (b *Book) Revise(draft BookDraft, version int64, now time.Time) error {
	f, err := normalize(draft)
	if err != nil {
		return err
	}
	if version != b.Version {
		return ErrVersionConflict
	}
	b.ISBN = f.ISBN
	b.Title = f.Title
	b.Authors = f.Authors
	b.PublishedYear = f.PublishedYear
	b.Language = f.Language
	b.Pages = f.Pages
	b.Version++
	b.UpdatedAt = now
	return nil
}

// normalize validates draft and returns its editable fields as a Book
// (without ID, timestamps or version). Slices and pointers are copied.
func normalize(d BookDraft) (Book, error) {
	isbn, err := ParseISBN(d.ISBN)
	if err != nil {
		return Book{}, err
	}
	title, err := normalizeTitle(d.Title)
	if err != nil {
		return Book{}, err
	}
	authors, err := normalizeAuthors(d.Authors)
	if err != nil {
		return Book{}, err
	}
	if y := d.PublishedYear; y != nil && (*y < 1450 || *y > 2100) {
		return Book{}, &ValidationError{Field: "publishedYear", Reason: "must be between 1450 and 2100"}
	}
	if l := d.Language; l != nil && !isLanguageCode(*l) {
		return Book{}, &ValidationError{Field: "language", Reason: "must be two lowercase letters (ISO 639-1)"}
	}
	if p := d.Pages; p != nil && (*p < 1 || *p > 10000) {
		return Book{}, &ValidationError{Field: "pages", Reason: "must be between 1 and 10000"}
	}
	return Book{
		ISBN:          isbn,
		Title:         title,
		Authors:       authors,
		PublishedYear: copyPtr(d.PublishedYear),
		Language:      copyPtr(d.Language),
		Pages:         copyPtr(d.Pages),
	}, nil
}

// isPlainText reports whether s is valid UTF-8 without control characters
// (U+0000 to U+001F, U+007F to U+009F). It is checked before trimming, so a
// stray newline or tab around a value is rejected too.
func isPlainText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// hasVisibleChar reports whether s has a rune that is neither white space nor
// a format character (Cf), such as a zero width space or a byte order mark.
// Format characters inside visible text, like the joiner in an emoji
// sequence, do not count against it.
func hasVisibleChar(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return !unicode.IsSpace(r) && !unicode.Is(unicode.Cf, r)
	}) >= 0
}

func normalizeTitle(raw string) (string, error) {
	if !isPlainText(raw) {
		return "", &ValidationError{Field: "title", Reason: "must be valid UTF-8 without control characters"}
	}
	title := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(title); n < 1 || n > 300 {
		return "", &ValidationError{Field: "title", Reason: "must have 1 to 300 characters"}
	}
	if !hasVisibleChar(title) {
		return "", &ValidationError{Field: "title", Reason: "must contain a visible character"}
	}
	return title, nil
}

func normalizeAuthors(raw []string) ([]string, error) {
	if len(raw) < 1 || len(raw) > 20 {
		return nil, &ValidationError{Field: "authors", Reason: "must have 1 to 20 authors"}
	}
	authors := make([]string, len(raw))
	for i, a := range raw {
		if !isPlainText(a) {
			return nil, &ValidationError{Field: "authors", Reason: "each author name must be valid UTF-8 without control characters"}
		}
		a = strings.TrimSpace(a)
		if n := utf8.RuneCountInString(a); n < 1 || n > 200 {
			return nil, &ValidationError{Field: "authors", Reason: "each author name must have 1 to 200 characters"}
		}
		if !hasVisibleChar(a) {
			return nil, &ValidationError{Field: "authors", Reason: "each author name must contain a visible character"}
		}
		authors[i] = a
	}
	return authors, nil
}

func isLanguageCode(s string) bool {
	return len(s) == 2 && s[0] >= 'a' && s[0] <= 'z' && s[1] >= 'a' && s[1] <= 'z'
}

func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
