package mongodb

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	platformmongo "orch-test/backend/internal/platform/mongodb"
)

// errCodeDocumentValidationFailure is MongoDB's DocumentValidationFailure.
const errCodeDocumentValidationFailure = 121

func TestMigrationsFormAValidSet(t *testing.T) {
	plan, err := platformmongo.Plan(Migrations())
	if err != nil {
		t.Fatalf("Plan(Migrations()): %v", err)
	}
	var ids []string
	for _, m := range plan {
		ids = append(ids, m.ID)
		if m.Up == nil {
			t.Errorf("migration %s has no Up step", m.ID)
		}
	}
	if want := []string{"catalog/0001"}; !slices.Equal(ids, want) {
		t.Errorf("migration IDs = %v, want %v", ids, want)
	}
}

// migratedBooks runs every Up step in order on a fresh database and returns the books collection.
func migratedBooks(t *testing.T) (*mongo.Database, *mongo.Collection) {
	t.Helper()
	db := newTestDB(t)
	runMigrations(t, db)
	return db, db.Collection(BooksCollection)
}

func runMigrations(t *testing.T, db *mongo.Database) {
	t.Helper()
	ctx := context.Background()
	for _, m := range Migrations() {
		if err := m.Up(ctx, db); err != nil {
			t.Fatalf("migration %s: %v", m.ID, err)
		}
	}
}

var seq int64

// book returns a valid book document with only the required fields, unique per call.
// Tests modify the returned document to break exactly one rule.
func book(t *testing.T) bson.D {
	t.Helper()
	seq++
	now := time.Now().UTC().Truncate(time.Millisecond)
	return bson.D{
		{Key: "_id", Value: seq},
		{Key: "isbn", Value: "978" + fmt.Sprintf("%010d", seq)},
		{Key: "title", Value: "The Go Programming Language"},
		{Key: "authors", Value: bson.A{"Alan Donovan", "Brian Kernighan"}},
		{Key: "createdAt", Value: now},
		{Key: "updatedAt", Value: now},
		{Key: "version", Value: int64(1)},
	}
}

// with returns doc with key set to value (replacing or appending it).
func with(doc bson.D, key string, value any) bson.D {
	out := slices.Clone(doc)
	for i := range out {
		if out[i].Key == key {
			out[i].Value = value
			return out
		}
	}
	return append(out, bson.E{Key: key, Value: value})
}

func without(doc bson.D, key string) bson.D {
	return slices.DeleteFunc(slices.Clone(doc), func(e bson.E) bool { return e.Key == key })
}

func authors(n int) bson.A {
	a := make(bson.A, n)
	for i := range a {
		a[i] = "Author"
	}
	return a
}

func requireValidationFailure(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("insert succeeded, want document validation failure (code 121)")
	}
	var we mongo.WriteException
	if !errors.As(err, &we) {
		t.Fatalf("error is %T (%v), want mongo.WriteException", err, err)
	}
	if len(we.WriteErrors) != 1 || we.WriteErrors[0].Code != errCodeDocumentValidationFailure {
		t.Fatalf("write errors = %+v, want exactly one with code %d", we.WriteErrors, errCodeDocumentValidationFailure)
	}
}

func TestBooksValidator(t *testing.T) {
	_, books := migratedBooks(t)
	ctx := context.Background()
	full := with(with(with(book(t), "publishedYear", int32(2009)), "language", "en"), "pages", int32(380))

	accepted := []struct {
		name string
		doc  bson.D
	}{
		{"only required fields", book(t)},
		{"complete book", full},
		{"title of 300 characters", with(book(t), "title", strings.Repeat("t", 300))},
		{"20 authors", with(book(t), "authors", authors(20))},
		{"author name of 200 characters", with(book(t), "authors", bson.A{strings.Repeat("a", 200)})},
		{"publishedYear 1450", with(book(t), "publishedYear", int32(1450))},
		{"publishedYear 2100", with(book(t), "publishedYear", int32(2100))},
		{"pages 1", with(book(t), "pages", int32(1))},
		{"pages 10000", with(book(t), "pages", int32(10000))},
		{"language en", with(book(t), "language", "en")},
		{"isbn starting with 979", with(book(t), "isbn", "9791234567890")},
	}
	for _, tc := range accepted {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			if _, err := books.InsertOne(ctx, tc.doc); err != nil {
				t.Fatalf("insert: %v", err)
			}
		})
	}

	rejected := []struct {
		name string
		doc  bson.D
	}{
		{"missing isbn", without(book(t), "isbn")},
		{"missing title", without(book(t), "title")},
		{"missing authors", without(book(t), "authors")},
		{"missing createdAt", without(book(t), "createdAt")},
		{"missing updatedAt", without(book(t), "updatedAt")},
		{"missing version", without(book(t), "version")},
		{"isbn with 12 digits", with(book(t), "isbn", "978123456789")},
		{"isbn with 14 digits", with(book(t), "isbn", "97812345678901")},
		{"isbn with letters", with(book(t), "isbn", "978123456789X")},
		{"isbn with hyphens", with(book(t), "isbn", "978-1-23-456789-0")},
		{"isbn starting with 977", with(book(t), "isbn", "9771234567890")},
		{"empty title", with(book(t), "title", "")},
		{"title of 301 characters", with(book(t), "title", strings.Repeat("t", 301))},
		{"empty authors", with(book(t), "authors", bson.A{})},
		{"21 authors", with(book(t), "authors", authors(21))},
		{"empty author name", with(book(t), "authors", bson.A{"Alan", ""})},
		{"author name of 201 characters", with(book(t), "authors", bson.A{strings.Repeat("a", 201)})},
		{"publishedYear 1449", with(book(t), "publishedYear", int32(1449))},
		{"publishedYear 2101", with(book(t), "publishedYear", int32(2101))},
		{"pages 0", with(book(t), "pages", int32(0))},
		{"pages 10001", with(book(t), "pages", int32(10001))},
		{"language EN", with(book(t), "language", "EN")},
		{"language eng", with(book(t), "language", "eng")},
		{"extra field price", with(book(t), "price", 9.99)},
		{"int32 _id", with(book(t), "_id", int32(7))},
		{"version 0", with(book(t), "version", int64(0))},
		{"int32 version", with(book(t), "version", int32(1))},
		{"createdAt as string", with(book(t), "createdAt", "2024-01-01")},
		{"title as number", with(book(t), "title", int32(5))},
	}
	for _, tc := range rejected {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			_, err := books.InsertOne(ctx, tc.doc)
			requireValidationFailure(t, err)
		})
	}
}

