package e2e_test

// End-to-end tests of MongoDB-only startup: the defaults, the failure when MongoDB is
// unreachable (in process and as the real server binary), and that a leftover STORAGE=memory
// is ignored. Every top-level identifier is prefixed with "startup".
//
// Tests that use t.Setenv are not parallel. The unreachable-MongoDB tests connect to port 1 of
// 127.0.0.1, which refuses connections, so they need no MongoDB server and no docker.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"orch-test/backend/internal/bootstrap"
	"orch-test/backend/internal/oas"
	"orch-test/backend/internal/platform/mongodb"
)

const (
	startupUser     = "e2e-user"
	startupPassword = "e2e-s3cret"
	// startupUnreachableURI points at a port that refuses connections. It carries credentials
	// and a query, which must never show up in an error or a log.
	startupUnreachableURI = "mongodb://" + startupUser + ":" + startupPassword + "@127.0.0.1:1/?authSource=admin"
	startupUnreachable    = "127.0.0.1:1"
)

// startupRequireRedacted fails unless text names the unreachable server and leaks no credentials.
func startupRequireRedacted(t *testing.T, what, text string) {
	t.Helper()
	if !strings.Contains(text, startupUnreachable) {
		t.Errorf("%s does not name %s:\n%s", what, startupUnreachable, text)
	}
	for _, secret := range []string{startupUser, startupPassword} {
		if strings.Contains(text, secret) {
			t.Errorf("%s leaks the credential %q:\n%s", what, secret, text)
		}
	}
}

func TestStartupDefaultsToLocalMongoDBAndDatabaseOrch(t *testing.T) {
	// The defaults are only read, never connected to: the test server may not be on localhost,
	// and `orch` is a developer's database.
	tests := []struct {
		name       string
		uri, db    string
		wantURI    string
		wantDBName string
	}{
		{"both unset", "", "", "mongodb://localhost:27017", "orch"},
		{"only the URI set", "mongodb://db.example:27017", "", "mongodb://db.example:27017", "orch"},
		{"only the database set", "", "library", "mongodb://localhost:27017", "library"},
		{"both set", "mongodb://db.example:27017", "library", "mongodb://db.example:27017", "library"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MONGO_URI", tt.uri)
			t.Setenv("MONGO_DB", tt.db)
			cfg := bootstrap.ConfigFromEnv()
			if cfg.MongoURI != tt.wantURI {
				t.Errorf("MongoURI = %q, want %q", cfg.MongoURI, tt.wantURI)
			}
			if cfg.MongoDB != tt.wantDBName {
				t.Errorf("MongoDB = %q, want %q", cfg.MongoDB, tt.wantDBName)
			}
		})
	}
}

func TestStartupFailsInProcessWhenMongoDBIsUnreachable(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	app, err := bootstrap.New(ctx, e2eConfig(startupUnreachableURI, "orch"))
	elapsed := time.Since(start)

	if err == nil {
		_ = app.Close(context.Background())
		t.Fatal("bootstrap.New succeeded against an unreachable MongoDB")
	}
	if app != nil {
		t.Errorf("App = %v, want nil on error", app)
	}
	if ctx.Err() != nil || elapsed >= 10*time.Second {
		t.Errorf("bootstrap.New took %v, want it to fail within 10s", elapsed)
	}
	startupRequireRedacted(t, "error", err.Error())
}

// startupBuildServer compiles cmd/server into dir and returns the binary's path.
func startupBuildServer(t *testing.T, dir string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bin := filepath.Join(dir, "server")
	out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, "orch-test/backend/cmd/server").CombinedOutput()
	if err != nil {
		t.Fatalf("build cmd/server: %v\n%s", err, out)
	}
	return bin
}

// startupFreePort returns a TCP port on 127.0.0.1 that is free right now.
func startupFreePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// startupCleanEnv is the current environment without anything that configures the server.
func startupCleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "MONGO_") {
			continue
		}
		switch name {
		case "STORAGE", "PORT", "LISTEN_HOST", "SEED_DEMO_DATA":
			continue
		}
		env = append(env, kv)
	}
	return env
}

// The server binary is built once for the tests below, in a directory that goes away with the
// parent test.
func TestStartupServerExitsWhenMongoDBIsUnreachable(t *testing.T) {
	bin := startupBuildServer(t, t.TempDir())

	tests := []struct {
		name  string
		extra []string
	}{
		{"credentials in the URI", nil},
		{"STORAGE=memory is ignored", []string{"STORAGE=memory"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := append(startupCleanEnv(),
				"MONGO_URI="+startupUnreachableURI,
				"MONGO_DB=orch",
				"LISTEN_HOST=127.0.0.1",
				"PORT="+startupFreePort(t),
			)
			env = append(env, tt.extra...)

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin)
			cmd.Env = env
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			err := cmd.Run()

			if ctx.Err() != nil {
				t.Fatalf("server still running after 15s, want it to exit:\n%s", out.String())
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("server exit = %v, want a non-zero exit status:\n%s", err, out.String())
			}
			if exitErr.ExitCode() == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			startupRequireRedacted(t, "output", out.String())
		})
	}
}

func TestStartupIgnoresStorageMemoryAndUsesMongoDB(t *testing.T) {
	name, db := e2eTestDB(t)
	t.Setenv("STORAGE", "memory")
	t.Setenv("MONGO_URI", e2eMongoURI(t))
	t.Setenv("MONGO_DB", name)
	t.Setenv("SEED_DEMO_DATA", "")

	cfg := bootstrap.ConfigFromEnv()
	cfg.RateLimit = generousLimits()
	env := e2eServe(t, cfg, db)

	const isbn = "9780132350884"
	ctx := context.Background()
	book, err := env.Client.CreateBook(ctx, &oas.BookInput{
		Isbn:    "978-0-13-235088-4",
		Title:   "Clean Code",
		Authors: []string{"Robert C. Martin"},
	})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if book.Isbn != isbn {
		t.Fatalf("ISBN = %q, want the normalized %q", book.Isbn, isbn)
	}

	var stored bson.M
	if err := db.Collection("books").FindOne(ctx, bson.D{{Key: "isbn", Value: isbn}}).Decode(&stored); err != nil {
		t.Fatalf("book %s is not in MongoDB database %s: %v", isbn, name, err)
	}
	if stored["title"] != "Clean Code" {
		t.Errorf("stored title = %v, want Clean Code", stored["title"])
	}

	var entry bson.M
	if err := db.Collection(mongodb.LedgerCollection).FindOne(ctx, bson.D{{Key: "_id", Value: "catalog/0001"}}).Decode(&entry); err != nil {
		t.Fatalf("migration catalog/0001 is not in the ledger: %v", err)
	}
}
