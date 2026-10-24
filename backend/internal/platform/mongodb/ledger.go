package mongodb

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// LedgerCollection is the ledger: one document per applied migration, keyed by migration ID.
const LedgerCollection = "schema_migrations"

// ledgerEntry records one applied migration.
type ledgerEntry struct {
	ID          string    `bson:"_id"` // migration ID, e.g. "catalog/0001"
	Description string    `bson:"description"`
	AppliedAt   time.Time `bson:"appliedAt"` // UTC
	DurationMs  int64     `bson:"durationMs"`
}

// appliedMigrationIDs returns the IDs of all migrations in the ledger, sorted ascending.
func appliedMigrationIDs(ctx context.Context, db *mongo.Database) ([]string, error) {
	cur, err := db.Collection(LedgerCollection).Find(ctx, bson.D{},
		options.Find().
			SetProjection(bson.D{{Key: "_id", Value: 1}}).
			SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", LedgerCollection, err)
	}
	var docs []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("read %s: %w", LedgerCollection, err)
	}
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	return ids, nil
}

// recordMigration adds e to the ledger. The caller records a migration only after its Up step
// succeeded. Recording the same migration ID twice fails, with an error naming the ID.
func recordMigration(ctx context.Context, db *mongo.Database, e ledgerEntry) error {
	e.AppliedAt = e.AppliedAt.UTC()
	if _, err := db.Collection(LedgerCollection).InsertOne(ctx, e); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return fmt.Errorf("migration %s is already recorded in %s: %w", e.ID, LedgerCollection, err)
		}
		return fmt.Errorf("record migration %s in %s: %w", e.ID, LedgerCollection, err)
	}
	return nil
}
