package e2e_test

// End-to-end tests of the demo data seeding (SEED_DEMO_DATA) of the catalog.
// Every top-level identifier is prefixed with "seed".
//
// Restarting the server means closing the app and calling bootstrap.New again with the same
// config. The restart keeps the data because every test runs on its own MongoDB test database.

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"orch-test/backend/internal/bootstrap"
	"orch-test/backend/internal/oas"
)

// seedDemoBook is one row of the demo catalog; zero year and pages mean "not set".
type seedDemoBook struct {
	isbn    string
	title   string
	authors []string
	year    int
	lang    string
	pages   int
}

var seedDemoBooks = []seedDemoBook{
	{"9780321125217", "Domain-Driven Design", []string{"Eric Evans"}, 2003, "en", 560},
	{"9780321834577", "Implementing Domain-Driven Design", []string{"Vaughn Vernon"}, 2013, "en", 656},
	{"9780321127426", "Patterns of Enterprise Application Architecture", []string{"Martin Fowler"}, 2002, "en", 560},
	{"9780134757599", "Refactoring", []string{"Martin Fowler"}, 2018, "en", 448},
	{"9780132350884", "Clean Code", []string{"Robert C. Martin"}, 2008, "en", 464},
	{"9780135957059", "The Pragmatic Programmer", []string{"David Thomas", "Andrew Hunt"}, 2019, "en", 352},
	{"9781449373320", "Designing Data-Intensive Applications", []string{"Martin Kleppmann"}, 2017, "en", 616},
	{"9780134190440", "The Go Programming Language", []string{"Alan A. A. Donovan", "Brian W. Kernighan"}, 2015, "en", 380},
	{"9781942788331", "Accelerate", []string{"Nicole Forsgren", "Jez Humble", "Gene Kim"}, 2018, "en", 288},
	{"9780262510875", "Structure and Interpretation of Computer Programs", []string{"Harold Abelson", "Gerald Jay Sussman"}, 1996, "en", 657},
	{"9780465026562", "Gödel, Escher, Bach", []string{"Douglas R. Hofstadter"}, 1999, "en", 0},
	{"9782070612758", "Le Petit Prince", []string{"Antoine de Saint-Exupéry"}, 0, "fr", 0},
}

// seedLogBuffer is a bytes.Buffer that is safe for the logger and the test to use concurrently.
type seedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *seedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *seedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// seedServer is a running application together with its client and its captured log.
type seedServer struct {
	client *oas.Client
	log    *seedLogBuffer
	app    *bootstrap.App
	srv    *httptest.Server
	once   sync.Once
}

// stop shuts the server down; it is safe to call more than once.
func (s *seedServer) stop() {
	s.once.Do(func() {
		s.srv.Close()
		_ = s.app.Close(context.Background())
	})
}

