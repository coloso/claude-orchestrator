// Package e2e_test drives the fully wired application through the ogen-generated client,
// checking the implementation against api/openapi.yaml from the outside.
package e2e_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"orch-test/backend/internal/bootstrap"
	"orch-test/backend/internal/middleware"
	"orch-test/backend/internal/oas"
	"orch-test/backend/internal/platform/mongodb"
)

// generousLimits is for functional tests: every request comes from 127.0.0.1, so the
// production limits would throttle tests that make many calls. The rate limit tests
// use the defaults instead.
func generousLimits() middleware.RateLimitConfig {
	cfg := middleware.DefaultRateLimitConfig()
	cfg.Read = middleware.Limit{PerSecond: 1e6, Burst: 1e6}
	cfg.Write = middleware.Limit{PerSecond: 1e6, Burst: 1e6}
	return cfg
}

// e2eMongoURI returns the MongoDB server the e2e tests run against. The tests never skip
// without it, so a missing setting cannot pass for a green run.
func e2eMongoURI(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Fatal("MONGO_TEST_URI is not set: the e2e tests run against MongoDB. Run `make mongo`, then `export MONGO_TEST_URI=mongodb://localhost:27017`")
	}
	return uri
}

// e2eConfig is the bootstrap configuration of every e2e app: MongoDB storage in database
// dbName and generous rate limits. It is the only place in this package that names the
// storage. S03-08 removes the Storage field once bootstrap has no storage setting.
func e2eConfig(uri, dbName string) bootstrap.Config {
	return bootstrap.Config{Storage: bootstrap.StorageMongo, MongoURI: uri, MongoDB: dbName, RateLimit: generousLimits()}
}

// e2eSharedClient connects once, on first use, and is shared by all tests. No test
// disconnects it; it lives until the test binary exits.
var e2eSharedClient = sync.OnceValues(func() (*mongo.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return mongodb.Connect(ctx, os.Getenv("MONGO_TEST_URI"))
})

// e2eTestDB returns the name and handle of a fresh test database and drops it when the
// test ends. MongoDB creates the database on the first write, so nothing is created here.
func e2eTestDB(t *testing.T) (name string, db *mongo.Database) {
	t.Helper()
	e2eMongoURI(t)
	client, err := e2eSharedClient()
	if err != nil {
		t.Fatalf("connect to MONGO_TEST_URI: %v", err)
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name = "test_" + hex.EncodeToString(suffix[:])
	db = client.Database(name)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := db.Drop(ctx); err != nil {
			t.Errorf("drop test database %s: %v", name, err)
		}
	})
	return name, db
}

// e2eMongoConfig returns the configuration for an app on a fresh test database, plus the
// database handle.
func e2eMongoConfig(t *testing.T) (bootstrap.Config, *mongo.Database) {
	t.Helper()
	name, db := e2eTestDB(t)
	return e2eConfig(e2eMongoURI(t), name), db
}

// e2eEnv is a running, fully wired application and what tests need to reach it.
type e2eEnv struct {
	URL    string
	Client *oas.Client
	App    *bootstrap.App
	DB     *mongo.Database
	Config bootstrap.Config
}

// e2eServe starts the app with cfg exactly as given, so a zero RateLimit means the
// production default limits. It closes the server, then the app, when the test ends.
func e2eServe(t *testing.T, cfg bootstrap.Config, db *mongo.Database) e2eEnv {
	t.Helper()
	app, err := bootstrap.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("bootstrap.New: %v", err)
	}
	srv := httptest.NewServer(app.Handler)
	t.Cleanup(func() {
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = app.Close(ctx)
	})
	client, err := oas.NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return e2eEnv{URL: srv.URL, Client: client, App: app, DB: db, Config: cfg}
}

// e2eStart serves a fresh app on its own test database with generous rate limits.
func e2eStart(t *testing.T) e2eEnv {
	t.Helper()
	cfg, db := e2eMongoConfig(t)
	return e2eServe(t, cfg, db)
}

// newClient starts a fresh, fully wired server and returns a generated client for it.
func newClient(t *testing.T) *oas.Client {
	t.Helper()
	return e2eStart(t).Client
}

func TestHealth(t *testing.T) {
	res, err := newClient(t).GetHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q, want ok", res.Status)
	}
}
