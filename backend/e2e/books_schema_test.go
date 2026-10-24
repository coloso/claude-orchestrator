package e2e_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"orch-test/backend/internal/bootstrap"
)

const booksDocumentValidationFailure = 121

var booksSeq atomic.Int64

// booksTestDB starts the app against a fresh database, so the schema
// migrations run, and returns the books collection.
func booksTestDB(t *testing.T) *mongo.Collection {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI not set")
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random database name: %v", err)
	}
	dbName := "test_" + hex.EncodeToString(b[:])

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Database(dbName).Drop(ctx); err != nil {
			t.Errorf("drop %s: %v", dbName, err)
		}
		_ = client.Disconnect(ctx)
	})

	app, err := bootstrap.New(ctx, bootstrap.Config{Storage: bootstrap.StorageMongo, MongoURI: uri, MongoDB: dbName})
	if err != nil {
		t.Fatalf("bootstrap.New: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("close app: %v", err)
		}
	})
	return client.Database(dbName).Collection("books")
}

// booksValidBook returns a fresh, complete, valid book with a unique _id and isbn.
func booksValidBook() bson.D {
	n := booksSeq.Add(1)
	now := time.Now().UTC().Truncate(time.Millisecond)
	return bson.D{
		{Key: "_id", Value: int64(n)},
		{Key: "isbn", Value: fmt.Sprintf("978%010d", n)},
		{Key: "title", Value: "The Go Programming Language"},
		{Key: "authors", Value: bson.A{"Alan Donovan", "Brian Kernighan"}},
		{Key: "publishedYear", Value: int32(2015)},
		{Key: "language", Value: "en"},
		{Key: "pages", Value: int32(380)},
		{Key: "createdAt", Value: now},
		{Key: "updatedAt", Value: now},
		{Key: "version", Value: int64(1)},
	}
}

// booksMinimalBook has only the required fields.
func booksMinimalBook() bson.D {
	return booksWithout(booksValidBook(), "publishedYear", "language", "pages")
}

