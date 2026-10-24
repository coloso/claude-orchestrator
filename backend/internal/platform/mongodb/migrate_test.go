package mongodb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// testSet builds a "catalog" set of n migrations whose Up steps count their calls in calls[i].
// Hooks, if given, run inside Up i (0-based) and may fail it.
type testSet struct {
	calls []*atomic.Int32
	hooks map[int]func(ctx context.Context) error
}

func newTestSet(n int) *testSet {
	s := &testSet{hooks: map[int]func(ctx context.Context) error{}}
	for range n {
		s.calls = append(s.calls, new(atomic.Int32))
	}
	return s
}

func (s *testSet) migrations() []Migration {
	ms := make([]Migration, len(s.calls))
	for i := range s.calls {
		ms[i] = Migration{
			ID:          fmt.Sprintf("catalog/%04d", i+1),
			Description: fmt.Sprintf("catalog change %d", i+1),
			Up: func(ctx context.Context, _ *mongo.Database) error {
				s.calls[i].Add(1)
				if hook := s.hooks[i]; hook != nil {
					return hook(ctx)
				}
				return nil
			},
		}
	}
	return ms
}

func (s *testSet) callCounts() []int32 {
	counts := make([]int32, len(s.calls))
	for i, c := range s.calls {
		counts[i] = c.Load()
	}
	return counts
}

func assertCalls(t *testing.T, s *testSet, want ...int32) {
	t.Helper()
	got := s.callCounts()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Up calls = %v, want %v", got, want)
	}
}

func fastOpts(owner string) MigrateOptions {
	return MigrateOptions{Lease: time.Second, PollInterval: 20 * time.Millisecond, Owner: owner}
}

func quietLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func lockDocCount(t *testing.T, db *mongo.Database) int64 {
	t.Helper()
	n, err := db.Collection(LockCollection).CountDocuments(context.Background(), bson.D{})
	if err != nil {
		t.Fatalf("count lock documents: %v", err)
	}
	return n
}

func ledgerIDs(t *testing.T, db *mongo.Database) []string {
	t.Helper()
	ids, err := appliedMigrationIDs(context.Background(), db)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	return ids
}

// --- unit tests (no MongoDB) ---

func TestMigrateRejectsInvalidSetsBeforeTouchingTheDatabase(t *testing.T) {
	ok := func(id string) Migration {
		return Migration{ID: id, Description: "d", Up: func(context.Context, *mongo.Database) error { return nil }}
	}
	tests := []struct {
		name string
		set  []Migration
	}{
		{"duplicate", []Migration{ok("catalog/0001"), ok("catalog/0001")}},
		{"gap", []Migration{ok("catalog/0001"), ok("catalog/0003")}},
		{"out of order", []Migration{ok("catalog/0002"), ok("catalog/0001")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Migrate(context.Background(), nil, [][]Migration{tt.set}, MigrateOptions{})
			if !errors.Is(err, ErrInvalidMigrationSet) {
				t.Fatalf("err = %v, want ErrInvalidMigrationSet", err)
			}
		})
	}
}

func TestMigrateRejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		name string
		opts MigrateOptions
	}{
		{"lease above max", MigrateOptions{Lease: MaxLease + time.Second}},
		{"negative lease", MigrateOptions{Lease: -time.Second}},
		{"negative poll interval", MigrateOptions{PollInterval: -time.Millisecond}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Migrate(context.Background(), nil, nil, tt.opts); err == nil {
				t.Fatal("err = nil, want an error")
			}
		})
	}
}

func TestMigrateWithoutDatabaseReturnsErrorInsteadOfPanicking(t *testing.T) {
	if err := Migrate(context.Background(), nil, nil, MigrateOptions{}); err == nil {
		t.Fatal("err = nil, want an error")
	}
}

func TestMigrateOptionsWithDefaults(t *testing.T) {
	got, err := MigrateOptions{}.withDefaults()
	if err != nil {
		t.Fatalf("withDefaults: %v", err)
	}
	if got.Lease != DefaultLease || got.PollInterval != DefaultPollInterval || got.Logger == nil {
		t.Errorf("defaults = %+v", got)
	}
	other, err := MigrateOptions{}.withDefaults()
	if err != nil {
		t.Fatalf("withDefaults: %v", err)
	}
	if got.Owner == "" || got.Owner == other.Owner {
		t.Errorf("owners %q and %q must be non-empty and differ", got.Owner, other.Owner)
	}
	if kept, _ := (MigrateOptions{Owner: "me", Lease: MaxLease}).withDefaults(); kept.Owner != "me" || kept.Lease != MaxLease {
		t.Errorf("explicit options not kept: %+v", kept)
	}
}

// --- integration tests (need MONGO_TEST_URI) ---