// seedStart starts the application with cfg and a debug-level logger writing to a buffer.
// The server is stopped when the test ends, or earlier with stop.
func seedStart(t *testing.T, cfg bootstrap.Config) *seedServer {
	t.Helper()
	logs := &seedLogBuffer{}
	cfg.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	app, err := bootstrap.New(ctx, cfg)
	if err != nil {
		t.Fatalf("bootstrap.New: %v", err)
	}
	s := &seedServer{log: logs, app: app, srv: httptest.NewServer(app.Handler)}
	t.Cleanup(s.stop)
	s.client, err = oas.NewClient(s.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// seedConfig is the config of an app on a fresh MongoDB test database with the given SEED_DEMO_DATA.
func seedConfig(t *testing.T, seedDemoData bool) bootstrap.Config {
	t.Helper()
	cfg, _ := e2eMongoConfig(t)
	cfg.SeedDemoData = seedDemoData
	return cfg
}

func seedListAll(t *testing.T, c *oas.Client) *oas.BookList {
	t.Helper()
	list, err := c.ListBooks(context.Background(), oas.ListBooksParams{Limit: oas.NewOptInt(100)})
	if err != nil {
		t.Fatalf("ListBooks: %v", err)
	}
	return list
}

func seedByISBN(t *testing.T, list *oas.BookList) map[string]oas.Book {
	t.Helper()
	m := make(map[string]oas.Book, len(list.Items))
	for _, b := range list.Items {
		if _, dup := m[b.Isbn]; dup {
			t.Fatalf("ISBN %s listed twice", b.Isbn)
		}
		m[b.Isbn] = b
	}
	return m
}

// seedRequireDemoCatalog asserts that the list holds exactly the 12 demo books as in the table.
func seedRequireDemoCatalog(t *testing.T, list *oas.BookList) {
	t.Helper()
	if list.Total != int64(len(seedDemoBooks)) || len(list.Items) != len(seedDemoBooks) {
		t.Fatalf("total = %d, items = %d, want %d", list.Total, len(list.Items), len(seedDemoBooks))
	}
	byISBN := seedByISBN(t, list)
	for _, want := range seedDemoBooks {
		got, ok := byISBN[want.isbn]
		if !ok {
			t.Errorf("demo book %s (%s) is missing", want.isbn, want.title)
			continue
		}
		if got.Title != want.title {
			t.Errorf("%s: title = %q, want %q", want.isbn, got.Title, want.title)
		}
		if !slices.Equal(got.Authors, want.authors) {
			t.Errorf("%s: authors = %q, want %q", want.isbn, got.Authors, want.authors)
		}
		seedRequireOptInt(t, want.isbn, "publishedYear", got.PublishedYear, want.year)
		seedRequireOptInt(t, want.isbn, "pages", got.Pages, want.pages)
		if got.Language.Value != want.lang || !got.Language.Set {
			t.Errorf("%s: language = %+v, want %q", want.isbn, got.Language, want.lang)
		}
		if got.ID <= 0 {
			t.Errorf("%s: id = %d, want a positive id", want.isbn, got.ID)
		}
		if got.Version != 1 {
			t.Errorf("%s: version = %d, want 1", want.isbn, got.Version)
		}
	}
}

// seedRequireOptInt asserts the optional field is set to want, or not set at all if want is 0.
func seedRequireOptInt(t *testing.T, isbn, field string, got oas.OptInt, want int) {
	t.Helper()
	if want == 0 {
		if got.Set {
			t.Errorf("%s: %s = %d, want it not set", isbn, field, got.Value)
		}
		return
	}
	if !got.Set || got.Value != want {
		t.Errorf("%s: %s = %+v, want %d", isbn, field, got, want)
	}
}

// Given an empty catalog and SEED_DEMO_DATA=true, when the server starts,
// then GET /api/books?limit=100 returns the 12 demo books.
func TestSeedEmptyCatalogGetsDemoBooks(t *testing.T) {
	s := seedStart(t, seedConfig(t, true))
	seedRequireDemoCatalog(t, seedListAll(t, s.client))

	if got := s.log.String(); !strings.Contains(got, "seeded 12 demo books, skipped 0") {
		t.Errorf("log lacks the seeding summary; log:\n%s", got)
	}
}

// Given `SEED_DEMO_DATA` is unset, when the server starts, then the catalog is empty.
func TestSeedUnsetFlagLeavesCatalogEmpty(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"unset", ""},
		{"false", "false"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SEED_DEMO_DATA", tt.value)
			cfg, _ := e2eMongoConfig(t)
			cfg.SeedDemoData = bootstrap.ConfigFromEnv().SeedDemoData
			s := seedStart(t, cfg)
			list := seedListAll(t, s.client)
			if list.Total != 0 || len(list.Items) != 0 {
				t.Fatalf("total = %d, items = %d, want an empty catalog", list.Total, len(list.Items))
			}
			if got := s.log.String(); strings.Contains(got, "seeded") {
				t.Errorf("log mentions seeding although the flag is off:\n%s", got)
			}
		})
	}
}

