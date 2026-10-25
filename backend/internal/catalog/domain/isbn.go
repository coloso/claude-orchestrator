package domain

import "strings"

// ISBN is an ISBN-13, the natural key of a book. Only ParseISBN produces
// one (repositories convert stored values back).
type ISBN string

// String returns the 13 digits without separators.
func (i ISBN) String() string { return string(i) }

// ParseISBN validates raw and normalizes it: hyphens and spaces are removed.
// Errors are *ValidationError with Field "isbn".
func ParseISBN(raw string) (ISBN, error) {
	var digits strings.Builder
	for _, r := range raw {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '-' || r == ' ':
		default:
			return "", &ValidationError{Field: "isbn", Reason: "must contain only digits, hyphens and spaces"}
		}
	}
	s := digits.String()
	if len(s) != 13 {
		return "", &ValidationError{Field: "isbn", Reason: "must have 13 digits"}
	}
	if !strings.HasPrefix(s, "978") && !strings.HasPrefix(s, "979") {
		return "", &ValidationError{Field: "isbn", Reason: "must start with 978 or 979"}
	}
	sum := 0
	for i := 0; i < 12; i++ {
		w := 1
		if i%2 == 1 {
			w = 3
		}
		sum += int(s[i]-'0') * w
	}
	if check := (10 - sum%10) % 10; int(s[12]-'0') != check {
		return "", &ValidationError{Field: "isbn", Reason: "invalid check digit"}
	}
	return ISBN(s), nil
}
