package mongodb

import (
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func ledgerTestEntry(id string) ledgerEntry {
	return ledgerEntry{
		ID:          id,
		Description: "description of " + id,
		AppliedAt:   time.Now().UTC().Truncate(time.Millisecond),
		DurationMs:  42,
	}
}

func TestLedger_EmptyReturnsNoIDs(t *testing.T) {
	db := newTestDB(t)

	ids, err := appliedMigrationIDs(t.Context(), db)
	if err != nil {
		t.Fatalf("appliedMigrationIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("ids = %v, want none", ids)
	}
}

func TestLedger_RecordedIDsAreSorted(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	for _, id := range []string{"catalog/0002", "auth/0001", "catalog/0001"} {
		if err := recordMigration(ctx, db, ledgerTestEntry(id)); err != nil {
			t.Fatalf("recordMigration(%s): %v", id, err)
		}
	}

	ids, err := appliedMigrationIDs(ctx, db)
	if err != nil {
		t.Fatalf("appliedMigrationIDs: %v", err)
	}
	want := []string{"auth/0001", "catalog/0001", "catalog/0002"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
}

func TestLedger_StoresFieldsWithBSONTypes(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()
	entry := ledgerTestEntry("catalog/0001")

	if err := recordMigration(ctx, db, entry); err != nil {
		t.Fatalf("recordMigration: %v", err)
	}

	raw, err := db.Collection(LedgerCollection).FindOne(ctx, bson.D{{Key: "_id", Value: entry.ID}}).Raw()
	if err != nil {
		t.Fatalf("find ledger entry: %v", err)
	}
	wantTypes := map[string]bson.Type{
		"_id":         bson.TypeString,
		"description": bson.TypeString,
		"appliedAt":   bson.TypeDateTime,
		"durationMs":  bson.TypeInt64,
	}
	for field, want := range wantTypes {
		v, err := raw.LookupErr(field)
		if err != nil {
			t.Fatalf("field %s missing from %s", field, raw)
		}
		if v.Type != want {
			t.Errorf("field %s has BSON type %s, want %s", field, v.Type, want)
		}
	}

	var got ledgerEntry
	if err := bson.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ID != entry.ID || got.Description != entry.Description ||
		!got.AppliedAt.Equal(entry.AppliedAt) || got.DurationMs != entry.DurationMs {
		t.Fatalf("stored %+v, want %+v", got, entry)
	}
}

func TestLedger_RecordingSameIDTwiceFails(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	if err := recordMigration(ctx, db, ledgerTestEntry("catalog/0001")); err != nil {
		t.Fatalf("first recordMigration: %v", err)
	}
	err := recordMigration(ctx, db, ledgerTestEntry("catalog/0001"))
	if err == nil {
		t.Fatal("second recordMigration of catalog/0001 succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "catalog/0001") {
		t.Fatalf("error %q does not name the migration ID", err)
	}
}
