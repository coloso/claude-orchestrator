// Package apperr defines errors that carry an HTTP status. Feature services return
// them; handler.NewError turns them into the spec's Error response.
package apperr

import (
	"fmt"
	"net/http"
)

// Error is an error with an HTTP status code.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// BadRequest returns a 400 error.
func BadRequest(format string, args ...any) error {
	return &Error{Status: http.StatusBadRequest, Message: fmt.Sprintf(format, args...)}
}

// NotFound returns a 404 error.
func NotFound(format string, args ...any) error {
	return &Error{Status: http.StatusNotFound, Message: fmt.Sprintf(format, args...)}
}
