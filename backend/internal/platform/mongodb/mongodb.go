// Package mongodb connects to MongoDB. It is shared infrastructure: only adapters
// (internal/<context>/adapter/mongodb) and internal/bootstrap may import it.
package mongodb

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// Connect opens a client for uri and verifies the connection with a ping.
func Connect(ctx context.Context, uri string) (*mongo.Client, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetServerSelectionTimeout(5 * time.Second))
	if err != nil {
		return nil, fmt.Errorf("mongodb connect: %w", err)
	}
	if err := Ping(ctx, client); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return client, nil
}

// Ping checks that the primary is reachable.
func Ping(ctx context.Context, client *mongo.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return fmt.Errorf("mongodb ping: %w", err)
	}
	return nil
}
