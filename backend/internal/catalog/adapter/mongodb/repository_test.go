package mongodb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"orch-test/backend/internal/catalog/adapter/repotest"
	"orch-test/backend/internal/catalog/domain"
	platformmongo "orch-test/backend/internal/platform/mongodb"
)

// newMigratedDB returns a fresh test database with the catalog schema migrated.
func newMigratedDB(t *testing.T) *mongo.Database {
	t.Helper()
	db := newTestDB(t)
	err := platformmongo.Migrate(context.Background(), db, [][]platformmongo.Migration{Migrations()}, platformmongo.MigrateOptions{})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestRepositoryContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) domain.Repository {
		return NewRepository(newMigratedDB(t))
	})
}

var testTime = time.Date(2026, 1, 2, 3, 4, 5, 678_000_000, time.UTC)

func isbnFor(t *testing.T, n int) string {
	t.Helper()
	body := fmt.Sprintf("978%09d", n)
	sum := 0
	for i, c := range body {
		w := 1
		if i%2 == 1 {
			w = 3
		}
		sum += int(c-'0') * w
	}
	isbn, err := domain.ParseISBN(fmt.Sprintf("%s%d", body, (10-sum%10)%10))
	if err != nil {
		t.Fatal(err)
	}
	return isbn.String()
}

