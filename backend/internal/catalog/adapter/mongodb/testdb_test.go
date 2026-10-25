package mongodb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// newTestDB returns a fresh database on the server at MONGO_TEST_URI, or fails the test if it
// isn't set. Each test gets its own database (test_<random hex>), because parallel agents share
// one server; it is dropped, and the client disconnected, when the test ends.
func newTestDB(t *testing.T) *mongo.Database {
	t.Helper()
	uri := os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Fatal("MONGO_TEST_URI is not set: run `make mongo`, then `export MONGO_TEST_URI=mongodb://localhost:27017`")
	}

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

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("random database name: %v", err)
	}
	db := client.Database("test_" + hex.EncodeToString(suffix[:]))

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := db.Drop(ctx); err != nil {
			t.Errorf("drop test database %s: %v", db.Name(), err)
		}
		if err := client.Disconnect(ctx); err != nil {
			t.Errorf("disconnect: %v", err)
		}
	})
	return db
}
