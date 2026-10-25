package domain

import (
	"reflect"
	"testing"
)

func TestDemoCatalog_AllBooksAreValid(t *testing.T) {
	drafts := DemoCatalog()
	if len(drafts) != 12 {
		t.Fatalf("len = %d, want 12", len(drafts))
	}
	for _, d := range drafts {
		if _, err := NewBook(d, t0); err != nil {
			t.Errorf("%s %q: %v", d.ISBN, d.Title, err)
		}
	}
}

func TestDemoCatalog_ISBNsAreDistinctAndNormalized(t *testing.T) {
	want := []string{
		"9780321125217", "9780321834577", "9780321127426", "9780134757599",
		"9780132350884", "9780135957059", "9781449373320", "9780134190440",
		"9781942788331", "9780262510875", "9780465026562", "9782070612758",
	}
	var got []string
	seen := map[string]bool{}
	for _, d := range DemoCatalog() {
		isbn, err := ParseISBN(d.ISBN)
		if err != nil {
			t.Fatal(err)
		}
		if isbn.String() != d.ISBN {
			t.Errorf("ISBN %q is not normalized", d.ISBN)
		}
		if seen[d.ISBN] {
			t.Errorf("duplicate ISBN %s", d.ISBN)
		}
		seen[d.ISBN] = true
		got = append(got, d.ISBN)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ISBNs = %v, want %v", got, want)
	}
}

func TestDemoCatalog_Fields(t *testing.T) {
	byISBN := map[string]BookDraft{}
	for _, d := range DemoCatalog() {
		byISBN[d.ISBN] = d
	}
	want := map[string]BookDraft{
		"9780321125217": {ISBN: "9780321125217", Title: "Domain-Driven Design", Authors: []string{"Eric Evans"}, PublishedYear: intp(2003), Language: strp("en"), Pages: intp(560)},
		"9780134190440": {ISBN: "9780134190440", Title: "The Go Programming Language", Authors: []string{"Alan A. A. Donovan", "Brian W. Kernighan"}, PublishedYear: intp(2015), Language: strp("en"), Pages: intp(380)},
		"9781942788331": {ISBN: "9781942788331", Title: "Accelerate", Authors: []string{"Nicole Forsgren", "Jez Humble", "Gene Kim"}, PublishedYear: intp(2018), Language: strp("en"), Pages: intp(288)},
		"9780465026562": {ISBN: "9780465026562", Title: "Gödel, Escher, Bach", Authors: []string{"Douglas R. Hofstadter"}, PublishedYear: intp(1999), Language: strp("en")},
		"9782070612758": {ISBN: "9782070612758", Title: "Le Petit Prince", Authors: []string{"Antoine de Saint-Exupéry"}, Language: strp("fr")},
	}
	for isbn, w := range want {
		if got := byISBN[isbn]; !reflect.DeepEqual(got, w) {
			t.Errorf("%s = %+v, want %+v", isbn, got, w)
		}
	}
}

func TestDemoCatalog_ReturnsFreshValues(t *testing.T) {
	a, b := DemoCatalog(), DemoCatalog()
	a[0].Authors[0] = "changed"
	*a[0].PublishedYear = 1
	*a[0].Language = "xx"
	*a[0].Pages = 1
	if b[0].Authors[0] != "Eric Evans" || *b[0].PublishedYear != 2003 || *b[0].Language != "en" || *b[0].Pages != 560 {
		t.Fatalf("calls share state: %+v", b[0])
	}
}
