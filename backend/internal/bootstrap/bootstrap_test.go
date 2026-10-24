package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
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
