package e2e_test

// End-to-end tests of the catalog storage: the memory and MongoDB repositories behave the same
// through the wired application, and STORAGE=mongo stores documents the S-01 validator accepts.
// Every top-level identifier is prefixed with "storage".
//
// MongoDB tests run only when MONGO_TEST_URI is set. Each of them uses its own database
// test_<random hex>, dropped when the test ends, because parallel agents share one server.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"orch-test/backend/internal/bootstrap"
	"orch-test/backend/internal/oas"
)

// storageEnv is a running application plus, for MongoDB, direct access to its database.
type storageEnv struct {
	client *oas.Client
	db     *mongo.Database // nil for memory storage
}

// storageBackends are the storages every scenario that has to behave the same runs against.
var storageBackends = []struct {
	name  string
	mongo bool
}{
	{"memory storage", false},
	{"mongodb storage", true},
}

// storageStart starts a fresh application on memory storage, or on a fresh MongoDB database
// (skipping the test if MONGO_TEST_URI is not set).
func storageStart(t *testing.T, useMongo bool) storageEnv {
	t.Helper()
	cfg := bootstrap.Config{Storage: bootstrap.StorageMemory, RateLimit: generousLimits()}
	var db *mongo.Database
	if useMongo {
		cfg, db = storageMongoConfig(t)
	}
	app, err := bootstrap.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("bootstrap.New: %v", err)
	}
	srv := httptest.NewServer(app.Handler)
	t.Cleanup(func() {
		srv.Close()
		_ = app.Close(context.Background())
	})
	client, err := oas.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return storageEnv{client: client, db: db}
}

// storageMongoConfig returns a STORAGE=mongo config for a fresh database test_<random hex>, and
// a handle on that database. The database is dropped when the test ends.
func storageMongoConfig(t *testing.T) (bootstrap.Config, *mongo.Database) {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI is not set; skipping MongoDB e2e test")
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "test_" + hex.EncodeToString(suffix[:])

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect to %s: %v", uri, err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		t.Fatalf("ping %s: %v", uri, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Database(name).Drop(ctx)
		_ = client.Disconnect(ctx)
	})
	cfg := bootstrap.Config{Storage: bootstrap.StorageMongo, MongoURI: uri, MongoDB: name, RateLimit: generousLimits()}
	return cfg, client.Database(name)
}

// storageISBN returns a valid, normalized ISBN-13 that is distinct for every n.
func storageISBN(n int) string {
	digits := fmt.Sprintf("978%09d", n)
	sum := 0
	for i, r := range digits {
		d := int(r - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return digits + fmt.Sprint((10-sum%10)%10)
}

func storageBookInput(n int, authors ...string) *oas.BookInput {
	return &oas.BookInput{
		Isbn:          storageISBN(n),
		Title:         fmt.Sprintf("Storage Book %d", n),
		Authors:       authors,
		PublishedYear: oas.NewOptInt(2008),
		Language:      oas.NewOptString("en"),
		Pages:         oas.NewOptInt(464),
	}
}

func storageUpdateFrom(in *oas.BookInput, version int64) *oas.BookUpdate {
	return &oas.BookUpdate{
		Isbn:          in.Isbn,
		Title:         in.Title,
		Authors:       in.Authors,
		PublishedYear: in.PublishedYear,
		Language:      in.Language,
		Pages:         in.Pages,
		Version:       version,
	}
}

func storageCreate(t *testing.T, c *oas.Client, in *oas.BookInput) *oas.Book {
	t.Helper()
	book, err := c.CreateBook(context.Background(), in)
	if err != nil {
		t.Fatalf("CreateBook(%s): %v", in.Isbn, err)
	}
	return book
}

func storageList(t *testing.T, c *oas.Client, params oas.ListBooksParams) *oas.BookList {
	t.Helper()
	list, err := c.ListBooks(context.Background(), params)
	if err != nil {
		t.Fatalf("ListBooks(%+v): %v", params, err)
	}
	return list
}

func storageIDs(books []oas.Book) []int64 {
	ids := make([]int64, len(books))
	for i, b := range books {
		ids[i] = b.ID
	}
	return ids
}

// storageRequireError asserts that err is an API error with the given status and a message.
func storageRequireError(t *testing.T, what string, err error, status int) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: got success, want status %d", what, status)
	}
	var apiErr *oas.ErrorStatusCode
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: got %T (%v), want *oas.ErrorStatusCode", what, err, err)
	}
	if apiErr.StatusCode != status {
		t.Fatalf("%s: status = %d (%s), want %d", what, apiErr.StatusCode, apiErr.Response.Error, status)
	}
	if apiErr.Response.Error == "" {
		t.Errorf("%s: error body has no message", what)
	}
}

