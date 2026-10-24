package mongodb

import (
	"bytes"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const schemaTestCollection = "widgets"

// schemaTestWidgetV1 requires a string name.
func schemaTestWidgetV1() bson.D {
	return bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: bson.A{"name"}},
		{Key: "properties", Value: bson.D{
			{Key: "name", Value: bson.D{{Key: "bsonType", Value: "string"}}},
		}},
	}
}

// schemaTestWidgetV2 additionally requires an int size.
func schemaTestWidgetV2() bson.D {
	return bson.D{
		{Key: "bsonType", Value: "object"},
		{Key: "required", Value: bson.A{"name", "size"}},
		{Key: "properties", Value: bson.D{
			{Key: "name", Value: bson.D{{Key: "bsonType", Value: "string"}}},
			{Key: "size", Value: bson.D{{Key: "bsonType", Value: "int"}}},
		}},
	}
}

// schemaTestCollectionOptions returns the collection's options as listCollections reports them.
func schemaTestCollectionOptions(t *testing.T, db *mongo.Database, name string) bson.Raw {
	t.Helper()
	specs, err := db.ListCollectionSpecifications(t.Context(), bson.D{{Key: "name", Value: name}})
	if err != nil {
		t.Fatalf("listCollections: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("listCollections returned %d collections named %s, want 1", len(specs), name)
	}
	return specs[0].Options
}

func assertSchemaTestValidator(t *testing.T, db *mongo.Database, jsonSchema bson.D) {
	t.Helper()
	var got struct {
		Validator        bson.Raw `bson:"validator"`
		ValidationLevel  string   `bson:"validationLevel"`
		ValidationAction string   `bson:"validationAction"`
	}
	raw := schemaTestCollectionOptions(t, db, schemaTestCollection)
	if err := bson.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode collection options %s: %v", raw, err)
	}
	want, err := bson.Marshal(bson.D{{Key: "$jsonSchema", Value: jsonSchema}})
	if err != nil {
		t.Fatalf("marshal validator: %v", err)
	}
	if !bytes.Equal(got.Validator, want) {
		t.Errorf("validator = %s, want %s", got.Validator, bson.Raw(want))
	}
	if got.ValidationLevel != "strict" || got.ValidationAction != "error" {
		t.Errorf("validationLevel/validationAction = %q/%q, want strict/error", got.ValidationLevel, got.ValidationAction)
	}
}

func assertSchemaTestRejected(t *testing.T, db *mongo.Database, doc bson.D) {
	t.Helper()
	_, err := db.Collection(schemaTestCollection).InsertOne(t.Context(), doc)
	var we mongo.WriteException
	if !errors.As(err, &we) || !we.HasErrorCode(121) {
		t.Fatalf("insert %v = %v, want a WriteException with code 121 (DocumentValidationFailure)", doc, err)
	}
}

func assertSchemaTestAccepted(t *testing.T, db *mongo.Database, doc bson.D) {
	t.Helper()
	if _, err := db.Collection(schemaTestCollection).InsertOne(t.Context(), doc); err != nil {
		t.Fatalf("insert %v: %v", doc, err)
	}
}

func TestEnsureCollection_CreatesCollectionWithStrictValidator(t *testing.T) {
	db := newTestDB(t)

	if err := EnsureCollection(t.Context(), db, schemaTestCollection, schemaTestWidgetV1()); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}

	assertSchemaTestValidator(t, db, schemaTestWidgetV1())
	assertSchemaTestRejected(t, db, bson.D{{Key: "name", Value: 42}})
	assertSchemaTestRejected(t, db, bson.D{{Key: "size", Value: 1}})
	assertSchemaTestAccepted(t, db, bson.D{{Key: "name", Value: "bolt"}})
}

func TestEnsureCollection_SameDefinitionAgainChangesNothing(t *testing.T) {
	db := newTestDB(t)
	if err := EnsureCollection(t.Context(), db, schemaTestCollection, schemaTestWidgetV1()); err != nil {
		t.Fatalf("first EnsureCollection: %v", err)
	}
	before := schemaTestCollectionOptions(t, db, schemaTestCollection)

	if err := EnsureCollection(t.Context(), db, schemaTestCollection, schemaTestWidgetV1()); err != nil {
		t.Fatalf("second EnsureCollection: %v", err)
	}

	if after := schemaTestCollectionOptions(t, db, schemaTestCollection); !bytes.Equal(before, after) {
		t.Fatalf("collection options changed:\nbefore %s\nafter  %s", before, after)
	}
}

