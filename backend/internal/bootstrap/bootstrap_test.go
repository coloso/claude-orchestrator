package bootstrap

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestNewWithMemoryStorageMakesNoMongoConnection(t *testing.T) {
	// 127.0.0.1:1 refuses connections. A connection attempt would wait for the
	// 5 s server-selection timeout, so finishing within 1 s proves none was made.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	app, err := New(ctx, Config{Storage: StorageMemory, MongoURI: "mongodb://127.0.0.1:1", MongoDB: "unused"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("New with memory storage: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if elapsed >= time.Second {
		t.Fatalf("New with memory storage took %v; it must not connect to MongoDB", elapsed)
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

func newMemoryApp(t *testing.T, cfg Config) *App {
	t.Helper()
	cfg.Storage = StorageMemory
	app, err := New(context.Background(), cfg)
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
	app := newMemoryApp(t, Config{Logger: logger})

	if total := listTotal(t, app); total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
	if logs.Len() != 0 {
		t.Fatalf("unexpected logs without SeedDemoData: %s", logs.String())
	}
}

func TestNewSeedsTheDemoCatalog(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	app := newMemoryApp(t, Config{SeedDemoData: true, Logger: logger})

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

func TestNewWithMongoStorageMigrates(t *testing.T) {
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Skip("MONGO_TEST_URI is not set; skipping MongoDB integration test")
	}

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("random database name: %v", err)
	}
	dbName := "test_" + hex.EncodeToString(suffix[:])

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		client, err := mongodb.Connect(ctx, uri)
		if err != nil {
			t.Errorf("connect to drop %s: %v", dbName, err)
			return
		}
		defer func() { _ = client.Disconnect(ctx) }()
		if err := client.Database(dbName).Drop(ctx); err != nil {
			t.Errorf("drop test database %s: %v", dbName, err)
		}
	})

	app, err := New(ctx, Config{Storage: StorageMongo, MongoURI: uri, MongoDB: dbName})
	if err != nil {
		t.Fatalf("New with mongo storage: %v", err)
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
