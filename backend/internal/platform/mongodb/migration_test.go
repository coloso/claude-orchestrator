package mongodb

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

// planTestUp is a no-op Up step; Plan only stores it.
func planTestUp(context.Context, *mongo.Database) error { return nil }

// planTestMigration builds a valid migration with the given ID.
func planTestMigration(id string) Migration {
	return Migration{ID: id, Description: "migration " + id, Up: planTestUp}
}

// planTestSet builds a valid migration set from IDs.
func planTestSet(ids ...string) []Migration {
	set := make([]Migration, 0, len(ids))
	for _, id := range ids {
		set = append(set, planTestMigration(id))
	}
	return set
}

func planTestIDs(ms []Migration) []string {
	ids := make([]string, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestPlanValid(t *testing.T) {
	tests := []struct {
		name string
		sets [][]Migration
		want []string
	}{
		{
			name: "no sets",
			sets: nil,
			want: []string{},
		},
		{
			name: "single set",
			sets: [][]Migration{planTestSet("catalog/0001", "catalog/0002", "catalog/0003")},
			want: []string{"catalog/0001", "catalog/0002", "catalog/0003"},
		},
		{
			name: "several sets in the given order",
			sets: [][]Migration{
				planTestSet("todo/0001", "todo/0002"),
				planTestSet("catalog/0001"),
				planTestSet("order_line/0001", "order_line/0002"),
			},
			want: []string{"todo/0001", "todo/0002", "catalog/0001", "order_line/0001", "order_line/0002"},
		},
		{
			name: "empty set",
			sets: [][]Migration{{}},
			want: []string{},
		},
		{
			name: "empty set among others",
			sets: [][]Migration{planTestSet("catalog/0001"), {}, planTestSet("todo/0001")},
			want: []string{"catalog/0001", "todo/0001"},
		},
		{
			name: "context with digits and underscores",
			sets: [][]Migration{planTestSet("a1_b/0001")},
			want: []string{"a1_b/0001"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Plan(tt.sets...)
			if err != nil {
				t.Fatalf("Plan: unexpected error: %v", err)
			}
			if ids := planTestIDs(got); !slices.Equal(ids, tt.want) {
				t.Errorf("Plan order = %v, want %v", ids, tt.want)
			}
			for _, m := range got {
				if m.Up == nil || m.Description == "" {
					t.Errorf("Plan dropped fields of %s", m.ID)
				}
			}
		})
	}
}

func TestPlanDoesNotModifyInput(t *testing.T) {
	set := planTestSet("catalog/0001", "catalog/0002")
	got, err := Plan(set)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got[0].ID = "changed/0001"
	got[1].Description = "changed"
	if set[0].ID != "catalog/0001" || set[1].Description != "migration catalog/0002" {
		t.Errorf("Plan result shares memory with its input: %+v", set)
	}
}

func TestPlanInvalid(t *testing.T) {
	tests := []struct {
		name      string
		sets      [][]Migration
		wantInMsg []string
	}{
		// Malformed IDs.
		{name: "empty ID", sets: [][]Migration{planTestSet("")}, wantInMsg: []string{`""`, "invalid ID"}},
		{name: "no sequence", sets: [][]Migration{planTestSet("catalog")}, wantInMsg: []string{"catalog", "invalid ID"}},
		{name: "no context", sets: [][]Migration{planTestSet("/0001")}, wantInMsg: []string{"/0001", "invalid ID"}},
		{name: "uppercase context", sets: [][]Migration{planTestSet("Catalog/0001")}, wantInMsg: []string{"Catalog/0001", "invalid ID"}},
		{name: "context starts with digit", sets: [][]Migration{planTestSet("1catalog/0001")}, wantInMsg: []string{"1catalog/0001", "invalid ID"}},
		{name: "context starts with underscore", sets: [][]Migration{planTestSet("_catalog/0001")}, wantInMsg: []string{"_catalog/0001", "invalid ID"}},
		{name: "hyphen in context", sets: [][]Migration{planTestSet("cat-alog/0001")}, wantInMsg: []string{"cat-alog/0001", "invalid ID"}},
		{name: "three-digit sequence", sets: [][]Migration{planTestSet("catalog/001")}, wantInMsg: []string{"catalog/001", "invalid ID"}},
		{name: "five-digit sequence", sets: [][]Migration{planTestSet("catalog/00001")}, wantInMsg: []string{"catalog/00001", "invalid ID"}},
		{name: "non-numeric sequence", sets: [][]Migration{planTestSet("catalog/abcd")}, wantInMsg: []string{"catalog/abcd", "invalid ID"}},
		{name: "extra path segment", sets: [][]Migration{planTestSet("catalog/0001/x")}, wantInMsg: []string{"catalog/0001/x", "invalid ID"}},
		{name: "surrounding whitespace", sets: [][]Migration{planTestSet(" catalog/0001")}, wantInMsg: []string{" catalog/0001", "invalid ID"}},
		{name: "trailing newline", sets: [][]Migration{planTestSet("catalog/0001\n")}, wantInMsg: []string{`catalog/0001\n`, "invalid ID"}},
		{name: "sequence 0000", sets: [][]Migration{planTestSet("catalog/0000")}, wantInMsg: []string{"catalog/0000", "0000"}},

		// Other fields.
		{
			name:      "blank description",
			sets:      [][]Migration{{planTestMigration("catalog/0001"), {ID: "catalog/0002", Description: " \t\n", Up: planTestUp}}},
			wantInMsg: []string{"catalog/0002", "description"},
		},
		{
			name:      "empty description",
			sets:      [][]Migration{{{ID: "catalog/0001", Up: planTestUp}}},
			wantInMsg: []string{"catalog/0001", "description"},
		},
		{
			name:      "nil Up",
			sets:      [][]Migration{{planTestMigration("catalog/0001"), {ID: "catalog/0002", Description: "x"}}},
			wantInMsg: []string{"catalog/0002", "Up"},
		},

		// Contexts.
		{
			name:      "mixed contexts in one set",
			sets:      [][]Migration{planTestSet("catalog/0001", "todo/0002")},
			wantInMsg: []string{"todo/0002", `"catalog"`},
		},
		{
			name:      "context registered twice",
			sets:      [][]Migration{planTestSet("catalog/0001"), planTestSet("todo/0001"), planTestSet("catalog/0001", "catalog/0002")},
			wantInMsg: []string{`"catalog"`, "more than one"},
		},

		// Sequence checks.
		{
			name:      "duplicate",
			sets:      [][]Migration{planTestSet("catalog/0001", "catalog/0002", "catalog/0002")},
			wantInMsg: []string{"catalog/0002", "duplicate"},
		},
		{
			name:      "duplicate is reported before out of order and gap",
			sets:      [][]Migration{planTestSet("catalog/0004", "catalog/0001", "catalog/0004")},
			wantInMsg: []string{"catalog/0004", "duplicate"},
		},
		{
			name:      "out of order",
			sets:      [][]Migration{planTestSet("catalog/0001", "catalog/0003", "catalog/0002")},
			wantInMsg: []string{"catalog/0002", "out of order"},
		},
		{
			name:      "out of order is reported before gap",
			sets:      [][]Migration{planTestSet("catalog/0005", "catalog/0001")},
			wantInMsg: []string{"catalog/0001", "out of order"},
		},
		{
			name:      "gap: hole",
			sets:      [][]Migration{planTestSet("catalog/0001", "catalog/0002", "catalog/0004")},
			wantInMsg: []string{"catalog/0004", "gap", "catalog/0003"},
		},
		{
			name:      "gap: starts at 0002",
			sets:      [][]Migration{planTestSet("catalog/0002", "catalog/0003")},
			wantInMsg: []string{"catalog/0002", "gap", "catalog/0001"},
		},
		{
			name:      "invalid second set",
			sets:      [][]Migration{planTestSet("catalog/0001"), planTestSet("todo/0002")},
			wantInMsg: []string{"todo/0002", "gap"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Plan(tt.sets...)
			if !errors.Is(err, ErrInvalidMigrationSet) {
				t.Fatalf("Plan error = %v, want ErrInvalidMigrationSet", err)
			}
			if got != nil {
				t.Errorf("Plan returned %v alongside an error", planTestIDs(got))
			}
			for _, want := range tt.wantInMsg {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Plan error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestPendingMigrations(t *testing.T) {
	plan := []Migration{
		planTestMigration("todo/0001"),
		planTestMigration("todo/0002"),
		planTestMigration("catalog/0001"),
		planTestMigration("catalog/0002"),
	}
	tests := []struct {
		name    string
		applied []string
		want    []string
	}{
		{
			name:    "nothing applied",
			applied: nil,
			want:    []string{"todo/0001", "todo/0002", "catalog/0001", "catalog/0002"},
		},
		{
			name:    "everything applied",
			applied: []string{"todo/0001", "todo/0002", "catalog/0001", "catalog/0002"},
			want:    []string{},
		},
		{
			name:    "only the newest pending",
			applied: []string{"todo/0001", "todo/0002", "catalog/0001"},
			want:    []string{"catalog/0002"},
		},
		{
			name:    "applied in random order",
			applied: []string{"catalog/0001", "todo/0002", "todo/0001"},
			want:    []string{"catalog/0002"},
		},
		{
			name:    "pending in several contexts keep plan order",
			applied: []string{"catalog/0001", "todo/0001"},
			want:    []string{"todo/0002", "catalog/0002"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pendingMigrations(plan, tt.applied)
			if err != nil {
				t.Fatalf("pendingMigrations: unexpected error: %v", err)
			}
			if ids := planTestIDs(got); !slices.Equal(ids, tt.want) {
				t.Errorf("pendingMigrations = %v, want %v", ids, tt.want)
			}
		})
	}
}

func TestPendingMigrationsUnknown(t *testing.T) {
	plan := planTestSet("catalog/0001", "catalog/0002")
	tests := []struct {
		name    string
		applied []string
		wantMsg string
	}{
		{
			name:    "one unknown ID",
			applied: []string{"catalog/0001", "catalog/0002", "catalog/0003"},
			wantMsg: "unknown migration in ledger: catalog/0003 (the database is newer than this binary)",
		},
		{
			name:    "several unknown IDs, sorted",
			applied: []string{"todo/0001", "catalog/0001", "catalog/0004", "catalog/0003"},
			wantMsg: "unknown migration in ledger: catalog/0003, catalog/0004, todo/0001 (the database is newer than this binary)",
		},
		{
			name:    "unknown ID listed once",
			applied: []string{"catalog/0003", "catalog/0003"},
			wantMsg: "unknown migration in ledger: catalog/0003 (the database is newer than this binary)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pendingMigrations(plan, tt.applied)
			if !errors.Is(err, ErrUnknownMigration) {
				t.Fatalf("pendingMigrations error = %v, want ErrUnknownMigration", err)
			}
			if got != nil {
				t.Errorf("pendingMigrations returned %v alongside an error", planTestIDs(got))
			}
			if err.Error() != tt.wantMsg {
				t.Errorf("pendingMigrations error = %q, want %q", err, tt.wantMsg)
			}
		})
	}
}
