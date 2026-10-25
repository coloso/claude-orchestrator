// Package app holds the use cases of the catalog bounded context.
package app

import (
	"context"

	"orch-test/backend/internal/catalog/domain"
)

// ListQuery selects a page of books, optionally of one author.
type ListQuery struct {
	Author string // "" = no filter
	Limit  int    // 1..100; the HTTP adapter fills in the default 20
	Offset int    // >= 0
}

// BookPage is one page of a list and the number of all matching books.
type BookPage struct {
	Items []domain.Book // never nil
	Total int64
}

// UseCases is the catalog's inbound port.
type UseCases interface {
	AddBook(ctx context.Context, draft domain.BookDraft) (domain.Book, error)
	GetBook(ctx context.Context, id domain.BookID) (domain.Book, error)
	ListBooks(ctx context.Context, q ListQuery) (BookPage, error)
	ReviseBook(ctx context.Context, id domain.BookID, draft domain.BookDraft, version int64) (domain.Book, error)
	RemoveBook(ctx context.Context, id domain.BookID) error
}
