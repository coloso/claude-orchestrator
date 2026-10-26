package e2e_test

// End-to-end tests of the schema migration platform: bootstrap.New, and
// mongodb.Migrate with synthetic migration sets for the scenarios the real code can't produce yet
// (catalog/0002, a failing migration). Every top-level identifier is prefixed with "migration".
//
// Each test gets its own database, because parallel agents share one server.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"orch-test/backend/internal/bootstrap"
	"orch-test/backend/internal/oas"
	"orch-test/backend/internal/platform/mongodb"
)

// --- helpers ---

// migrationCtx returns a context that ends with the test, bounded so a hung test fails fast.
func migrationCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// migrationTestDB returns a fresh database test_<random hex> and the server URI. The database is
// dropped when the test ends.
func migrationTestDB(t *testing.T) (db *mongo.Database, uri string) {
	t.Helper()
	_, db = e2eTestDB(t)
	return db, e2eMongoURI(t)
}

// migrationNewApp builds the application on the given database.
func migrationNewApp(ctx context.Context, uri, dbName string) (*bootstrap.App, error) {
	return bootstrap.New(ctx, e2eConfig(uri, dbName))
}

// migrationServe serves app with httptest and returns a generated client. The server and the app
// are closed when the test ends.
func migrationServe(t *testing.T, app *bootstrap.App) *oas.Client {
	t.Helper()
	srv := httptest.NewServer(app.Handler)
	t.Cleanup(func() {
		srv.Close()
		_ = app.Close(context.Background())
	})
	client, err := oas.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// migrationStartApp starts the application on db and fails the test if startup fails.
func migrationStartApp(t *testing.T, uri string, db *mongo.Database) *oas.Client {
	t.Helper()
	app, err := migrationNewApp(migrationCtx(t), uri, db.Name())
	if err != nil {
		t.Fatalf("bootstrap.New: %v", err)
	}
	return migrationServe(t, app)
}

func migrationRequireHealthy(t *testing.T, client *oas.Client) {
	t.Helper()
	res, err := client.GetHealth(migrationCtx(t))
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("health status = %q, want ok", res.Status)
	}
}

