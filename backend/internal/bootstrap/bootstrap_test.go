package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"orch-test/backend/internal/platform/mongodb"
)

func TestMigrationSetsAreValid(t *testing.T) {
	if _, err := mongodb.Plan(migrationSets()...); err != nil {
		t.Fatalf("migrationSets() is not a valid migration plan: %v", err)
	}
}

func TestNewFailsWhenMongoIsUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	app, err := New(ctx, Config{MongoURI: "mongodb://alice:s3cret@127.0.0.1:1/?authSource=admin", MongoDB: "orch"})
	if err == nil {
		_ = app.Close(context.Background())
		t.Fatal("New succeeded against an unreachable MongoDB")
	}
	if app != nil {
		t.Fatalf("New returned an app along with the error: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("New did not fail within 10 s: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "mongodb://127.0.0.1:1/") {
		t.Errorf("error does not name the URI: %v", err)
	}
	for _, secret := range []string{"alice", "s3cret"} {
		if strings.Contains(msg, secret) {
			t.Errorf("error leaks %q: %v", secret, err)
		}
	}
}

func TestNewRequiresMongoURIAndDatabase(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"empty URI", Config{MongoDB: "orch"}},
		{"empty database", Config{MongoURI: "mongodb://localhost:27017"}},
		{"both empty", Config{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, err := New(context.Background(), tt.cfg)
			if err == nil {
				_ = app.Close(context.Background())
				t.Fatal("New succeeded without MongoURI or MongoDB")
			}
			if app != nil {
				t.Fatal("New returned an app along with the error")
			}
		})
	}
}

func TestRedactMongoURI(t *testing.T) {
	const unparsable = "(unparsable MongoDB URI)"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "mongodb://localhost:27017", "mongodb://localhost:27017"},
		{"with path", "mongodb://localhost:27017/orch", "mongodb://localhost:27017/orch"},
		{"credentials dropped", "mongodb://alice:s3cret@db.example:27017/admin?authSource=admin", "mongodb://db.example:27017/admin"},
		{"user without password", "mongodb://alice@db.example:27017", "mongodb://db.example:27017"},
		{"several hosts", "mongodb://u:p@a:1,b:2/db?replicaSet=rs", "mongodb://a:1,b:2/db"},
		{"srv", "mongodb+srv://u:p@cluster0.example.net/db?retryWrites=true", "mongodb+srv://cluster0.example.net/db"},
		{"fragment dropped", "mongodb://u:p@h:1/db#frag", "mongodb://h:1/db"},
		{"bad port", "mongodb://u:s3cret@h:bad/", unparsable},
		{"control character", "mongodb://u:s3cret@h\x7f:1/", unparsable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactMongoURI(tt.in)
			if got != tt.want {
				t.Fatalf("redactMongoURI(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, "s3cret") {
				t.Fatalf("leaks the password: %q", got)
			}
		})
	}
}

func TestConfigFromEnvSeedDemoData(t *testing.T) {
	tests := []struct {
		name  string
		value *string // nil = unset
		want  bool
	}{
		{"unset", nil, false},
		{"true", ptr("true"), true},
		{"false", ptr("false"), false},
		{"yes does not parse", ptr("yes"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SEED_DEMO_DATA", "")
			if tt.value != nil {
				t.Setenv("SEED_DEMO_DATA", *tt.value)
			}
			if got := ConfigFromEnv().SeedDemoData; got != tt.want {
				t.Fatalf("SeedDemoData = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigFromEnvMongoSettings(t *testing.T) {
	tests := []struct {
		name    string
		uri, db string
		wantURI string
		wantDB  string
	}{
		{"empty gives the defaults", "", "", "mongodb://localhost:27017", "orch"},
		{"set values are used as given", "mongodb://db.example:27018", "books", "mongodb://db.example:27018", "books"},
		{"only the database set", "", "books", "mongodb://localhost:27017", "books"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MONGO_URI", tt.uri)
			t.Setenv("MONGO_DB", tt.db)
			cfg := ConfigFromEnv()
			if cfg.MongoURI != tt.wantURI || cfg.MongoDB != tt.wantDB {
				t.Fatalf("MongoURI, MongoDB = %q, %q; want %q, %q", cfg.MongoURI, cfg.MongoDB, tt.wantURI, tt.wantDB)
			}
		})
	}
}

func TestConfigFromEnvIgnoresStorage(t *testing.T) {
	t.Setenv("MONGO_URI", "mongodb://db.example:27018")
	t.Setenv("MONGO_DB", "books")
	t.Setenv("SEED_DEMO_DATA", "true")
	t.Setenv("STORAGE", "")
	want := ConfigFromEnv()

	t.Setenv("STORAGE", "memory")
	if got := ConfigFromEnv(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfigFromEnv with STORAGE=memory = %+v, want %+v", got, want)
	}
}

func ptr[T any](v T) *T { return &v }

// listTotal calls GET /api/books on the app and returns the list's total.
func listTotal(t *testing.T, app *App) int64 {
	t.Helper()
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/books", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/books = %d: %s", rec.Code, rec.Body)
	}
	var list struct {
		Items []json.RawMessage `json:"items"`
		Total int64             `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if list.Items == nil {
		t.Fatalf("items is not an array: %s", rec.Body)
	}
	return list.Total
}

// newApp builds an app on a fresh test database and closes it when the test ends.
func newApp(t *testing.T, cfg Config) *App {
	t.Helper()
	cfg.MongoURI, cfg.MongoDB = newTestDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	app, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return app
}

func TestNewWithoutSeedingLeavesTheCatalogEmpty(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app := newApp(t, Config{Logger: logger})

	if total := listTotal(t, app); total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
	// The migrations log on MongoDB, so only the seeding lines must be absent.
	if strings.Contains(logs.String(), "seeded") {
		t.Fatalf("unexpected seeding logs without SeedDemoData: %s", logs.String())
	}
}

func TestNewSeedsTheDemoCatalog(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app := newApp(t, Config{SeedDemoData: true, Logger: logger})

	if total := listTotal(t, app); total != 12 {
		t.Fatalf("total = %d, want 12", total)
	}
	out := logs.String()
	if !strings.Contains(out, "seeded 12 demo books, skipped 0") {
		t.Fatalf("log lacks the summary: %s", out)
	}
	if n := strings.Count(out, "seeded demo book"); n != 12 {
		t.Fatalf("%d \"seeded demo book\" lines, want 12: %s", n, out)
	}
}

func TestNewMigrates(t *testing.T) {
	uri, dbName := newTestDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	app, err := New(ctx, Config{MongoURI: uri, MongoDB: dbName})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	client, err := mongodb.Connect(ctx, uri)
	if err != nil {
		t.Fatalf("connect to check migrations: %v", err)
	}
	defer func() { _ = client.Disconnect(ctx) }()

	n, err := client.Database(dbName).Collection(mongodb.LedgerCollection).CountDocuments(ctx, bson.D{{Key: "_id", Value: "catalog/0001"}})
	if err != nil {
		t.Fatalf("read %s: %v", mongodb.LedgerCollection, err)
	}
	if n != 1 {
		t.Fatalf("%s has %d catalog/0001 entries after New, want 1", mongodb.LedgerCollection, n)
	}
}
