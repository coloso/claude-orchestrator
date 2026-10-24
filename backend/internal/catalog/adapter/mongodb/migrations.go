// Package mongodb holds the catalog context's MongoDB storage: its schema, expressed as migrations.
package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	platformmongo "orch-test/backend/internal/platform/mongodb"
)

// BooksCollection is the collection holding one document per book.
const BooksCollection = "books"

// Migrations is the catalog context's migration set, in sequence order.
//
// Future changes to the catalog schema are new migrations (catalog/0002, ...) appended to this
// list. An applied migration is never edited: databases that already ran it won't run it again.
func Migrations() []platformmongo.Migration {
	return []platformmongo.Migration{
		{
			ID:          "catalog/0001",
			Description: "create books collection with validator and indexes",
			Up:          createBooks,
		},
	}
}

// createBooks is the Up step of catalog/0001.
func createBooks(ctx context.Context, db *mongo.Database) error {
	if err := platformmongo.EnsureCollection(ctx, db, BooksCollection, booksSchema()); err != nil {
		return err
	}
	return platformmongo.EnsureIndexes(ctx, db, BooksCollection,
		mongo.IndexModel{
			Keys:    bson.D{{Key: "isbn", Value: 1}},
			Options: options.Index().SetName("isbn_unique").SetUnique(true),
		},
		mongo.IndexModel{
			Keys:    bson.D{{Key: "authors", Value: 1}},
			Options: options.Index().SetName("authors_lookup"),
		},
	)
}

// booksSchema is the $jsonSchema of the books collection. It is built from bson.D and bson.A
// only, so its field order is deterministic.
func booksSchema() bson.D {
	return bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "additionalProperties", Value: false},
		{Key: "required", Value: bson.A{"_id", "isbn", "title", "authors", "createdAt", "updatedAt", "version"}},
		{Key: "properties", Value: bson.D{
			// The int64 book ID, assigned from the counters document {_id: "books", seq}.
			{Key: "_id", Value: bson.D{{Key: "bsonType", Value: "long"}}},
			// ISBN-13: 13 digits starting with 978 or 979. The check digit is validated by the domain.
			{Key: "isbn", Value: bson.D{
				{Key: "bsonType", Value: "string"},
				{Key: "pattern", Value: "^97[89][0-9]{10}$"},
			}},
			{Key: "title", Value: bson.D{
				{Key: "bsonType", Value: "string"},
				{Key: "minLength", Value: 1},
				{Key: "maxLength", Value: 300},
			}},
			{Key: "authors", Value: bson.D{
				{Key: "bsonType", Value: "array"},
				{Key: "minItems", Value: 1},
				{Key: "maxItems", Value: 20},
				{Key: "items", Value: bson.D{
					{Key: "bsonType", Value: "string"},
					{Key: "minLength", Value: 1},
					{Key: "maxLength", Value: 200},
				}},
			}},
			{Key: "publishedYear", Value: bson.D{
				{Key: "bsonType", Value: "int"},
				{Key: "minimum", Value: 1450},
				{Key: "maximum", Value: 2100},
			}},
			// ISO 639-1 language code.
			{Key: "language", Value: bson.D{
				{Key: "bsonType", Value: "string"},
				{Key: "pattern", Value: "^[a-z]{2}$"},
			}},
			{Key: "pages", Value: bson.D{
				{Key: "bsonType", Value: "int"},
				{Key: "minimum", Value: 1},
				{Key: "maximum", Value: 10000},
			}},
			{Key: "createdAt", Value: bson.D{{Key: "bsonType", Value: "date"}}},
			{Key: "updatedAt", Value: bson.D{{Key: "bsonType", Value: "date"}}},
			// Version for optimistic concurrency.
			{Key: "version", Value: bson.D{
				{Key: "bsonType", Value: "long"},
				{Key: "minimum", Value: 1},
			}},
		}},
	}
}
