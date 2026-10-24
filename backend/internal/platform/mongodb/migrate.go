package mongodb

// Migrate: the schema migrator. It ties the planning logic, the ledger and the schema lock together.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

const (
	// DefaultLease is the schema lock lease when MigrateOptions.Lease is zero.
	DefaultLease = 30 * time.Second
	// MaxLease is the longest allowed lease: a dead holder blocks the others no longer than this.
	MaxLease = 60 * time.Second
	// DefaultPollInterval is the wait between lock attempts when MigrateOptions.PollInterval is zero.
	DefaultPollInterval = 500 * time.Millisecond
)

// releaseTimeout bounds releasing the lock, which must work even after ctx was cancelled.
const releaseTimeout = 5 * time.Second

// MigrateOptions tunes Migrate; the zero value uses the defaults.
type MigrateOptions struct {
	Lease        time.Duration // schema lock lease; 0 = DefaultLease; > MaxLease is an error
	PollInterval time.Duration // wait between lock attempts; 0 = DefaultPollInterval
	Owner        string        // lock owner; "" = hostname plus random hex (crypto/rand)
	Logger       *slog.Logger  // nil = slog.Default(), looked up at call time
}

// withDefaults validates the options and fills in the defaults.
func (o MigrateOptions) withDefaults() (MigrateOptions, error) {
	switch {
	case o.Lease < 0:
		return o, fmt.Errorf("migrate: lease must not be negative, got %v", o.Lease)
	case o.Lease > MaxLease:
		return o, fmt.Errorf("migrate: lease %v exceeds the maximum of %v", o.Lease, MaxLease)
	case o.Lease == 0:
		o.Lease = DefaultLease
	}
	switch {
	case o.PollInterval < 0:
		return o, fmt.Errorf("migrate: poll interval must not be negative, got %v", o.PollInterval)
	case o.PollInterval == 0:
		o.PollInterval = DefaultPollInterval
	}
	if o.Owner == "" {
		owner, err := newLockOwner()
		if err != nil {
			return o, err
		}
		o.Owner = owner
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	return o, nil
}

// newLockOwner returns the hostname plus random hex, unique per Migrate call.
func newLockOwner() (string, error) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("migrate: generate lock owner: %w", err)
	}
	return host + "-" + hex.EncodeToString(suffix[:]), nil
}

// Migrate applies all pending migrations of sets to db, in order: the sets in the given order,
// each set in sequence order. Only one instance migrates at a time, guarded by the schema lock;
// the others wait for it and then re-check the ledger. If nothing is pending, Migrate returns
// without taking the lock. A failing migration is not recorded, and Migrate returns an error
// naming its ID.
func Migrate(ctx context.Context, db *mongo.Database, sets [][]Migration, opts MigrateOptions) error {
	opts, err := opts.withDefaults()
	if err != nil {
		return err
	}
	plan, err := Plan(sets...)
	if err != nil {
		return err
	}
	if db == nil {
		return errors.New("migrate: database is nil")
	}

	applied, err := appliedMigrationIDs(ctx, db)
	if err != nil {
		return err
	}
	pending, err := pendingMigrations(plan, applied)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	lock := newSchemaLock(db, opts.Owner, opts.Lease)
	if err := lock.acquire(ctx, opts.PollInterval); err != nil {
		return err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
		defer cancel()
		if err := lock.release(releaseCtx); err != nil {
			// The lease expires on its own, so this only delays the other instances.
			opts.Logger.Warn("schema lock not released", "error", err)
		}
	}()

	migCtx, cancel := context.WithCancelCause(ctx)
	var renewer sync.WaitGroup
	renewer.Add(1)
	go func() {
		defer renewer.Done()
		renewLoop(migCtx, cancel, lock, opts.Lease/3)
	}()
	// Runs before the deferred release above: the renewer must be gone before we give up the lock.
	defer func() {
		cancel(nil)
		renewer.Wait()
	}()

	// Another instance may have migrated while we waited for the lock.
	applied, err = appliedMigrationIDs(migCtx, db)
	if err != nil {
		return err
	}
	pending, err = pendingMigrations(plan, applied)
	if err != nil {
		return err
	}

	for _, m := range pending {
		if err := migCtx.Err(); err != nil {
			return fmt.Errorf("schema migration %s not applied: %w", m.ID, context.Cause(migCtx))
		}
		start := time.Now()
		if err := m.Up(migCtx, db); err != nil {
			return fmt.Errorf("schema migration %s failed: %w", m.ID, err)
		}
		finished := time.Now()
		duration := finished.Sub(start)
		entry := ledgerEntry{
			ID:          m.ID,
			Description: m.Description,
			AppliedAt:   finished.UTC(),
			DurationMs:  duration.Milliseconds(),
		}
		if err := recordMigration(migCtx, db, entry); err != nil {
			if migCtx.Err() != nil && context.Cause(migCtx) != nil {
				// Most likely the lock was lost; report why, not just "context canceled".
				return fmt.Errorf("schema migration %s applied but not recorded: %w (%w)", m.ID, err, context.Cause(migCtx))
			}
			return fmt.Errorf("schema migration %s applied but not recorded: %w", m.ID, err)
		}
		opts.Logger.Info("schema migration applied",
			"id", m.ID,
			"description", m.Description,
			"durationMs", entry.DurationMs,
		)
	}
	return nil
}

// renewLoop renews the lock every interval until ctx is done. If a renewal fails, the lock may
// be lost, so it cancels the migration with the failure as the cause.
func renewLoop(ctx context.Context, cancel context.CancelCauseFunc, lock *schemaLock, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := lock.renew(ctx); err != nil {
				if ctx.Err() == nil {
					cancel(err)
				}
				return
			}
		}
	}
}