func TestMigrateAppliesAllMigrationsInOrderAndRecordsThem(t *testing.T) {
	db := newTestDB(t)
	s := newTestSet(3)
	var order []string
	var mu sync.Mutex
	for i := range s.calls {
		s.hooks[i] = func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, fmt.Sprintf("catalog/%04d", i+1))
			return nil
		}
	}
	before := time.Now().Add(-time.Second)

	opts := fastOpts("a")
	opts.Logger = quietLogger()
	if err := Migrate(context.Background(), db, [][]Migration{s.migrations()}, opts); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if want := "[catalog/0001 catalog/0002 catalog/0003]"; fmt.Sprint(order) != want {
		t.Errorf("order = %v, want %s", order, want)
	}
	cur, err := db.Collection(LedgerCollection).Find(context.Background(), bson.D{})
	if err != nil {
		t.Fatalf("find ledger: %v", err)
	}
	var entries []ledgerEntry
	if err := cur.All(context.Background(), &entries); err != nil {
		t.Fatalf("decode ledger: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("ledger has %d entries, want 3", len(entries))
	}
	for _, e := range entries {
		if e.AppliedAt.Before(before) || e.AppliedAt.After(time.Now().Add(time.Second)) {
			t.Errorf("%s appliedAt = %v, want about now", e.ID, e.AppliedAt)
		}
		if e.DurationMs < 0 {
			t.Errorf("%s durationMs = %d", e.ID, e.DurationMs)
		}
		if e.Description == "" {
			t.Errorf("%s has no description", e.ID)
		}
	}
	var raw bson.M
	if err := db.Collection(LedgerCollection).FindOne(context.Background(), bson.D{{Key: "_id", Value: "catalog/0001"}}).Decode(&raw); err != nil {
		t.Fatalf("find catalog/0001: %v", err)
	}
	for _, field := range []string{"appliedAt", "durationMs"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("ledger entry has no %s field: %v", field, raw)
		}
	}
}

func TestMigrateRunsNothingWhenEverythingIsApplied(t *testing.T) {
	db := newTestDB(t)
	s := newTestSet(2)
	sets := [][]Migration{s.migrations()}
	for range 2 {
		if err := Migrate(context.Background(), db, sets, fastOpts("a")); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
	}
	assertCalls(t, s, 1, 1)
}

func TestMigrateRunsOnlyTheNewMigration(t *testing.T) {
	db := newTestDB(t)
	s := newTestSet(1)
	if err := Migrate(context.Background(), db, [][]Migration{s.migrations()}, fastOpts("a")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	s2 := newTestSet(2)
	s2.calls[0] = s.calls[0]
	if err := Migrate(context.Background(), db, [][]Migration{s2.migrations()}, fastOpts("a")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	assertCalls(t, s2, 1, 1)
}

func TestMigrateFailureNamesMigrationAndKeepsEarlierOnes(t *testing.T) {
	db := newTestDB(t)
	s := newTestSet(2)
	s.hooks[1] = func(context.Context) error { return errors.New("boom") }

	err := Migrate(context.Background(), db, [][]Migration{s.migrations()}, fastOpts("a"))
	if err == nil || !strings.Contains(err.Error(), "catalog/0002") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want one naming catalog/0002 and wrapping boom", err)
	}
	if got := fmt.Sprint(ledgerIDs(t, db)); got != "[catalog/0001]" {
		t.Errorf("ledger = %s, want [catalog/0001]", got)
	}
	if n := lockDocCount(t, db); n != 0 {
		t.Errorf("%d lock documents after failure, want 0", n)
	}

	delete(s.hooks, 1)
	if err := Migrate(context.Background(), db, [][]Migration{s.migrations()}, fastOpts("a")); err != nil {
		t.Fatalf("Migrate after fix: %v", err)
	}
	assertCalls(t, s, 1, 2)
	if got := fmt.Sprint(ledgerIDs(t, db)); got != "[catalog/0001 catalog/0002]" {
		t.Errorf("ledger = %s", got)
	}
}

func TestMigrateTwoInstancesApplyEachMigrationOnce(t *testing.T) {
	db := newTestDB(t)
	s := newTestSet(3)
	for i := range s.calls {
		s.hooks[i] = func(context.Context) error { time.Sleep(50 * time.Millisecond); return nil }
	}
	sets := [][]Migration{s.migrations()}

	errs := make(chan error, 2)
	for _, owner := range []string{"a", "b"} {
		go func() { errs <- Migrate(context.Background(), db, sets, fastOpts(owner)) }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("Migrate: %v", err)
		}
	}
	assertCalls(t, s, 1, 1, 1)
	if n := lockDocCount(t, db); n != 0 {
		t.Errorf("%d lock documents left, want 0", n)
	}
}

func TestMigrateWaitsForLockOfDeadInstance(t *testing.T) {
	db := newTestDB(t)
	_, err := db.Collection(LockCollection).InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: "schema"},
		{Key: "owner", Value: "dead"},
		{Key: "expiresAt", Value: time.Now().Add(time.Second)},
	})
	if err != nil {
		t.Fatalf("insert stale lock: %v", err)
	}
	s := newTestSet(1)

	start := time.Now()
	if err := Migrate(context.Background(), db, [][]Migration{s.migrations()}, fastOpts("a")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if waited := time.Since(start); waited < 500*time.Millisecond {
		t.Errorf("Migrate returned after %v, want it to wait for the lease of the dead instance", waited)
	}
	assertCalls(t, s, 1)
	if n := lockDocCount(t, db); n != 0 {
		t.Errorf("%d lock documents left, want 0", n)
	}
}