func TestBooksIsbnIsUnique(t *testing.T) {
	_, books := migratedBooks(t)
	ctx := context.Background()
	first := book(t)
	if _, err := books.InsertOne(ctx, first); err != nil {
		t.Fatalf("insert first: %v", err)
	}
	second := with(book(t), "isbn", first[1].Value)
	_, err := books.InsertOne(ctx, second)
	if err == nil {
		t.Fatal("insert of duplicate isbn succeeded")
	}
	if !mongo.IsDuplicateKeyError(err) {
		t.Fatalf("error = %v, want duplicate key error", err)
	}
}

func TestBooksIndexes(t *testing.T) {
	_, books := migratedBooks(t)
	ctx := context.Background()
	cur, err := books.Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	var specs []struct {
		Name   string `bson:"name"`
		Key    bson.D `bson:"key"`
		Unique bool   `bson:"unique"`
	}
	if err := cur.All(ctx, &specs); err != nil {
		t.Fatalf("read indexes: %v", err)
	}
	byName := map[string]struct {
		Key    bson.D
		Unique bool
	}{}
	for _, s := range specs {
		byName[s.Name] = struct {
			Key    bson.D
			Unique bool
		}{s.Key, s.Unique}
	}

	isbn, ok := byName["isbn_unique"]
	if !ok {
		t.Fatalf("index isbn_unique missing; have %v", byName)
	}
	if !isbn.Unique || len(isbn.Key) != 1 || isbn.Key[0].Key != "isbn" || !isOne(isbn.Key[0].Value) {
		t.Errorf("isbn_unique = %+v, want unique {isbn: 1}", isbn)
	}
	authorsIdx, ok := byName["authors_lookup"]
	if !ok {
		t.Fatalf("index authors_lookup missing; have %v", byName)
	}
	if authorsIdx.Unique || len(authorsIdx.Key) != 1 || authorsIdx.Key[0].Key != "authors" || !isOne(authorsIdx.Key[0].Value) {
		t.Errorf("authors_lookup = %+v, want non-unique {authors: 1}", authorsIdx)
	}
}

func isOne(v any) bool {
	switch n := v.(type) {
	case int32:
		return n == 1
	case int64:
		return n == 1
	case float64:
		return n == 1
	}
	return false
}

func TestMigrationUpIsIdempotent(t *testing.T) {
	db, books := migratedBooks(t)
	ctx := context.Background()
	if _, err := books.InsertOne(ctx, book(t)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	optionsBefore, indexesBefore := collectionState(t, db)

	runMigrations(t, db)

	optionsAfter, indexesAfter := collectionState(t, db)
	if !slices.Equal(optionsBefore, optionsAfter) {
		t.Errorf("collection options changed:\nbefore %v\nafter  %v", optionsBefore, optionsAfter)
	}
	if !slices.Equal(indexesBefore, indexesAfter) {
		t.Errorf("indexes changed:\nbefore %v\nafter  %v", indexesBefore, indexesAfter)
	}
	if n, err := books.CountDocuments(ctx, bson.D{}); err != nil || n != 1 {
		t.Errorf("document count = %d, %v, want 1", n, err)
	}
	// The validator is still in force.
	_, err := books.InsertOne(ctx, with(book(t), "price", 1.0))
	requireValidationFailure(t, err)
}

// collectionState returns the books collection options and index definitions as extended JSON.
func collectionState(t *testing.T, db *mongo.Database) (opts, indexes []string) {
	t.Helper()
	ctx := context.Background()
	specs, err := db.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: BooksCollection}})
	if err != nil || len(specs) != 1 {
		t.Fatalf("list collection specs: %v (%d found)", err, len(specs))
	}
	opts = append(opts, specs[0].Options.String())

	cur, err := db.Collection(BooksCollection).Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	var docs []bson.Raw
	if err := cur.All(ctx, &docs); err != nil {
		t.Fatalf("read indexes: %v", err)
	}
	for _, d := range docs {
		indexes = append(indexes, d.String())
	}
	return opts, indexes
}