// migrationLedger returns the documents of schema_migrations, sorted by _id.
func migrationLedger(t *testing.T, db *mongo.Database) []bson.M {
	t.Helper()
	ctx := migrationCtx(t)
	cur, err := db.Collection(mongodb.LedgerCollection).Find(ctx, bson.D{},
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var docs []bson.M
	if err := cur.All(ctx, &docs); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return docs
}

func migrationLedgerIDs(t *testing.T, db *mongo.Database) []string {
	t.Helper()
	ids := []string{}
	for _, d := range migrationLedger(t, db) {
		ids = append(ids, d["_id"].(string))
	}
	return ids
}

func migrationCollectionNames(t *testing.T, db *mongo.Database) []string {
	t.Helper()
	names, err := db.ListCollectionNames(migrationCtx(t), bson.D{})
	if err != nil {
		t.Fatalf("list collections: %v", err)
	}
	slices.Sort(names)
	return names
}

// migrationServerTime reads the server's clock, which decides when a lease has expired.
func migrationServerTime(t *testing.T, db *mongo.Database) time.Time {
	t.Helper()
	var reply struct {
		LocalTime time.Time `bson:"localTime"`
	}
	if err := db.RunCommand(migrationCtx(t), bson.D{{Key: "hello", Value: 1}}).Decode(&reply); err != nil {
		t.Fatalf("read server clock: %v", err)
	}
	return reply.LocalTime
}

// migrationSeedLock inserts a schema lock document held by owner until expiresAt.
func migrationSeedLock(t *testing.T, db *mongo.Database, owner string, expiresAt time.Time) {
	t.Helper()
	_, err := db.Collection(mongodb.LockCollection).InsertOne(migrationCtx(t), bson.D{
		{Key: "_id", Value: "schema"},
		{Key: "owner", Value: owner},
		{Key: "expiresAt", Value: expiresAt},
	})
	if err != nil {
		t.Fatalf("seed schema lock: %v", err)
	}
}

// migrationRecorder counts and orders the calls of synthetic Up steps.
type migrationRecorder struct {
	mu    sync.Mutex
	calls []string
	delay time.Duration // makes an Up step slow, so concurrent instances overlap
}

func (r *migrationRecorder) record(id string) {
	r.mu.Lock()
	r.calls = append(r.calls, id)
	r.mu.Unlock()
}

func (r *migrationRecorder) order() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func (r *migrationRecorder) count(id string) int {
	n := 0
	for _, c := range r.order() {
		if c == id {
			n++
		}
	}
	return n
}

// migrationCreateCollection creates a collection, and does nothing if it exists already.
func migrationCreateCollection(ctx context.Context, db *mongo.Database, name string) error {
	err := db.CreateCollection(ctx, name)
	if se := mongo.ServerError(nil); errors.As(err, &se) && se.HasErrorCode(48) { // NamespaceExists
		return nil
	}
	return err
}

// migrationStep builds a synthetic migration. Its Up step records the call, optionally fails,
// and otherwise idempotently creates a collection named after the ID.
func migrationStep(r *migrationRecorder, id string, fail *atomic.Bool) mongodb.Migration {
	return mongodb.Migration{
		ID:          id,
		Description: "synthetic migration " + id,
		Up: func(ctx context.Context, db *mongo.Database) error {
			r.record(id)
			if r.delay > 0 {
				time.Sleep(r.delay)
			}
			if fail != nil && fail.Load() {
				return errors.New("synthetic failure")
			}
			return migrationCreateCollection(ctx, db, strings.ReplaceAll(id, "/", "_"))
		},
	}
}

// migrationSet builds the synthetic set <name>/0001 ... <name>/000n.
func migrationSet(r *migrationRecorder, name string, n int) []mongodb.Migration {
	set := make([]mongodb.Migration, n)
	for i := range set {
		set[i] = migrationStep(r, fmt.Sprintf("%s/%04d", name, i+1), nil)
	}
	return set
}

func migrationOpts(owner string) mongodb.MigrateOptions {
	return mongodb.MigrateOptions{
		Lease:        10 * time.Second,
		PollInterval: 20 * time.Millisecond,
		Owner:        owner,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func migrationRequireIDs(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// --- 1. first start applies everything, in order, and records it ---

func TestMigrationsRunAndAreRecordedOnAnEmptyDatabase(t *testing.T) {
	t.Parallel()
	db, uri := migrationTestDB(t)

	migrationStartApp(t, uri, db)

	ledger := migrationLedger(t, db)
	if len(ledger) != 1 || ledger[0]["_id"] != "catalog/0001" {
		t.Fatalf("ledger = %v, want exactly catalog/0001", ledger)
	}
	entry := ledger[0]
	if desc, ok := entry["description"].(string); !ok || desc == "" {
		t.Errorf("description = %#v, want a non-empty string", entry["description"])
	}
	appliedAt, ok := entry["appliedAt"].(bson.DateTime)
	if !ok {
		t.Fatalf("appliedAt = %#v (%T), want a date", entry["appliedAt"], entry["appliedAt"])
	}
	if age := time.Since(appliedAt.Time()); age < -time.Minute || age > time.Minute {
		t.Errorf("appliedAt = %v, want about now", appliedAt.Time())
	}
	durationMs, ok := entry["durationMs"].(int64)
	if !ok {
		t.Fatalf("durationMs = %#v (%T), want int64", entry["durationMs"], entry["durationMs"])
	}
	if durationMs < 0 {
		t.Errorf("durationMs = %d, want >= 0", durationMs)
	}
	if names := migrationCollectionNames(t, db); !slices.Contains(names, "books") {
		t.Errorf("collections = %v, want books (created by catalog/0001)", names)
	}
}

func TestMigrationsRunInOrder(t *testing.T) {
	t.Parallel()
	db, _ := migrationTestDB(t)
	r := &migrationRecorder{}
	// The sets run in the given order, each in sequence order.
	sets := [][]mongodb.Migration{
		migrationSet(r, "catalog", 3),
		migrationSet(r, "loans", 2),
	}

	if err := mongodb.Migrate(migrationCtx(t), db, sets, migrationOpts("a")); err != nil {
		t.Fatal(err)
	}

	want := []string{"catalog/0001", "catalog/0002", "catalog/0003", "loans/0001", "loans/0002"}
	migrationRequireIDs(t, r.order(), want)
	migrationRequireIDs(t, migrationLedgerIDs(t, db), want)
	for _, doc := range migrationLedger(t, db) {
		if _, ok := doc["appliedAt"].(bson.DateTime); !ok {
			t.Errorf("%v: appliedAt = %#v, want a date", doc["_id"], doc["appliedAt"])
		}
		if _, ok := doc["durationMs"].(int64); !ok {
			t.Errorf("%v: durationMs = %#v, want int64", doc["_id"], doc["durationMs"])
		}
	}
}

// --- 2. a restart runs nothing again ---

func TestMigrationsDoNotRunAgainWhenTheServerRestarts(t *testing.T) {
	t.Parallel()
	db, uri := migrationTestDB(t)

	migrationStartApp(t, uri, db)
	first := migrationLedger(t, db)
	time.Sleep(20 * time.Millisecond) // a rerun would record a later appliedAt
	migrationStartApp(t, uri, db)
	second := migrationLedger(t, db)

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ledger changed on restart:\nbefore: %v\nafter:  %v", first, second)
	}
}

func TestMigrationsAlreadyAppliedAreNotCalledAgain(t *testing.T) {
	t.Parallel()
	db, _ := migrationTestDB(t)
	r := &migrationRecorder{}
	sets := [][]mongodb.Migration{migrationSet(r, "catalog", 3)}

	for i := 0; i < 3; i++ {
		if err := mongodb.Migrate(migrationCtx(t), db, sets, migrationOpts("a")); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}

	for _, id := range []string{"catalog/0001", "catalog/0002", "catalog/0003"} {
		if n := r.count(id); n != 1 {
			t.Errorf("%s ran %d times, want 1", id, n)
		}
	}
}

// --- 3. only the new migration runs ---

func TestOnlyANewMigrationRunsWhenOneIsAdded(t *testing.T) {
	t.Parallel()
	db, _ := migrationTestDB(t)
	r := &migrationRecorder{}

	if err := mongodb.Migrate(migrationCtx(t), db, [][]mongodb.Migration{migrationSet(r, "catalog", 1)}, migrationOpts("a")); err != nil {
		t.Fatal(err)
	}
	before := migrationLedger(t, db)

	// The next release ships catalog/0002.
	if err := mongodb.Migrate(migrationCtx(t), db, [][]mongodb.Migration{migrationSet(r, "catalog", 2)}, migrationOpts("a")); err != nil {
		t.Fatal(err)
	}

	migrationRequireIDs(t, r.order(), []string{"catalog/0001", "catalog/0002"})
	migrationRequireIDs(t, migrationLedgerIDs(t, db), []string{"catalog/0001", "catalog/0002"})
	if after := migrationLedger(t, db); !reflect.DeepEqual(before[0], after[0]) {
		t.Errorf("catalog/0001 was rewritten:\nbefore: %v\nafter:  %v", before[0], after[0])
	}
}

// --- 4. a failing migration aborts startup, and a fix is picked up by the next start ---

func TestAFailingMigrationAbortsAndTheFixedOneIsAppliedOnTheNextStart(t *testing.T) {
	t.Parallel()
	db, _ := migrationTestDB(t)
	r := &migrationRecorder{}
	var fail atomic.Bool
	fail.Store(true)
	sets := [][]mongodb.Migration{{
		migrationStep(r, "catalog/0001", nil),
		migrationStep(r, "catalog/0002", &fail),
		migrationStep(r, "catalog/0003", nil),
	}}

	err := mongodb.Migrate(migrationCtx(t), db, sets, migrationOpts("a"))
	if err == nil {
		t.Fatal("Migrate succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "catalog/0002") {
		t.Errorf("error %q does not name catalog/0002", err)
	}
	migrationRequireIDs(t, migrationLedgerIDs(t, db), []string{"catalog/0001"})
	migrationRequireIDs(t, r.order(), []string{"catalog/0001", "catalog/0002"}) // 0003 never started

	// The failed run released the schema lock, so the restart doesn't wait for the lease.
	fail.Store(false)
	start := time.Now()
	if err := mongodb.Migrate(migrationCtx(t), db, sets, migrationOpts("b")); err != nil {
		t.Fatalf("restart after the fix: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("restart took %v, want it not to wait for the lock lease", elapsed)
	}
	migrationRequireIDs(t, migrationLedgerIDs(t, db), []string{"catalog/0001", "catalog/0002", "catalog/0003"})
	migrationRequireIDs(t, r.order(), []string{"catalog/0001", "catalog/0002", "catalog/0002", "catalog/0003"})
}

// --- 5. two instances starting at the same time ---

func TestTwoInstancesStartingTogetherApplyEveryMigrationOnce(t *testing.T) {
	t.Parallel()
	db, uri := migrationTestDB(t)

	var wg sync.WaitGroup
	start := make(chan struct{})
	apps := make([]*bootstrap.App, 2)
	errs := make([]error, 2)
	for i := range apps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			apps[i], errs[i] = migrationNewApp(migrationCtx(t), uri, db.Name())
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("instance %d: %v", i, err)
		}
	}
	migrationRequireIDs(t, migrationLedgerIDs(t, db), []string{"catalog/0001"})
	for _, app := range apps {
		migrationRequireHealthy(t, migrationServe(t, app))
	}
}

func TestTwoMigratorsRunningTogetherCallEveryUpStepOnce(t *testing.T) {
	t.Parallel()
	db, _ := migrationTestDB(t)
	r := &migrationRecorder{delay: 100 * time.Millisecond}
	sets := [][]mongodb.Migration{migrationSet(r, "catalog", 3)}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i, owner := range []string{"instance-a", "instance-b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = mongodb.Migrate(migrationCtx(t), db, sets, migrationOpts(owner))
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("instance %d: %v", i, err)
		}
	}
	for _, id := range []string{"catalog/0001", "catalog/0002", "catalog/0003"} {
		if n := r.count(id); n != 1 {
			t.Errorf("%s ran %d times, want exactly 1", id, n)
		}
	}
	migrationRequireIDs(t, migrationLedgerIDs(t, db), []string{"catalog/0001", "catalog/0002", "catalog/0003"})
}

// --- 6. a dead instance's lock is taken over after the lease expired ---

func TestAnInstanceTakesOverTheSchemaLockOfADeadInstance(t *testing.T) {
	t.Parallel()
	db, uri := migrationTestDB(t)
	migrationSeedLock(t, db, "dead-instance", time.Now().Add(-time.Hour))

	migrationRequireHealthy(t, migrationStartApp(t, uri, db))

	migrationRequireIDs(t, migrationLedgerIDs(t, db), []string{"catalog/0001"})
	var lock struct {
		Owner string `bson:"owner"`
	}
	err := db.Collection(mongodb.LockCollection).FindOne(migrationCtx(t), bson.D{{Key: "_id", Value: "schema"}}).Decode(&lock)
	if err == nil && lock.Owner == "dead-instance" {
		t.Error("the lock is still held by dead-instance")
	}
}

func TestAnInstanceWaitsForTheLeaseOfADeadInstanceToExpire(t *testing.T) {
	t.Parallel()
	db, uri := migrationTestDB(t)
	// Seed from the server's clock, which decides expiry; the test host's clock may differ.
	migrationSeedLock(t, db, "dead-instance", migrationServerTime(t, db).Add(2*time.Second))

	begin := time.Now()
	client := migrationStartApp(t, uri, db)
	elapsed := time.Since(begin)

	if elapsed < time.Second {
		t.Errorf("startup took %v, want it to wait for the lease (about 2 s)", elapsed)
	}
	migrationRequireIDs(t, migrationLedgerIDs(t, db), []string{"catalog/0001"})
	migrationRequireHealthy(t, client)
}

// --- 7. a database newer than the code is refused ---

func TestAStartIsRefusedWhenTheLedgerHoldsAnUnknownMigration(t *testing.T) {
	t.Parallel()
	db, _ := migrationTestDB(t)
	r := &migrationRecorder{}
	known := [][]mongodb.Migration{migrationSet(r, "catalog", 2)}
	if err := mongodb.Migrate(migrationCtx(t), db, known, migrationOpts("a")); err != nil {
		t.Fatal(err)
	}
	// A newer release applied catalog/0003.
	if _, err := db.Collection(mongodb.LedgerCollection).InsertOne(migrationCtx(t), bson.D{
		{Key: "_id", Value: "catalog/0003"},
		{Key: "description", Value: "from a newer release"},
		{Key: "appliedAt", Value: time.Now()},
		{Key: "durationMs", Value: int64(1)},
	}); err != nil {
		t.Fatal(err)
	}

	err := mongodb.Migrate(migrationCtx(t), db, known, migrationOpts("a"))

	if !errors.Is(err, mongodb.ErrUnknownMigration) {
		t.Fatalf("err = %v, want ErrUnknownMigration", err)
	}
	if !strings.Contains(err.Error(), "catalog/0003") {
		t.Errorf("error %q does not name catalog/0003", err)
	}
	if n := len(r.order()); n != 2 {
		t.Errorf("Up steps ran %d times in total, want 2 (none on the refused start)", n)
	}
}

func TestTheServerRefusesToStartOnADatabaseNewerThanItsCode(t *testing.T) {
	t.Parallel()
	db, uri := migrationTestDB(t)
	// The real code knows catalog/0001 only.
	if _, err := db.Collection(mongodb.LedgerCollection).InsertOne(migrationCtx(t), bson.D{
		{Key: "_id", Value: "catalog/0002"},
		{Key: "description", Value: "from a newer release"},
		{Key: "appliedAt", Value: time.Now()},
		{Key: "durationMs", Value: int64(1)},
	}); err != nil {
		t.Fatal(err)
	}

	app, err := migrationNewApp(migrationCtx(t), uri, db.Name())
	if err == nil {
		_ = app.Close(context.Background())
		t.Fatal("bootstrap.New succeeded, want an error")
	}

	if !errors.Is(err, mongodb.ErrUnknownMigration) {
		t.Errorf("err = %v, want ErrUnknownMigration", err)
	}
	if !strings.Contains(err.Error(), "catalog/0002") {
		t.Errorf("error %q does not name catalog/0002", err)
	}
	migrationRequireIDs(t, migrationLedgerIDs(t, db), []string{"catalog/0002"})
}

// --- 8. invalid migration sets are rejected before the database is touched ---

func TestAnInvalidMigrationSetIsRejectedBeforeTouchingTheDatabase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ids  []string
	}{
		{"duplicate sequence number", []string{"catalog/0001", "catalog/0001"}},
		{"duplicate after a valid prefix", []string{"catalog/0001", "catalog/0002", "catalog/0002"}},
		{"gap in the sequence", []string{"catalog/0001", "catalog/0003"}},
		{"sequence does not start at 0001", []string{"catalog/0002"}},
		{"out of order", []string{"catalog/0002", "catalog/0001"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db, _ := migrationTestDB(t)
			r := &migrationRecorder{}
			set := make([]mongodb.Migration, len(tt.ids))
			for i, id := range tt.ids {
				set[i] = migrationStep(r, id, nil)
			}

			err := mongodb.Migrate(migrationCtx(t), db, [][]mongodb.Migration{set}, migrationOpts("a"))

			if !errors.Is(err, mongodb.ErrInvalidMigrationSet) {
				t.Fatalf("err = %v, want ErrInvalidMigrationSet", err)
			}
			if calls := r.order(); len(calls) != 0 {
				t.Errorf("Up steps ran: %v", calls)
			}
			if names := migrationCollectionNames(t, db); len(names) != 0 {
				t.Errorf("database has collections %v, want none", names)
			}
		})
	}
}