func TestMigrateRenewsLockDuringSlowMigration(t *testing.T) {
	db := newTestDB(t)
	s := newTestSet(2)
	var inFlight, maxInFlight atomic.Int32
	for i := range s.calls {
		s.hooks[i] = func(context.Context) error {
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				m := maxInFlight.Load()
				if n <= m || maxInFlight.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(1250 * time.Millisecond) // longer than the 1 s lease, in total 2.5 s
			return nil
		}
	}
	sets := [][]Migration{s.migrations()}

	errs := make(chan error, 2)
	go func() { errs <- Migrate(context.Background(), db, sets, fastOpts("a")) }()
	time.Sleep(300 * time.Millisecond)
	go func() { errs <- Migrate(context.Background(), db, sets, fastOpts("b")) }()
	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("Migrate: %v", err)
		}
	}
	assertCalls(t, s, 1, 1)
	if m := maxInFlight.Load(); m != 1 {
		t.Errorf("up to %d Up steps in flight at once, want 1", m)
	}
}

func TestMigrateStopsWhenLockIsLost(t *testing.T) {
	db := newTestDB(t)
	s := newTestSet(2)
	s.hooks[0] = func(ctx context.Context) error {
		// Another instance takes over the lock.
		_, err := db.Collection(LockCollection).UpdateOne(ctx,
			bson.D{{Key: "_id", Value: "schema"}},
			bson.D{{Key: "$set", Value: bson.D{{Key: "owner", Value: "thief"}}}})
		if err != nil {
			return err
		}
		<-time.After(500 * time.Millisecond) // let the renewal notice
		return nil
	}

	err := Migrate(context.Background(), db, [][]Migration{s.migrations()}, fastOpts("a"))
	if !errors.Is(err, ErrSchemaLockLost) {
		t.Fatalf("err = %v, want ErrSchemaLockLost", err)
	}
	if !strings.Contains(err.Error(), "catalog/0002") && !strings.Contains(err.Error(), "catalog/0001") {
		t.Errorf("err = %v, want it to name a migration", err)
	}
	assertCalls(t, s, 1, 0)
}

func TestMigrateRefusesDatabaseNewerThanBinary(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.Collection(LedgerCollection).InsertOne(context.Background(), ledgerEntry{
		ID: "catalog/0003", Description: "from the future", AppliedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("insert ledger entry: %v", err)
	}
	s := newTestSet(2)

	err := Migrate(context.Background(), db, [][]Migration{s.migrations()}, fastOpts("a"))
	if !errors.Is(err, ErrUnknownMigration) {
		t.Fatalf("err = %v, want ErrUnknownMigration", err)
	}
	if !strings.Contains(err.Error(), "catalog/0003") {
		t.Errorf("err = %v, want it to name catalog/0003", err)
	}
	assertCalls(t, s, 0, 0)
	if n := lockDocCount(t, db); n != 0 {
		t.Errorf("%d lock documents, want 0", n)
	}
}

func TestMigrateLeavesNoLockAfterSuccess(t *testing.T) {
	db := newTestDB(t)
	s := newTestSet(1)
	if err := Migrate(context.Background(), db, [][]Migration{s.migrations()}, fastOpts("a")); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if n := lockDocCount(t, db); n != 0 {
		t.Errorf("%d lock documents, want 0", n)
	}
}

func TestMigrateReleasesLockWhenContextIsCancelled(t *testing.T) {
	db := newTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := newTestSet(1)
	s.hooks[0] = func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}

	err := Migrate(ctx, db, [][]Migration{s.migrations()}, fastOpts("a"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := lockDocCount(t, db); n != 0 {
		t.Errorf("%d lock documents, want 0", n)
	}
}

func TestMigrateLogsEachAppliedMigration(t *testing.T) {
	db := newTestDB(t)
	s := newTestSet(2)
	var buf bytes.Buffer
	opts := fastOpts("a")
	opts.Logger = slog.New(slog.NewJSONHandler(&buf, nil))

	if err := Migrate(context.Background(), db, [][]Migration{s.migrations()}, opts); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d log lines, want 2:\n%s", len(lines), buf.String())
	}
	for i, line := range lines {
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if rec["level"] != "INFO" || rec["msg"] != "schema migration applied" {
			t.Errorf("line %d: level/msg = %v/%v", i, rec["level"], rec["msg"])
		}
		if want := fmt.Sprintf("catalog/%04d", i+1); rec["id"] != want {
			t.Errorf("line %d: id = %v, want %s", i, rec["id"], want)
		}
		if want := fmt.Sprintf("catalog change %d", i+1); rec["description"] != want {
			t.Errorf("line %d: description = %v, want %s", i, rec["description"], want)
		}
		if _, ok := rec["durationMs"].(float64); !ok {
			t.Errorf("line %d: durationMs = %v, want a number", i, rec["durationMs"])
		}
	}
}
