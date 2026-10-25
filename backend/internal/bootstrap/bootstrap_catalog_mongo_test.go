package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"orch-test/backend/internal/platform/mongodb"
)

func TestNewStoresBooksInMongo(t *testing.T) {
	uri, dbName := newTestDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := mongodb.Connect(ctx, uri)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })

	app, err := New(ctx, Config{MongoURI: uri, MongoDB: dbName})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	body := `{"isbn":"978-0-13-468599-1","title":"The Go Programming Language","authors":["Alan Donovan"]}`
	req := httptest.NewRequest(http.MethodPost, "/api/books", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/books = %d: %s", rec.Code, rec.Body)
	}

	var stored struct {
		ID    int64  `bson:"_id"`
		Title string `bson:"title"`
	}
	err = client.Database(dbName).Collection("books").FindOne(ctx, bson.D{{Key: "isbn", Value: "9780134685991"}}).Decode(&stored)
	if err != nil {
		t.Fatalf("book not found in the books collection: %v", err)
	}
	if stored.ID != 1 || stored.Title != "The Go Programming Language" {
		t.Fatalf("stored book = %+v", stored)
	}
	if got := listTotal(t, app); got != 1 {
		t.Fatalf("list total = %d, want 1", got)
	}
}