func newBook(t *testing.T, n int, mod func(*domain.BookDraft)) domain.Book {
	t.Helper()
	d := domain.BookDraft{ISBN: isbnFor(t, n), Title: fmt.Sprintf("Book %d", n), Authors: []string{"Author"}}
	if mod != nil {
		mod(&d)
	}
	b, err := domain.NewBook(d, testTime)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func completeDraft(d *domain.BookDraft) {
	year, lang, pages := 2015, "en", 380
	d.PublishedYear, d.Language, d.Pages = &year, &lang, &pages
}

func TestAddStoresDocumentsThatPassTheValidator(t *testing.T) {
	r := NewRepository(newMigratedDB(t))
	ctx := context.Background()
	if _, err := r.Add(ctx, newBook(t, 1, completeDraft)); err != nil {
		t.Fatalf("complete book: %v", err)
	}
	if _, err := r.Add(ctx, newBook(t, 2, nil)); err != nil {
		t.Fatalf("minimal book: %v", err)
	}
}

func TestStoredDocumentHasExactlyTheSchemaFields(t *testing.T) {
	db := newMigratedDB(t)
	r := NewRepository(db)
	ctx := context.Background()
	stored, err := r.Add(ctx, newBook(t, 1, completeDraft))
	if err != nil {
		t.Fatal(err)
	}
	minimal, err := r.Add(ctx, newBook(t, 2, nil))
	if err != nil {
		t.Fatal(err)
	}

	raw := func(id domain.BookID) bson.Raw {
		t.Helper()
		doc, err := db.Collection(BooksCollection).FindOne(ctx, bson.D{{Key: "_id", Value: int64(id)}}).Raw()
		if err != nil {
			t.Fatal(err)
		}
		return doc
	}
	keys := func(doc bson.Raw) []string {
		elems, err := doc.Elements()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range elems {
			out = append(out, e.Key())
		}
		return out
	}

	full := raw(stored.ID)
	wantTypes := map[string]bson.Type{
		"_id": bson.TypeInt64, "version": bson.TypeInt64,
		"publishedYear": bson.TypeInt32, "pages": bson.TypeInt32,
		"isbn": bson.TypeString, "title": bson.TypeString, "language": bson.TypeString,
		"authors": bson.TypeArray, "createdAt": bson.TypeDateTime, "updatedAt": bson.TypeDateTime,
	}
	if got := keys(full); len(got) != len(wantTypes) {
		t.Errorf("fields = %v, want exactly %d", got, len(wantTypes))
	}
	for k, want := range wantTypes {
		v, err := full.LookupErr(k)
		if err != nil {
			t.Errorf("field %s: %v", k, err)
			continue
		}
		if v.Type != want {
			t.Errorf("field %s has type %s, want %s", k, v.Type, want)
		}
	}

	min := raw(minimal.ID)
	for _, k := range []string{"publishedYear", "language", "pages"} {
		if _, err := min.LookupErr(k); err == nil {
			t.Errorf("minimal book has field %s, want it absent", k)
		}
	}
	if got := keys(min); len(got) != 7 {
		t.Errorf("minimal book fields = %v, want 7", got)
	}
}

func TestCounterTracksTheLastIDAndSurvivesRemove(t *testing.T) {
	db := newMigratedDB(t)
	r := NewRepository(db)
	ctx := context.Background()
	seq := func() int64 {
		t.Helper()
		var c struct {
			Seq int64 `bson:"seq"`
		}
		if err := db.Collection(CountersCollection).FindOne(ctx, bson.D{{Key: "_id", Value: "books"}}).Decode(&c); err != nil {
			t.Fatal(err)
		}
		return c.Seq
	}

	for i := 1; i <= 3; i++ {
		b, err := r.Add(ctx, newBook(t, i, nil))
		if err != nil {
			t.Fatal(err)
		}
		if int64(b.ID) != int64(i) || seq() != int64(i) {
			t.Fatalf("after add %d: id = %d, seq = %d", i, b.ID, seq())
		}
	}
	if err := r.Remove(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if seq() != 3 {
		t.Fatalf("seq = %d after Remove, want 3", seq())
	}
	b, err := r.Add(ctx, newBook(t, 4, nil))
	if err != nil || b.ID != 4 {
		t.Fatalf("Add after Remove = %+v, %v; want ID 4", b, err)
	}
}

func TestDuplicateISBNIsReportedAsSuch(t *testing.T) {
	r := NewRepository(newMigratedDB(t))
	ctx := context.Background()
	if _, err := r.Add(ctx, newBook(t, 1, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Add(ctx, newBook(t, 1, nil)); !errors.Is(err, domain.ErrDuplicateISBN) {
		t.Fatalf("err = %v, want ErrDuplicateISBN", err)
	}
}

func writeException(t *testing.T, code int, message string, keyPattern bson.D) error {
	t.Helper()
	raw := bson.D{{Key: "index", Value: 0}, {Key: "code", Value: code}, {Key: "errmsg", Value: message}}
	if keyPattern != nil {
		raw = append(raw, bson.E{Key: "keyPattern", Value: keyPattern})
	}
	doc, err := bson.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Errorf("insert book: %w", mongo.WriteException{
		WriteErrors: mongo.WriteErrors{{Code: code, Message: message, Raw: doc}},
	})
}

func TestIsDuplicateISBNReadsTheStructuredKeyPattern(t *testing.T) {
	isbnKey := bson.D{{Key: "isbn", Value: 1}}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"isbn key pattern", writeException(t, 11000, "E11000 duplicate key", isbnKey), true},
		{"message text does not matter", writeException(t, 11000, "something else entirely", isbnKey), true},
		{"duplicate _id", writeException(t, 11000, "E11000 duplicate key error collection: x index: _id_ dup key: { _id: 1 }", bson.D{{Key: "_id", Value: 1}}), false},
		{"_id with misleading message", writeException(t, 11000, "index: isbn_unique", bson.D{{Key: "_id", Value: 1}}), false},
		{"compound key including isbn", writeException(t, 11000, "dup", bson.D{{Key: "isbn", Value: 1}, {Key: "title", Value: 1}}), false},
		{"no key pattern", writeException(t, 11000, "index: isbn_unique", nil), false},
		{"other write error code", writeException(t, 121, "validation", isbnKey), false},
		{"not a write error", errors.New("index: isbn_unique"), false},
		{"timeout", context.DeadlineExceeded, false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDuplicateISBN(tt.err); got != tt.want {
				t.Errorf("isDuplicateISBN = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWithDeadlineBoundsTheCallButKeepsAnEarlierDeadline(t *testing.T) {
	ctx, cancel := withDeadline(context.Background())
	defer cancel()
	d, ok := ctx.Deadline()
	if !ok || time.Until(d) > operationTimeout || time.Until(d) < operationTimeout-time.Second {
		t.Errorf("deadline = %v (set: %v), want about %v from now", d, ok, operationTimeout)
	}

	parent, cancelParent := context.WithTimeout(context.Background(), time.Second)
	defer cancelParent()
	child, cancelChild := withDeadline(parent)
	defer cancelChild()
	pd, _ := parent.Deadline()
	if cd, _ := child.Deadline(); cd.After(pd) {
		t.Errorf("child deadline %v is later than the caller's %v", cd, pd)
	}
}

func TestDuplicateISBNIsDetectedFromARealServerError(t *testing.T) {
	// Guards the assumption of isDuplicateISBN: the server really sends keyPattern.
	db := newMigratedDB(t)
	ctx := context.Background()
	books := db.Collection(BooksCollection)
	r := NewRepository(db)
	first, err := r.Add(ctx, newBook(t, 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	_, err = books.InsertOne(ctx, toDoc(domain.Book{ID: 99, ISBN: first.ISBN, Title: "Dup", Authors: []string{"A"}, CreatedAt: testTime, UpdatedAt: testTime, Version: 1}))
	if !isDuplicateISBN(err) {
		t.Errorf("duplicate isbn: isDuplicateISBN(%v) = false", err)
	}
	_, err = books.InsertOne(ctx, toDoc(domain.Book{ID: first.ID, ISBN: domain.ISBN(isbnFor(t, 2)), Title: "Dup", Authors: []string{"A"}, CreatedAt: testTime, UpdatedAt: testTime, Version: 1}))
	if err == nil || isDuplicateISBN(err) {
		t.Errorf("duplicate _id: isDuplicateISBN(%v) = true, want a duplicate key error that is not an ISBN one", err)
	}
}

func TestExpiredDeadlineIsNotADomainError(t *testing.T) {
	r := NewRepository(newMigratedDB(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Get(ctx, 1)
	if err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Get with a cancelled context = %v, want a non-domain error", err)
	}
	if _, _, err := r.List(ctx, domain.ListFilter{}, domain.Page{Limit: 10}); err == nil {
		t.Fatal("List with a cancelled context succeeded")
	}
}

func TestCounterBehindExistingDataIsAnInternalError(t *testing.T) {
	db := newMigratedDB(t)
	r := NewRepository(db)
	ctx := context.Background()
	if _, err := r.Add(ctx, newBook(t, 1, nil)); err != nil {
		t.Fatal(err)
	}
	// Rewind the counter, so the next ID collides with book 1.
	if _, err := db.Collection(CountersCollection).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: "books"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "seq", Value: int64(0)}}}}); err != nil {
		t.Fatal(err)
	}
	_, err := r.Add(ctx, newBook(t, 2, nil))
	if err == nil || errors.Is(err, domain.ErrDuplicateISBN) {
		t.Fatalf("err = %v, want an internal error that is not ErrDuplicateISBN", err)
	}
}

func TestConcurrentFirstAddsAllSucceed(t *testing.T) {
	r := NewRepository(newMigratedDB(t))
	ctx := context.Background()
	const n = 8
	books := make([]domain.Book, n)
	for i := range books {
		books[i] = newBook(t, i+1, nil)
	}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for _, b := range books {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Add(ctx, b)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Add: %v", err)
		}
	}
}

func TestConcurrentUpdatesWithTheSameVersionExactlyOneWins(t *testing.T) {
	r := NewRepository(newMigratedDB(t))
	ctx := context.Background()
	stored, err := r.Add(ctx, newBook(t, 1, nil))
	if err != nil {
		t.Fatal(err)
	}
	const n = 6
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := stored
			b.Title = fmt.Sprintf("Title %d", i)
			b.Version = stored.Version + 1
			errs <- r.Update(ctx, b, stored.Version)
		}()
	}
	wg.Wait()
	close(errs)
	var ok, conflict int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrVersionConflict):
			conflict++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || conflict != n-1 {
		t.Fatalf("%d succeeded, %d conflicted; want 1 and %d", ok, conflict, n-1)
	}
}

func TestUpdateDroppingOptionalFieldsRemovesThem(t *testing.T) {
	db := newMigratedDB(t)
	r := NewRepository(db)
	ctx := context.Background()
	stored, err := r.Add(ctx, newBook(t, 1, completeDraft))
	if err != nil {
		t.Fatal(err)
	}
	revised := stored
	if err := revised.Revise(domain.BookDraft{ISBN: stored.ISBN.String(), Title: "Plain", Authors: []string{"Author"}}, stored.Version, testTime.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := r.Update(ctx, revised, stored.Version); err != nil {
		t.Fatal(err)
	}
	doc, err := db.Collection(BooksCollection).FindOne(ctx, bson.D{{Key: "_id", Value: int64(stored.ID)}}).Raw()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"publishedYear", "language", "pages"} {
		if _, err := doc.LookupErr(k); err == nil {
			t.Errorf("field %s still present after Update", k)
		}
	}
	got, err := r.Get(ctx, stored.ID)
	if err != nil || got.PublishedYear != nil || got.Language != nil || got.Pages != nil || got.Title != "Plain" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
}
