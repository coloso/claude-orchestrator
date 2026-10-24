package mongodb

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// errCodeNamespaceExists is MongoDB's NamespaceExists: the collection already exists
// (on MongoDB >= 7.0 only if its options differ from the requested ones).
const errCodeNamespaceExists = 48

// EnsureCollection creates the collection with validator {$jsonSchema: jsonSchema}, validationLevel "strict"
// and validationAction "error", or, if the collection exists, applies that definition with collMod. Idempotent.
//
// Build jsonSchema with bson.D, never bson.M, so the definition is deterministic and running
// EnsureCollection again with the same schema leaves the collection's options unchanged.
func EnsureCollection(ctx context.Context, db *mongo.Database, name string, jsonSchema bson.D) error {
	validator := bson.D{{Key: "$jsonSchema", Value: jsonSchema}}
	err := db.CreateCollection(ctx, name, options.CreateCollection().
		SetValidator(validator).
		SetValidationLevel("strict").
		SetValidationAction("error"))
	if err == nil {
		return nil
	}
	// The collection exists already, possibly created concurrently by another instance.
	if se := mongo.ServerError(nil); !errors.As(err, &se) || !se.HasErrorCode(errCodeNamespaceExists) {
		return fmt.Errorf("create collection %s: %w", name, err)
	}
	collMod := bson.D{
		{Key: "collMod", Value: name},
		{Key: "validator", Value: validator},
		{Key: "validationLevel", Value: "strict"},
		{Key: "validationAction", Value: "error"},
	}
	if err := db.RunCommand(ctx, collMod).Err(); err != nil {
		return fmt.Errorf("update validator of collection %s: %w", name, err)
	}
	return nil
}

// EnsureIndexes creates the indexes on collection. Idempotent for identical definitions.
//
// Every model needs an explicit name (options.Index().SetName), so that an index is identified by
// its name rather than by a name MongoDB derives from the keys; models are checked before any
// index is created.
//
// Changing the definition of an existing index isn't supported: MongoDB rejects it with a conflict
// error (IndexOptionsConflict or IndexKeySpecsConflict), which is returned wrapped with the index
// name. To change an index, write a new migration that drops the old index first.
func EnsureIndexes(ctx context.Context, db *mongo.Database, collection string, models ...mongo.IndexModel) error {
	names := make([]string, len(models))
	for i, m := range models {
		name, err := indexModelName(m)
		if err != nil {
			return fmt.Errorf("index %d on %s: %w", i, collection, err)
		}
		if name == "" {
			return fmt.Errorf("index %d on %s has no name: set one with options.Index().SetName", i, collection)
		}
		names[i] = name
	}
	indexes := db.Collection(collection).Indexes()
	for i, m := range models {
		if _, err := indexes.CreateOne(ctx, m); err != nil {
			return fmt.Errorf("create index %q on %s: %w", names[i], collection, err)
		}
	}
	return nil
}

// indexModelName returns the name set in m's options, or "" if there is none.
func indexModelName(m mongo.IndexModel) (string, error) {
	if m.Options == nil {
		return "", nil
	}
	var opts options.IndexOptions
	for _, set := range m.Options.List() {
		if err := set(&opts); err != nil {
			return "", err
		}
	}
	if opts.Name == nil {
		return "", nil
	}
	return *opts.Name, nil
}