// Given the demo books are already seeded, when the server restarts with the flag,
// then there are still 12 books, and the log says `seeded 0 demo books, skipped 12`.
func TestSeedRestartDoesNotDuplicateDemoBooks(t *testing.T) {
	cfg := seedConfig(t, true)

	first := seedStart(t, cfg)
	before := seedByISBN(t, seedListAll(t, first.client))
	first.stop()

	second := seedStart(t, cfg)
	list := seedListAll(t, second.client)
	seedRequireDemoCatalog(t, list)
	if got := second.log.String(); !strings.Contains(got, "seeded 0 demo books, skipped 12") {
		t.Errorf("log lacks `seeded 0 demo books, skipped 12`; log:\n%s", got)
	}
	if got := second.log.String(); strings.Contains(got, "seeded demo book") {
		t.Errorf("restart seeded a book again; log:\n%s", got)
	}
	for isbn, b := range seedByISBN(t, list) {
		if b.ID != before[isbn].ID || b.Version != before[isbn].Version {
			t.Errorf("%s: id/version = %d/%d after restart, want %d/%d",
				isbn, b.ID, b.Version, before[isbn].ID, before[isbn].Version)
		}
	}
}

// Given a demo book was deleted, when the server restarts with the flag, then it is added again
// (it's missing by ISBN). Given a demo book was edited but kept its ISBN, then it is left as it is.
func TestSeedRestartAddsDeletedDemoBookAndKeepsEditedOne(t *testing.T) {
	const (
		deletedISBN = "9780132350884" // Clean Code
		editedISBN  = "9780134757599" // Refactoring
		editedTitle = "Refactoring, second edition (annotated)"
	)
	ctx := context.Background()
	cfg := seedConfig(t, true)

	first := seedStart(t, cfg)
	list := seedListAll(t, first.client)
	byISBN := seedByISBN(t, list)
	var highestID int64
	for _, b := range list.Items {
		highestID = max(highestID, b.ID)
	}
	deleted, edited := byISBN[deletedISBN], byISBN[editedISBN]
	if deleted.ID == 0 || edited.ID == 0 {
		t.Fatalf("demo books %s / %s were not seeded", deletedISBN, editedISBN)
	}

	if err := first.client.DeleteBook(ctx, oas.DeleteBookParams{ID: deleted.ID}); err != nil {
		t.Fatalf("DeleteBook: %v", err)
	}
	update := &oas.BookUpdate{
		Isbn:          edited.Isbn,
		Title:         editedTitle,
		Authors:       edited.Authors,
		PublishedYear: edited.PublishedYear,
		Language:      edited.Language,
		Pages:         edited.Pages,
		Version:       edited.Version,
	}
	if _, err := first.client.UpdateBook(ctx, update, oas.UpdateBookParams{ID: edited.ID}); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}
	first.stop()

	second := seedStart(t, cfg)
	after := seedListAll(t, second.client)
	if after.Total != 12 || len(after.Items) != 12 {
		t.Fatalf("total = %d, items = %d after restart, want 12", after.Total, len(after.Items))
	}
	afterByISBN := seedByISBN(t, after)

	readded, ok := afterByISBN[deletedISBN]
	if !ok {
		t.Fatalf("deleted demo book %s was not added again", deletedISBN)
	}
	if readded.ID <= highestID {
		t.Errorf("re-added book has id %d, want an id above the previous highest %d", readded.ID, highestID)
	}
	if readded.Title != deleted.Title || readded.Version != 1 {
		t.Errorf("re-added book = %q v%d, want %q v1", readded.Title, readded.Version, deleted.Title)
	}

	kept := afterByISBN[editedISBN]
	if kept.ID != edited.ID || kept.Title != editedTitle || kept.Version != 2 {
		t.Errorf("edited book = id %d %q v%d, want id %d %q v2",
			kept.ID, kept.Title, kept.Version, edited.ID, editedTitle)
	}

	for _, d := range seedDemoBooks {
		if _, ok := afterByISBN[d.isbn]; !ok {
			t.Errorf("demo book %s is missing after restart", d.isbn)
		}
	}
	if got := second.log.String(); !strings.Contains(got, "seeded 1 demo books, skipped 11") {
		t.Errorf("log lacks `seeded 1 demo books, skipped 11`; log:\n%s", got)
	}
}