// --- 9. the validator and index helpers are idempotent ---

// migrationCollectionOptions returns the options listCollections reports for a collection.
func migrationCollectionOptions(t *testing.T, db *mongo.Database, name string) bson.M {
	t.Helper()
	ctx := migrationCtx(t)
	cur, err := db.ListCollections(ctx, bson.D{{Key: "name", Value: name}})
	if err != nil {
		t.Fatal(err)
	}
	var specs []struct {
		Options bson.M `bson:"options"`
	}
	if err := cur.All(ctx, &specs); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("listCollections(%s) returned %d collections, want 1", name, len(specs))
	}
	return specs[0].Options
}

func migrationIndexes(t *testing.T, db *mongo.Database, collection string) []bson.M {
	t.Helper()
	ctx := migrationCtx(t)
	cur, err := db.Collection(collection).Indexes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var indexes []bson.M
	if err := cur.All(ctx, &indexes); err != nil {
		t.Fatal(err)
	}
	return indexes
}

func migrationNameSchema(maxLength int) bson.D {
	return bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: bson.A{"name"}},
		{Key: "properties", Value: bson.D{
			{Key: "name", Value: bson.D{
				{Key: "bsonType", Value: "string"},
				{Key: "maxLength", Value: maxLength},
			}},
		}},
	}
}

