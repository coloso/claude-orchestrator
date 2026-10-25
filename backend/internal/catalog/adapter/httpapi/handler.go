// Package httpapi is the catalog's driving HTTP adapter: it implements the
// catalog operations of oas.Handler on top of app.UseCases.
package httpapi

import (
	"context"
	"errors"
	"net/http"

	"orch-test/backend/internal/catalog/app"
	"orch-test/backend/internal/catalog/domain"
	"orch-test/backend/internal/oas"
)

// CatalogHandler implements the catalog operations of oas.Handler.
type CatalogHandler struct{ uc app.UseCases }

// NewCatalogHandler returns a handler that serves the catalog use cases.
func NewCatalogHandler(uc app.UseCases) *CatalogHandler { return &CatalogHandler{uc: uc} }

// CreateBook implements createBook.
func (h *CatalogHandler) CreateBook(ctx context.Context, req *oas.BookInput) (*oas.Book, error) {
	b, err := h.uc.AddBook(ctx, domain.BookDraft{
		ISBN:          req.Isbn,
		Title:         req.Title,
		Authors:       req.Authors,
		PublishedYear: optPtr(req.PublishedYear.Get()),
		Language:      optPtr(req.Language.Get()),
		Pages:         optPtr(req.Pages.Get()),
	})
	if err != nil {
		return nil, mapError(err)
	}
	return toBook(b), nil
}

// ListBooks implements listBooks.
func (h *CatalogHandler) ListBooks(ctx context.Context, params oas.ListBooksParams) (*oas.BookList, error) {
	page, err := h.uc.ListBooks(ctx, app.ListQuery{
		Author: params.Author.Or(""),
		Limit:  params.Limit.Or(domain.DefaultLimit),
		Offset: params.Offset.Or(0),
	})
	if err != nil {
		return nil, mapError(err)
	}
	items := make([]oas.Book, 0, len(page.Items))
	for _, b := range page.Items {
		items = append(items, *toBook(b))
	}
	return &oas.BookList{Items: items, Total: page.Total}, nil
}

// GetBook implements getBook.
func (h *CatalogHandler) GetBook(ctx context.Context, params oas.GetBookParams) (*oas.Book, error) {
	b, err := h.uc.GetBook(ctx, domain.BookID(params.ID))
	if err != nil {
		return nil, mapError(err)
	}
	return toBook(b), nil
}

// UpdateBook implements updateBook.
func (h *CatalogHandler) UpdateBook(ctx context.Context, req *oas.BookUpdate, params oas.UpdateBookParams) (*oas.Book, error) {
	b, err := h.uc.ReviseBook(ctx, domain.BookID(params.ID), domain.BookDraft{
		ISBN:          req.Isbn,
		Title:         req.Title,
		Authors:       req.Authors,
		PublishedYear: optPtr(req.PublishedYear.Get()),
		Language:      optPtr(req.Language.Get()),
		Pages:         optPtr(req.Pages.Get()),
	}, req.Version)
	if err != nil {
		return nil, mapError(err)
	}
	return toBook(b), nil
}

// DeleteBook implements deleteBook.
func (h *CatalogHandler) DeleteBook(ctx context.Context, params oas.DeleteBookParams) error {
	if err := h.uc.RemoveBook(ctx, domain.BookID(params.ID)); err != nil {
		return mapError(err)
	}
	return nil
}

// optPtr turns an Opt*'s Get result into a pointer: nil when unset.
func optPtr[T any](v T, ok bool) *T {
	if !ok {
		return nil
	}
	return &v
}

func toBook(b domain.Book) *oas.Book {
	out := &oas.Book{
		ID:        int64(b.ID),
		Isbn:      b.ISBN.String(),
		Title:     b.Title,
		Authors:   b.Authors,
		CreatedAt: b.CreatedAt,
		UpdatedAt: b.UpdatedAt,
		Version:   b.Version,
	}
	if b.PublishedYear != nil {
		out.PublishedYear = oas.NewOptInt(*b.PublishedYear)
	}
	if b.Language != nil {
		out.Language = oas.NewOptString(*b.Language)
	}
	if b.Pages != nil {
		out.Pages = oas.NewOptInt(*b.Pages)
	}
	return out
}

// mapError translates domain errors into API errors. For 404 and 409 it uses
// the sentinel's own message, so wrapped repository details never reach the
// client. Other errors are returned unchanged and become a 500.
func mapError(err error) error {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		return apiError(http.StatusBadRequest, ve.Error())
	case errors.Is(err, domain.ErrNotFound):
		return apiError(http.StatusNotFound, domain.ErrNotFound.Error())
	case errors.Is(err, domain.ErrDuplicateISBN):
		return apiError(http.StatusConflict, domain.ErrDuplicateISBN.Error())
	case errors.Is(err, domain.ErrVersionConflict):
		return apiError(http.StatusConflict, domain.ErrVersionConflict.Error())
	default:
		return err
	}
}

func apiError(status int, msg string) *oas.ErrorStatusCode {
	return &oas.ErrorStatusCode{StatusCode: status, Response: oas.Error{Error: msg}}
}
