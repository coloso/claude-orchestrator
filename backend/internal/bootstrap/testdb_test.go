package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"orch-test/backend/internal/platform/mongodb"
)

// newTestDB returns MONGO_TEST_URI and the name of a fresh database on that server. The
// database is dropped when the test ends. MongoDB creates it on the first write. The tests
// never skip without MONGO_TEST_URI, so a missing setting cannot pass for a green run.
func newTestDB(t *testing.T) (uri, dbName string) {
	t.Helper()
	uri = os.Getenv("MONGO_TEST_URI")
	if uri == "" {
		t.Fatal("MONGO_TEST_URI is not set: the bootstrap tests run against MongoDB. Run `make mongo`, then `export MONGO_TEST_URI=mongodb://localhost:27017`")
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("random database name: %v", err)
	}
	dbName = "test_" + hex.EncodeToString(suffix[:])

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
	return uri, dbName
}