func migrationIndexModels() []mongo.IndexModel {
	return []mongo.IndexModel{
		{Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetName("name_unique").SetUnique(true)},
		{Keys: bson.D{{Key: "tags", Value: 1}}, Options: options.Index().SetName("tags_lookup")},
	}
}

func TestRunningTheSchemaHelpersAgainWithTheSameDefinitionChangesNothing(t *testing.T) {
	t.Parallel()
	db, _ := migrationTestDB(t)
	ctx := migrationCtx(t)
	const coll = "scratch"

	if err := mongodb.EnsureCollection(ctx, db, coll, migrationNameSchema(10)); err != nil {
		t.Fatal(err)
	}
	if err := mongodb.EnsureIndexes(ctx, db, coll, migrationIndexModels()...); err != nil {
		t.Fatal(err)
	}
	optionsBefore := migrationCollectionOptions(t, db, coll)
	indexesBefore := migrationIndexes(t, db, coll)

	if err := mongodb.EnsureCollection(ctx, db, coll, migrationNameSchema(10)); err != nil {
		t.Fatalf("EnsureCollection, second run: %v", err)
	}
	if err := mongodb.EnsureIndexes(ctx, db, coll, migrationIndexModels()...); err != nil {
		t.Fatalf("EnsureIndexes, second run: %v", err)
	}

	if after := migrationCollectionOptions(t, db, coll); !reflect.DeepEqual(optionsBefore, after) {
		t.Errorf("collection options changed:\nbefore: %v\nafter:  %v", optionsBefore, after)
	}
	if after := migrationIndexes(t, db, coll); !reflect.DeepEqual(indexesBefore, after) {
		t.Errorf("indexes changed:\nbefore: %v\nafter:  %v", indexesBefore, after)
	}
	if len(indexesBefore) != 3 { // _id_ plus the two above
		t.Errorf("got %d indexes, want 3: %v", len(indexesBefore), indexesBefore)
	}
}

