package domain

import (
	"errors"
	"testing"
)

func TestNewListFilter(t *testing.T) {
	tests := []struct {
		name   string
		author string
		want   string
	}{
		{"empty means no filter", "", ""},
		{"plain name", "Alice", "Alice"},
		{"unicode letters and emoji", "José 😀", "José 😀"},
		{"spaces are kept, not trimmed", "  Alice  ", "  Alice  "},
		{"zero width space is text, not a control character", "​", "​"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := NewListFilter(tc.author)
			if err != nil {
				t.Fatalf("NewListFilter(%q) error = %v", tc.author, err)
			}
			if f.Author != tc.want {
				t.Fatalf("Author = %q, want %q", f.Author, tc.want)
			}
		})
	}
}

func TestNewListFilter_RejectsNotPlainText(t *testing.T) {
	tests := map[string]string{
		"invalid UTF-8":         "\xff",
		"overlong UTF-8":        "\xc0\xaf",
		"lone surrogate (CESU)": "\xed\xa0\x80",
		"NUL":                   "a\x00b",
		"newline":               "Alice\n",
		"CRLF":                  "Alice\r\n",
		"tab":                   "\tAlice",
		"DEL":                   "Alice\x7f",
		"C1 control":            "Alice\u0085",
	}
	for name, author := range tests {
		t.Run(name, func(t *testing.T) {
			f, err := NewListFilter(author)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %T, want *ValidationError", err)
			}
			if ve.Field != "author" || ve.Reason != "must be valid UTF-8 without control characters" {
				t.Fatalf("ValidationError = %+v", ve)
			}
			if f != (ListFilter{}) {
				t.Fatalf("filter = %+v, want the zero value on error", f)
			}
		})
	}
}