// booksWith returns a copy of doc with key set (replaced or appended).
func booksWith(doc bson.D, key string, value any) bson.D {
	out := make(bson.D, 0, len(doc)+1)
	found := false
	for _, e := range doc {
		if e.Key == key {
			out = append(out, bson.E{Key: key, Value: value})
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		out = append(out, bson.E{Key: key, Value: value})
	}
	return out
}

// booksWithout returns a copy of doc without the given keys.
func booksWithout(doc bson.D, keys ...string) bson.D {
	out := make(bson.D, 0, len(doc))
outer:
	for _, e := range doc {
		for _, k := range keys {
			if e.Key == k {
				continue outer
			}
		}
		out = append(out, e)
	}
	return out
}

func booksAuthors(n int, length int) bson.A {
	a := make(bson.A, n)
	for i := range a {
		a[i] = strings.Repeat("a", length)
	}
	return a
}

func booksIsValidationFailure(err error) bool {
	var we mongo.WriteException
	if !errors.As(err, &we) {
		return false
	}
	for _, e := range we.WriteErrors {
		if e.Code == booksDocumentValidationFailure {
			return true
		}
	}
	return false
}

type booksCase struct {
	name   string
	mutate func(bson.D) bson.D
	valid  bool
}

func booksRunCases(t *testing.T, cases []booksCase) {
	t.Helper()
	coll := booksTestDB(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := tc.mutate(booksValidBook())
			_, err := coll.InsertOne(context.Background(), doc)
			if tc.valid {
				if err != nil {
					t.Fatalf("insert rejected, want accepted: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("insert accepted, want rejected with code 121")
			}
			if !booksIsValidationFailure(err) {
				t.Fatalf("error = %v, want WriteException with code %d", err, booksDocumentValidationFailure)
			}
		})
	}
}

func TestBooksAcceptCompleteAndMinimalDocuments(t *testing.T) {
	booksRunCases(t, []booksCase{
		{"a complete valid book", func(d bson.D) bson.D { return d }, true},
		{"a book with only the required fields", func(d bson.D) bson.D { return booksMinimalBook() }, true},
	})
}

func TestBooksRejectMissingRequiredFields(t *testing.T) {
	var cases []booksCase
	for _, f := range []string{"isbn", "title", "authors", "createdAt", "updatedAt", "version"} {
		cases = append(cases, booksCase{"without " + f, func(d bson.D) bson.D { return booksWithout(d, f) }, false})
	}
	booksRunCases(t, cases)
}

func TestBooksIsbnFormat(t *testing.T) {
	booksRunCases(t, []booksCase{
		{"978 prefix with 13 digits", func(d bson.D) bson.D { return booksWith(d, "isbn", "9780000000001") }, true},
		{"979 prefix with 13 digits", func(d bson.D) bson.D { return booksWith(d, "isbn", "9790000000002") }, true},
		{"12 digits", func(d bson.D) bson.D { return booksWith(d, "isbn", "978000000003") }, false},
		{"14 digits", func(d bson.D) bson.D { return booksWith(d, "isbn", "97800000000004") }, false},
		{"letters", func(d bson.D) bson.D { return booksWith(d, "isbn", "978000000000X") }, false},
		{"hyphens", func(d bson.D) bson.D { return booksWith(d, "isbn", "978-0-00-000005-6") }, false},
		{"977 prefix", func(d bson.D) bson.D { return booksWith(d, "isbn", "9770000000006") }, false},
		{"not a string", func(d bson.D) bson.D { return booksWith(d, "isbn", int64(9780000000007)) }, false},
	})
}

func TestBooksRejectDuplicateIsbn(t *testing.T) {
	coll := booksTestDB(t)
	ctx := context.Background()
	first := booksValidBook()
	if _, err := coll.InsertOne(ctx, first); err != nil {
		t.Fatalf("insert first: %v", err)
	}
	isbn := first[1].Value

	second := booksWith(booksValidBook(), "isbn", isbn)
	_, err := coll.InsertOne(ctx, second)
	if !mongo.IsDuplicateKeyError(err) {
		t.Fatalf("duplicate isbn error = %v, want duplicate key error", err)
	}

	other := booksValidBook()
	if _, err := coll.InsertOne(ctx, other); err != nil {
		t.Fatalf("a different isbn must be accepted: %v", err)
	}
}

func TestBooksTitleAndAuthorsBounds(t *testing.T) {
	booksRunCases(t, []booksCase{
		{"title of 1 character", func(d bson.D) bson.D { return booksWith(d, "title", "x") }, true},
		{"title of 300 characters", func(d bson.D) bson.D { return booksWith(d, "title", strings.Repeat("t", 300)) }, true},
		{"title of 300 multibyte characters", func(d bson.D) bson.D { return booksWith(d, "title", strings.Repeat("ä", 300)) }, true},
		{"empty title", func(d bson.D) bson.D { return booksWith(d, "title", "") }, false},
		{"title of 301 characters", func(d bson.D) bson.D { return booksWith(d, "title", strings.Repeat("t", 301)) }, false},
		{"title of wrong type", func(d bson.D) bson.D { return booksWith(d, "title", int32(1)) }, false},
		{"1 author", func(d bson.D) bson.D { return booksWith(d, "authors", booksAuthors(1, 5)) }, true},
		{"20 authors", func(d bson.D) bson.D { return booksWith(d, "authors", booksAuthors(20, 5)) }, true},
		{"empty authors array", func(d bson.D) bson.D { return booksWith(d, "authors", bson.A{}) }, false},
		{"21 authors", func(d bson.D) bson.D { return booksWith(d, "authors", booksAuthors(21, 5)) }, false},
		{"author name of 1 character", func(d bson.D) bson.D { return booksWith(d, "authors", booksAuthors(1, 1)) }, true},
		{"author name of 200 characters", func(d bson.D) bson.D { return booksWith(d, "authors", booksAuthors(1, 200)) }, true},
		{"empty author name", func(d bson.D) bson.D { return booksWith(d, "authors", bson.A{"ok", ""}) }, false},
		{"author name of 201 characters", func(d bson.D) bson.D { return booksWith(d, "authors", booksAuthors(1, 201)) }, false},
		{"authors that are not strings", func(d bson.D) bson.D { return booksWith(d, "authors", bson.A{int32(1)}) }, false},
		{"authors that is not an array", func(d bson.D) bson.D { return booksWith(d, "authors", "Alan") }, false},
	})
}

func TestBooksOptionalFieldBounds(t *testing.T) {
	booksRunCases(t, []booksCase{
		{"publishedYear 1450", func(d bson.D) bson.D { return booksWith(d, "publishedYear", int32(1450)) }, true},
		{"publishedYear 2100", func(d bson.D) bson.D { return booksWith(d, "publishedYear", int32(2100)) }, true},
		{"publishedYear 1449", func(d bson.D) bson.D { return booksWith(d, "publishedYear", int32(1449)) }, false},
		{"publishedYear 2101", func(d bson.D) bson.D { return booksWith(d, "publishedYear", int32(2101)) }, false},
		{"publishedYear as string", func(d bson.D) bson.D { return booksWith(d, "publishedYear", "2015") }, false},
		{"pages 1", func(d bson.D) bson.D { return booksWith(d, "pages", int32(1)) }, true},
		{"pages 10000", func(d bson.D) bson.D { return booksWith(d, "pages", int32(10000)) }, true},
		{"pages 0", func(d bson.D) bson.D { return booksWith(d, "pages", int32(0)) }, false},
		{"pages 10001", func(d bson.D) bson.D { return booksWith(d, "pages", int32(10001)) }, false},
		{"pages as string", func(d bson.D) bson.D { return booksWith(d, "pages", "380") }, false},
		{"language en", func(d bson.D) bson.D { return booksWith(d, "language", "en") }, true},
		{"language EN", func(d bson.D) bson.D { return booksWith(d, "language", "EN") }, false},
		{"language eng", func(d bson.D) bson.D { return booksWith(d, "language", "eng") }, false},
		{"language e", func(d bson.D) bson.D { return booksWith(d, "language", "e") }, false},
		{"language with a digit", func(d bson.D) bson.D { return booksWith(d, "language", "e1") }, false},
	})
}

func TestBooksRejectUnknownFields(t *testing.T) {
	booksRunCases(t, []booksCase{
		{"extra field price", func(d bson.D) bson.D { return booksWith(d, "price", int32(10)) }, false},
		{"extra field on a minimal book", func(d bson.D) bson.D { return booksWith(booksMinimalBook(), "price", 9.99) }, false},
	})
}

func TestBooksIdAndVersionTypes(t *testing.T) {
	booksRunCases(t, []booksCase{
		{"int64 _id", func(d bson.D) bson.D { return d }, true},
		{"int32 _id", func(d bson.D) bson.D { return booksWith(d, "_id", int32(1_000_001)) }, false},
		{"version 1", func(d bson.D) bson.D { return booksWith(d, "version", int64(1)) }, true},
		{"version 2", func(d bson.D) bson.D { return booksWith(d, "version", int64(2)) }, true},
		{"version 0", func(d bson.D) bson.D { return booksWith(d, "version", int64(0)) }, false},
		{"version -1", func(d bson.D) bson.D { return booksWith(d, "version", int64(-1)) }, false},
		{"int32 version", func(d bson.D) bson.D { return booksWith(d, "version", int32(1)) }, false},
		{"createdAt as string", func(d bson.D) bson.D { return booksWith(d, "createdAt", "2020-01-01") }, false},
		{"updatedAt as string", func(d bson.D) bson.D { return booksWith(d, "updatedAt", "2020-01-01") }, false},
	})
}

func TestBooksIndexes(t *testing.T) {
	coll := booksTestDB(t)
	ctx := context.Background()

	cur, err := coll.Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	var indexes []struct {
		Name   string `bson:"name"`
		Key    bson.D `bson:"key"`
		Unique bool   `bson:"unique"`
	}
	if err := cur.All(ctx, &indexes); err != nil {
		t.Fatalf("read indexes: %v", err)
	}

	tests := []struct {
		name   string
		field  string
		unique bool
	}{
		{"isbn_unique", "isbn", true},
		{"authors_lookup", "authors", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, ix := range indexes {
				if ix.Name != tc.name {
					continue
				}
				if len(ix.Key) != 1 || ix.Key[0].Key != tc.field {
					t.Fatalf("keys = %v, want {%s: 1}", ix.Key, tc.field)
				}
				switch v := ix.Key[0].Value.(type) {
				case int32:
					if v != 1 {
						t.Fatalf("key direction = %v, want 1", v)
					}
				case int64:
					if v != 1 {
						t.Fatalf("key direction = %v, want 1", v)
					}
				case float64:
					if v != 1 {
						t.Fatalf("key direction = %v, want 1", v)
					}
				default:
					t.Fatalf("key direction = %#v, want 1", v)
				}
				if ix.Unique != tc.unique {
					t.Fatalf("unique = %v, want %v", ix.Unique, tc.unique)
				}
				return
			}
			t.Fatalf("index %s not found in %v", tc.name, indexes)
		})
	}
}
