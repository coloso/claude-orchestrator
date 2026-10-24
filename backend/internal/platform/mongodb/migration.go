package mongodb

// Schema migrations: the pure planning logic. Nothing in this file touches the database.
//
// Ubiquitous language:
//   - Migration: one forward-only, numbered schema change. It has an ID
//     "<context>/<NNNN>" (e.g. "catalog/0001"), a Description and an Up step.
//   - Migration set: all migrations of one bounded context, in sequence order.
//   - Ledger: the schema_migrations collection, which records each applied migration.
//   - To migrate: to apply all pending migrations in order.
//
// Rules:
//   - Migrations are forward-only and have no down step; a mistake is fixed by a new migration.
//   - Within a context, sequence numbers start at 0001 and have no gaps or duplicates.
//   - Each context migrates independently of the others.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Migration is one forward-only, numbered schema change of one bounded context.
type Migration struct {
	ID          string // "<context>/<NNNN>", e.g. "catalog/0001"
	Description string
	Up          func(ctx context.Context, db *mongo.Database) error
}

var (
	// ErrInvalidMigrationSet is wrapped by every error Plan returns.
	ErrInvalidMigrationSet = errors.New("invalid migration set")
	// ErrUnknownMigration means the ledger records migrations this binary doesn't know.
	ErrUnknownMigration = errors.New("unknown migration in ledger")
)

var migrationIDPattern = regexp.MustCompile(`^([a-z][a-z0-9_]*)/([0-9]{4})$`)

// parseMigrationID splits a migration ID into its context and sequence number.
func parseMigrationID(id string) (migrationContext string, seq int, err error) {
	m := migrationIDPattern.FindStringSubmatch(id)
	if m == nil {
		return "", 0, fmt.Errorf("%w: invalid ID %q, want <context>/<NNNN>", ErrInvalidMigrationSet, id)
	}
	seq, _ = strconv.Atoi(m[2]) // four digits, always parses
	if seq == 0 {
		return "", 0, fmt.Errorf("%w: invalid ID %q, sequence numbers start at 0001, not 0000", ErrInvalidMigrationSet, id)
	}
	return m[1], seq, nil
}

// Plan validates the migration sets and returns all migrations in apply order:
// the sets in the given order, each one in sequence order. It never touches the database.
func Plan(sets ...[]Migration) ([]Migration, error) {
	plan := []Migration{}
	seen := map[string]bool{}
	for _, set := range sets {
		migrationContext, err := validateMigrationSet(set)
		if err != nil {
			return nil, err
		}
		if migrationContext == "" {
			continue // empty set: a context without migrations yet
		}
		if seen[migrationContext] {
			return nil, fmt.Errorf("%w: context %q appears in more than one set", ErrInvalidMigrationSet, migrationContext)
		}
		seen[migrationContext] = true
		plan = append(plan, set...)
	}
	return plan, nil
}

// validateMigrationSet checks one migration set and returns its context ("" for an empty set).
func validateMigrationSet(set []Migration) (string, error) {
	var migrationContext string
	seqs := make([]int, len(set))
	for i, m := range set {
		ctxName, seq, err := parseMigrationID(m.ID)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(m.Description) == "" {
			return "", fmt.Errorf("%w: %s has a blank description", ErrInvalidMigrationSet, m.ID)
		}
		if m.Up == nil {
			return "", fmt.Errorf("%w: %s has no Up step", ErrInvalidMigrationSet, m.ID)
		}
		if i == 0 {
			migrationContext = ctxName
		} else if ctxName != migrationContext {
			return "", fmt.Errorf("%w: %s does not belong to context %q of its set", ErrInvalidMigrationSet, m.ID, migrationContext)
		}
		seqs[i] = seq
	}

	seenSeq := map[int]bool{}
	for i, seq := range seqs {
		if seenSeq[seq] {
			return "", fmt.Errorf("%w: %s: duplicate sequence number", ErrInvalidMigrationSet, set[i].ID)
		}
		seenSeq[seq] = true
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] < seqs[i-1] {
			return "", fmt.Errorf("%w: %s: out of order, comes after %s", ErrInvalidMigrationSet, set[i].ID, set[i-1].ID)
		}
	}
	for i, seq := range seqs {
		if want := i + 1; seq != want {
			return "", fmt.Errorf("%w: %s: gap in sequence, expected %s/%04d", ErrInvalidMigrationSet, set[i].ID, migrationContext, want)
		}
	}
	return migrationContext, nil
}

// pendingMigrations returns the migrations of plan whose ID is not in applied, in plan order.
func pendingMigrations(plan []Migration, applied []string) ([]Migration, error) {
	known := make(map[string]bool, len(plan))
	for _, m := range plan {
		known[m.ID] = true
	}
	done := make(map[string]bool, len(applied))
	var unknown []string
	for _, id := range applied {
		if !known[id] && !done[id] {
			unknown = append(unknown, id)
		}
		done[id] = true
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return nil, fmt.Errorf("%w: %s (the database is newer than this binary)", ErrUnknownMigration, strings.Join(unknown, ", "))
	}

	pending := []Migration{}
	for _, m := range plan {
		if !done[m.ID] {
			pending = append(pending, m)
		}
	}
	return pending, nil
}