func TestEnsureCollection_ChangedSchemaIsApplied(t *testing.T) {
	db := newTestDB(t)
	if err := EnsureCollection(t.Context(), db, schemaTestCollection, schemaTestWidgetV1()); err != nil {
		t.Fatalf("EnsureCollection v1: %v", err)
	}
	nameOnly := bson.D{{Key: "name", Value: "bolt"}}
	assertSchemaTestAccepted(t, db, nameOnly)

	if err := EnsureCollection(t.Context(), db, schemaTestCollection, schemaTestWidgetV2()); err != nil {
		t.Fatalf("EnsureCollection v2: %v", err)
	}

	assertSchemaTestValidator(t, db, schemaTestWidgetV2())
	assertSchemaTestRejected(t, db, nameOnly)
	assertSchemaTestAccepted(t, db, bson.D{{Key: "name", Value: "nut"}, {Key: "size", Value: int32(8)}})
}

func TestEnsureCollection_AppliesValidatorToExistingCollection(t *testing.T) {
	db := newTestDB(t)
	// As if another instance had just created it, without our options.
	if err := db.CreateCollection(t.Context(), schemaTestCollection); err != nil {
		t.Fatalf("create collection: %v", err)
	}

	if err := EnsureCollection(t.Context(), db, schemaTestCollection, schemaTestWidgetV1()); err != nil {
		t.Fatalf("EnsureCollection: %v", err)
	}

	assertSchemaTestValidator(t, db, schemaTestWidgetV1())
	assertSchemaTestRejected(t, db, bson.D{{Key: "size", Value: 1}})
}

func schemaTestIndexModels() []mongo.IndexModel {
	return []mongo.IndexModel{
		{Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetName("name_unique").SetUnique(true)},
		{Keys: bson.D{{Key: "tags", Value: 1}}, Options: options.Index().SetName("tags")},
	}
}

func schemaTestIndexes(t *testing.T, db *mongo.Database) []mongo.IndexSpecification {
	t.Helper()
	specs, err := db.Collection(schemaTestCollection).Indexes().ListSpecifications(t.Context())
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	return specs
}

func TestEnsureIndexes_SecondRunKeepsIndexList(t *testing.T) {
	db := newTestDB(t)
	if err := EnsureIndexes(t.Context(), db, schemaTestCollection, schemaTestIndexModels()...); err != nil {
		t.Fatalf("first EnsureIndexes: %v", err)
	}
	before := schemaTestIndexes(t, db)

	if err := EnsureIndexes(t.Context(), db, schemaTestCollection, schemaTestIndexModels()...); err != nil {
		t.Fatalf("second EnsureIndexes: %v", err)
	}

	after := schemaTestIndexes(t, db)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("indexes changed:\nbefore %+v\nafter  %+v", before, after)
	}
	var names []string
	for _, s := range after {
		names = append(names, s.Name)
		if s.Name == "name_unique" && (s.Unique == nil || !*s.Unique) {
			t.Errorf("index name_unique is not unique")
		}
	}
	slices.Sort(names)
	if want := []string{"_id_", "name_unique", "tags"}; !slices.Equal(names, want) {
		t.Fatalf("index names = %v, want %v", names, want)
	}
}

func TestEnsureIndexes_RequiresNameOnEveryModel(t *testing.T) {
	db := newTestDB(t)
	for name, unnamed := range map[string]mongo.IndexModel{
		"no options":      {Keys: bson.D{{Key: "tags", Value: 1}}},
		"options no name": {Keys: bson.D{{Key: "tags", Value: 1}}, Options: options.Index().SetUnique(true)},
		"empty name":      {Keys: bson.D{{Key: "tags", Value: 1}}, Options: options.Index().SetName("")},
	} {
		t.Run(name, func(t *testing.T) {
			named := mongo.IndexModel{Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetName("name")}

			err := EnsureIndexes(t.Context(), db, schemaTestCollection, named, unnamed)

			if err == nil || !strings.Contains(err.Error(), "name") {
				t.Fatalf("EnsureIndexes = %v, want an error about the missing name", err)
			}
			collections, err := db.ListCollectionNames(t.Context(), bson.D{{Key: "name", Value: schemaTestCollection}})
			if err != nil {
				t.Fatalf("listCollections: %v", err)
			}
			if len(collections) != 0 {
				t.Fatalf("EnsureIndexes created %v before rejecting the unnamed index", collections)
			}
		})
	}
}

func TestEnsureIndexes_ChangedDefinitionFailsNamingTheIndex(t *testing.T) {
	db := newTestDB(t)
	if err := EnsureIndexes(t.Context(), db, schemaTestCollection, schemaTestIndexModels()...); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	changed := mongo.IndexModel{Keys: bson.D{{Key: "tags", Value: -1}}, Options: options.Index().SetName("tags")}

	err := EnsureIndexes(t.Context(), db, schemaTestCollection, changed)

	var se mongo.ServerError
	if !errors.As(err, &se) {
		t.Fatalf("EnsureIndexes = %v, want MongoDB's conflict error", err)
	}
	if !strings.Contains(err.Error(), `"tags"`) {
		t.Fatalf("error %q does not name the index", err)
	}
}