func TestAChangedValidatorIsAppliedToAnExistingCollection(t *testing.T) {
	t.Parallel()
	db, _ := migrationTestDB(t)
	ctx := migrationCtx(t)
	const coll = "scratch"
	c := db.Collection(coll)

	if err := mongodb.EnsureCollection(ctx, db, coll, migrationNameSchema(10)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InsertOne(ctx, bson.D{{Key: "_id", Value: 1}, {Key: "name", Value: "abcd"}}); err != nil {
		t.Fatalf("insert under the old validator (maxLength 10): %v", err)
	}
	optionsBefore := migrationCollectionOptions(t, db, coll)

	if err := mongodb.EnsureCollection(ctx, db, coll, migrationNameSchema(3)); err != nil {
		t.Fatalf("EnsureCollection with a changed validator: %v", err)
	}

	if after := migrationCollectionOptions(t, db, coll); reflect.DeepEqual(optionsBefore, after) {
		t.Error("collection options are unchanged, want the new validator")
	}
	_, err := c.InsertOne(ctx, bson.D{{Key: "_id", Value: 2}, {Key: "name", Value: "abcd"}})
	if se := mongo.ServerError(nil); !errors.As(err, &se) || !se.HasErrorCode(121) { // DocumentValidationFailure
		t.Errorf("insert of a 4-character name under the new validator (maxLength 3): err = %v, want a validation failure", err)
	}
	if _, err := c.InsertOne(ctx, bson.D{{Key: "_id", Value: 3}, {Key: "name", Value: "abc"}}); err != nil {
		t.Errorf("insert of a 3-character name under the new validator: %v", err)
	}
}

// --- 10. logging, and health keeps working ---

// migrationSyncBuffer is a bytes.Buffer that is safe for concurrent writes.
type migrationSyncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *migrationSyncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *migrationSyncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// No t.Parallel: the test replaces the process-wide default logger.
func TestEveryAppliedMigrationIsLoggedAndHealthKeepsWorking(t *testing.T) {
	db, uri := migrationTestDB(t)
	var logs migrationSyncBuffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	client := migrationStartApp(t, uri, db)

	var applied []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // not a JSON log line
		}
		if rec["msg"] == "schema migration applied" {
			applied = append(applied, rec)
		}
	}
	if len(applied) != 1 {
		t.Fatalf("got %d 'schema migration applied' records, want 1; logs:\n%s", len(applied), logs.String())
	}
	rec := applied[0]
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	if rec["id"] != "catalog/0001" {
		t.Errorf("id = %v, want catalog/0001", rec["id"])
	}
	if desc, ok := rec["description"].(string); !ok || desc == "" {
		t.Errorf("description = %#v, want a non-empty string", rec["description"])
	}
	if ms, ok := rec["durationMs"].(float64); !ok || ms < 0 {
		t.Errorf("durationMs = %#v, want a number >= 0", rec["durationMs"])
	}

	migrationRequireHealthy(t, client)
}