// The memory and MongoDB repositories pass the same contract suite (`repotest`): add, get, list with
// filter and paging, update with version check, delete, duplicate ISBN, unknown ID.
//
// The suite itself runs in the adapters' package tests. This is one API-level scenario through the
// wired app that touches each of these operations; both storages must give the same results.
func TestStorageBehavesTheSameForBothRepositories(t *testing.T) {
	for _, backend := range storageBackends {
		t.Run(backend.name, func(t *testing.T) {
			env := storageStart(t, backend.mongo)
			c := env.client
			ctx := context.Background()

			// add
			inA := storageBookInput(1, "Ada Author", "Bob Writer")
			inB := storageBookInput(2, "Bob Writer")
			inC := storageBookInput(3, "Cy Penman")
			a, b, cc := storageCreate(t, c, inA), storageCreate(t, c, inB), storageCreate(t, c, inC)
			if a.ID <= 0 || b.ID <= a.ID || cc.ID <= b.ID {
				t.Fatalf("ids = %d, %d, %d, want positive and ascending", a.ID, b.ID, cc.ID)
			}
			if a.Version != 1 || a.Isbn != inA.Isbn || a.Title != inA.Title || !slices.Equal(a.Authors, inA.Authors) {
				t.Errorf("created book = %+v, want the input with version 1", a)
			}

			// get
			got, err := c.GetBook(ctx, oas.GetBookParams{ID: b.ID})
			if err != nil {
				t.Fatalf("GetBook: %v", err)
			}
			if got.ID != b.ID || got.Isbn != inB.Isbn || got.Title != inB.Title || got.Version != 1 ||
				got.PublishedYear.Value != 2008 || got.Language.Value != "en" || got.Pages.Value != 464 {
				t.Errorf("GetBook = %+v, want %+v", got, b)
			}

			// list: sorted by id, filter by author, paging
			all := storageList(t, c, oas.ListBooksParams{})
			if all.Total != 3 || !slices.Equal(storageIDs(all.Items), []int64{a.ID, b.ID, cc.ID}) {
				t.Errorf("list = total %d ids %v, want 3 / [%d %d %d]", all.Total, storageIDs(all.Items), a.ID, b.ID, cc.ID)
			}
			bob := storageList(t, c, oas.ListBooksParams{Author: oas.NewOptString("Bob Writer")})
			if bob.Total != 2 || !slices.Equal(storageIDs(bob.Items), []int64{a.ID, b.ID}) {
				t.Errorf("author filter = total %d ids %v, want 2 / [%d %d]", bob.Total, storageIDs(bob.Items), a.ID, b.ID)
			}
			if none := storageList(t, c, oas.ListBooksParams{Author: oas.NewOptString("Bob")}); none.Total != 0 || len(none.Items) != 0 {
				t.Errorf("author filter is not an exact match: total %d, items %d", none.Total, len(none.Items))
			}
			page := storageList(t, c, oas.ListBooksParams{Limit: oas.NewOptInt(1), Offset: oas.NewOptInt(1)})
			if page.Total != 3 || !slices.Equal(storageIDs(page.Items), []int64{b.ID}) {
				t.Errorf("limit 1 offset 1 = total %d ids %v, want 3 / [%d]", page.Total, storageIDs(page.Items), b.ID)
			}
			pastEnd := storageList(t, c, oas.ListBooksParams{Offset: oas.NewOptInt(3)})
			if pastEnd.Total != 3 || len(pastEnd.Items) != 0 {
				t.Errorf("offset past the end = total %d, items %d, want 3 / 0", pastEnd.Total, len(pastEnd.Items))
			}
			bobPage := storageList(t, c, oas.ListBooksParams{
				Author: oas.NewOptString("Bob Writer"), Limit: oas.NewOptInt(1), Offset: oas.NewOptInt(1),
			})
			if bobPage.Total != 2 || !slices.Equal(storageIDs(bobPage.Items), []int64{b.ID}) {
				t.Errorf("author filter + paging = total %d ids %v, want 2 / [%d]", bobPage.Total, storageIDs(bobPage.Items), b.ID)
			}

			// update with version check
			upd := storageUpdateFrom(inA, 1)
			upd.Title = "Storage Book 1, revised"
			upd.Pages = oas.OptInt{} // clearing an optional field removes it
			updated, err := c.UpdateBook(ctx, upd, oas.UpdateBookParams{ID: a.ID})
			if err != nil {
				t.Fatalf("UpdateBook: %v", err)
			}
			if updated.Version != 2 || updated.Title != upd.Title || updated.Pages.Set {
				t.Errorf("updated book = %+v, want version 2, new title and no pages", updated)
			}
			_, err = c.UpdateBook(ctx, upd, oas.UpdateBookParams{ID: a.ID})
			storageRequireError(t, "update with a stale version", err, http.StatusConflict)
			again, err := c.GetBook(ctx, oas.GetBookParams{ID: a.ID})
			if err != nil {
				t.Fatalf("GetBook: %v", err)
			}
			if again.Version != 2 || again.Title != upd.Title || again.Pages.Set {
				t.Errorf("book after the rejected update = %+v, want the first update only", again)
			}

			// duplicate ISBN: on create and on update
			_, err = c.CreateBook(ctx, storageBookInput(2, "Someone Else"))
			storageRequireError(t, "create with a duplicate ISBN", err, http.StatusConflict)
			clash := storageUpdateFrom(inC, 1)
			clash.Isbn = inA.Isbn
			_, err = c.UpdateBook(ctx, clash, oas.UpdateBookParams{ID: cc.ID})
			storageRequireError(t, "update to a duplicate ISBN", err, http.StatusConflict)
			if total := storageList(t, c, oas.ListBooksParams{}).Total; total != 3 {
				t.Errorf("total = %d after the rejected writes, want 3", total)
			}

			// unknown ID
			const unknown = 987654321
			_, err = c.GetBook(ctx, oas.GetBookParams{ID: unknown})
			storageRequireError(t, "get an unknown id", err, http.StatusNotFound)
			_, err = c.UpdateBook(ctx, storageUpdateFrom(inA, 1), oas.UpdateBookParams{ID: unknown})
			storageRequireError(t, "update an unknown id", err, http.StatusNotFound)
			storageRequireError(t, "delete an unknown id", c.DeleteBook(ctx, oas.DeleteBookParams{ID: unknown}), http.StatusNotFound)

			// delete
			if err := c.DeleteBook(ctx, oas.DeleteBookParams{ID: b.ID}); err != nil {
				t.Fatalf("DeleteBook: %v", err)
			}
			_, err = c.GetBook(ctx, oas.GetBookParams{ID: b.ID})
			storageRequireError(t, "get a deleted book", err, http.StatusNotFound)
			storageRequireError(t, "delete a deleted book", c.DeleteBook(ctx, oas.DeleteBookParams{ID: b.ID}), http.StatusNotFound)
			rest := storageList(t, c, oas.ListBooksParams{})
			if rest.Total != 2 || !slices.Equal(storageIDs(rest.Items), []int64{a.ID, cc.ID}) {
				t.Errorf("list after delete = total %d ids %v, want 2 / [%d %d]", rest.Total, storageIDs(rest.Items), a.ID, cc.ID)
			}
			// the ISBN of a deleted book is free again
			storageCreate(t, c, storageBookInput(2, "Bob Writer"))
		})
	}
}

