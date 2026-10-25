package domain

import (
	"errors"
	"testing"
)

func TestParseISBN(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    ISBN
		wantErr string
	}{
		{"hyphenated", "978-0-13-235088-4", "9780132350884", ""},
		{"plain", "9780132350884", "9780132350884", ""},
		{"spaces", " 978 0 13 235088 4 ", "9780132350884", ""},
		{"979 prefix", "9791034326228", "9791034326228", ""},
		{"check digit zero", "9780306406157", "9780306406157", ""},
		{"wrong check digit", "9780132350885", "", "isbn: invalid check digit"},
		{"12 digits", "978013235088", "", "isbn: must have 13 digits"},
		{"14 digits", "97801323508844", "", "isbn: must have 13 digits"},
		{"letters", "978013235088X", "", "isbn: must contain only digits, hyphens and spaces"},
		{"977 prefix", "9770000000002", "", "isbn: must start with 978 or 979"},
		{"empty", "", "", "isbn: must have 13 digits"},
		{"arabic-indic digit", "978013235088٤", "", "isbn: must contain only digits, hyphens and spaces"},
		{"fullwidth digits", "９７８０１３２３５０８８４", "", "isbn: must contain only digits, hyphens and spaces"},
		{"arabic-indic digits", "٩٧٨٠١٣٢٣٥٠٨٨٤", "", "isbn: must contain only digits, hyphens and spaces"},
		{"extended arabic-indic digit", "978013235088۴", "", "isbn: must contain only digits, hyphens and spaces"},
		{"NUL", "978\x000132350884", "", "isbn: must contain only digits, hyphens and spaces"},
		{"trailing newline", "9780132350884\n", "", "isbn: must contain only digits, hyphens and spaces"},
		{"invalid UTF-8", "978013235088\xff", "", "isbn: must contain only digits, hyphens and spaces"},
		{"non-breaking space", "978 0132350884", "", "isbn: must contain only digits, hyphens and spaces"},
		{"tab is not a space", "978\t0132350884", "", "isbn: must contain only digits, hyphens and spaces"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseISBN(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ParseISBN(%q) error = %v", tc.in, err)
				}
				if got != tc.want || got.String() != string(tc.want) {
					t.Fatalf("ParseISBN(%q) = %q, want %q", tc.in, got, tc.want)
				}
				return
			}
			assertValidation(t, err, "isbn")
			if err.Error() != tc.wantErr {
				t.Fatalf("error = %q, want %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func assertValidation(t *testing.T, err error, field string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want validation error on %q, got nil", field)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("error %v does not match ErrInvalid", err)
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error %v is not a *ValidationError", err)
	}
	if ve.Field != field {
		t.Fatalf("Field = %q, want %q (%v)", ve.Field, field, err)
	}
}