// storageFindRaw reads the raw book document with the given id.
func storageFindRaw(t *testing.T, db *mongo.Database, id int64) bson.M {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var doc bson.M
	if err := db.Collection("books").FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc); err != nil {
		t.Fatalf("read raw book %d: %v", id, err)
	}
	return doc
}

func storageCounterSeq(t *testing.T, db *mongo.Database) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var doc bson.M
	if err := db.Collection("counters").FindOne(ctx, bson.D{{Key: "_id", Value: "books"}}).Decode(&doc); err != nil {
		t.Fatalf("read counters document {_id: books}: %v", err)
	}
	seq, ok := doc["seq"].(int64)
	if !ok {
		t.Fatalf("counters seq = %v (%T), want int64", doc["seq"], doc["seq"])
	}
	return seq
}

func storageKeys(doc bson.M) []string {
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// With `STORAGE=mongo`, a stored book document passes the S-01 validator, and IDs come from the
// `counters` document.
func TestStorageMongoDocumentsMatchTheSchemaAndIDsComeFromTheCounter(t *testing.T) {
	env := storageStart(t, true)
	c, db := env.client, env.db
	ctx := context.Background()

	// the books collection is created by the migration with a validator (inserting below passes it)
	specs, err := db.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: "books"}})
	if err != nil || len(specs) != 1 {
		t.Fatalf("books collection specification: %v (found %d)", err, len(specs))
	}
	if _, err := specs[0].Options.LookupErr("validator"); err != nil {
		t.Fatalf("books collection has no validator: %v", err)
	}

	complete := storageCreate(t, c, storageBookInput(10, "Ada Author", "Bob Writer"))
	minimal := storageCreate(t, c, &oas.BookInput{
		Isbn: storageISBN(11), Title: "Minimal", Authors: []string{"Cy Penman"},
	})

	tests := []struct {
		name     string
		book     *oas.Book
		wantKeys []string
	}{
		{"complete book", complete, []string{
			"_id", "authors", "createdAt", "isbn", "language", "pages", "publishedYear", "title", "updatedAt", "version"}},
		{"minimal book", minimal, []string{
			"_id", "authors", "createdAt", "isbn", "title", "updatedAt", "version"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := storageFindRaw(t, db, tt.book.ID)
			if id, ok := doc["_id"].(int64); !ok || id != tt.book.ID {
				t.Errorf("_id = %v (%T), want int64 %d", doc["_id"], doc["_id"], tt.book.ID)
			}
			if v, ok := doc["version"].(int64); !ok || v != 1 {
				t.Errorf("version = %v (%T), want int64 1", doc["version"], doc["version"])
			}
			if got := storageKeys(doc); !slices.Equal(got, tt.wantKeys) {
				t.Errorf("fields = %v, want exactly %v (optional fields absent, not null)", got, tt.wantKeys)
			}
			for k, v := range doc {
				if v == nil {
					t.Errorf("field %q is null", k)
				}
			}
			if doc["isbn"] != tt.book.Isbn || doc["title"] != tt.book.Title {
				t.Errorf("isbn/title = %v/%v, want %s/%s", doc["isbn"], doc["title"], tt.book.Isbn, tt.book.Title)
			}
			for _, k := range []string{"publishedYear", "pages"} {
				if v, ok := doc[k]; ok {
					if _, isInt := v.(int32); !isInt {
						t.Errorf("%s = %v (%T), want int32", k, v, v)
					}
				}
			}
			for _, k := range []string{"createdAt", "updatedAt"} {
				if _, ok := doc[k].(bson.DateTime); !ok {
					t.Errorf("%s = %v (%T), want a date", k, doc[k], doc[k])
				}
			}
		})
	}

	// an update keeps the document within the schema; clearing optional fields removes them
	upd := storageUpdateFrom(storageBookInput(10, "Ada Author"), 1)
	upd.PublishedYear, upd.Language, upd.Pages = oas.OptInt{}, oas.OptString{}, oas.OptInt{}
	if _, err := c.UpdateBook(ctx, upd, oas.UpdateBookParams{ID: complete.ID}); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}
	doc := storageFindRaw(t, db, complete.ID)
	wantKeys := []string{"_id", "authors", "createdAt", "isbn", "title", "updatedAt", "version"}
	if got := storageKeys(doc); !slices.Equal(got, wantKeys) {
		t.Errorf("fields after clearing the optional ones = %v, want %v", got, wantKeys)
	}
	if v, ok := doc["version"].(int64); !ok || v != 2 {
		t.Errorf("version after the update = %v (%T), want int64 2", doc["version"], doc["version"])
	}

	// counters {_id: "books"}.seq equals the highest id
	highest := max(complete.ID, minimal.ID)
	if seq := storageCounterSeq(t, db); seq != highest {
		t.Errorf("counter seq = %d, want the highest id %d", seq, highest)
	}

	// after deleting the last book, the next id is seq+1, not the deleted one's
	if err := c.DeleteBook(ctx, oas.DeleteBookParams{ID: highest}); err != nil {
		t.Fatalf("DeleteBook: %v", err)
	}
	if seq := storageCounterSeq(t, db); seq != highest {
		t.Errorf("counter seq = %d after the delete, want it unchanged at %d", seq, highest)
	}
	next := storageCreate(t, c, storageBookInput(12, "Dee Editor"))
	if next.ID != highest+1 {
		t.Errorf("new id = %d after deleting the last book, want seq+1 = %d", next.ID, highest+1)
	}
	if seq := storageCounterSeq(t, db); seq != next.ID {
		t.Errorf("counter seq = %d, want the new highest id %d", seq, next.ID)
	}
}

// With MongoDB, two concurrent updates of the same book with the same version: exactly one
// succeeds, the other gets a version conflict.
func TestStorageConcurrentUpdatesWithTheSameVersionAllowExactlyOne(t *testing.T) {
	for _, backend := range storageBackends {
		t.Run(backend.name, func(t *testing.T) {
			env := storageStart(t, backend.mongo)
			c := env.client
			ctx := context.Background()

			in := storageBookInput(20, "Ada Author")
			book := storageCreate(t, c, in)

			const writers = 8
			type result struct {
				title string
				err   error
			}
			results := make([]result, writers)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range writers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					upd := storageUpdateFrom(in, book.Version)
					upd.Title = fmt.Sprintf("Concurrent edit %d", i)
					<-start
					_, err := c.UpdateBook(ctx, upd, oas.UpdateBookParams{ID: book.ID})
					results[i] = result{title: upd.Title, err: err}
				}()
			}
			close(start)
			wg.Wait()

			var winner string
			succeeded := 0
			for i, r := range results {
				if r.err == nil {
					succeeded++
					winner = r.title
					continue
				}
				var apiErr *oas.ErrorStatusCode
				if !errors.As(r.err, &apiErr) || apiErr.StatusCode != http.StatusConflict {
					t.Errorf("writer %d: error = %v, want a 409 version conflict", i, r.err)
				}
			}
			if succeeded != 1 {
				t.Fatalf("%d of %d concurrent updates succeeded, want exactly 1", succeeded, writers)
			}

			got, err := c.GetBook(ctx, oas.GetBookParams{ID: book.ID})
			if err != nil {
				t.Fatalf("GetBook: %v", err)
			}
			if got.Version != 2 || got.Title != winner {
				t.Errorf("book = version %d %q, want version 2 %q (the winner's edit)", got.Version, got.Title, winner)
			}
		})
	}
}
