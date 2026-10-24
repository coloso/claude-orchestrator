package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// LockCollection holds the schema lock: one document {_id: "schema", owner: string, expiresAt: date}.
const LockCollection = "schema_lock"

// ErrSchemaLockLost means another instance took over the schema lock, or it was released.
var ErrSchemaLockLost = errors.New("schema lock lost")

const schemaLockDocID = "schema"

// schemaLock is a lease on the schema lock, held by the one instance that applies migrations.
// The lease expires on its own if the holder dies, so the holder renews it while migrating.
// Expiry is decided by the server's clock, so instances with skewed clocks agree on it.
type schemaLock struct {
	db    *mongo.Database
	coll  *mongo.Collection
	owner string
	lease time.Duration
}

// newSchemaLock returns the lock for owner, which must be unique per instance. lease should be
// at most 60 s, so a dead holder blocks the others no longer than that.
func newSchemaLock(db *mongo.Database, owner string, lease time.Duration) *schemaLock {
	return &schemaLock{db: db, coll: db.Collection(LockCollection), owner: owner, lease: lease}
}

// tryAcquire makes one attempt to take the lock, and reports whether we hold it now.
//
// It is a single atomic upsert on {_id: "schema"} that only matches if we already own the lock
// or its lease has expired. If another live instance holds it, the filter doesn't match, the
// upsert tries to insert a second document with the same _id, and fails with a duplicate key.
//
// Both sides of the expiry use the server clock. expiresAt is written as $$NOW + lease. The filter
// can't use $$NOW, because MongoDB rejects $expr in the filter of an upsert, so its "now" is the
// server's localTime, read just before. That lags behind the server's real clock by one round
// trip, so an expired lease is at worst taken over that much later, never too early.
func (l *schemaLock) tryAcquire(ctx context.Context) (bool, error) {
	now, err := schemaLockServerTime(ctx, l.db)
	if err != nil {
		return false, fmt.Errorf("acquire schema lock: %w", err)
	}
	filter := bson.D{
		{Key: "_id", Value: schemaLockDocID},
		{Key: "$or", Value: bson.A{
			bson.D{{Key: "owner", Value: l.owner}},
			bson.D{{Key: "expiresAt", Value: bson.D{{Key: "$lte", Value: now}}}},
		}},
	}
	update := mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "owner", Value: bson.D{{Key: "$literal", Value: l.owner}}},
		{Key: "expiresAt", Value: l.leaseEnd()},
	}}}}
	res, err := l.coll.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("acquire schema lock: %w", err)
	}
	return res.MatchedCount+res.UpsertedCount == 1, nil
}

// acquire calls tryAcquire every poll until we hold the lock, or until ctx is done.
func (l *schemaLock) acquire(ctx context.Context, poll time.Duration) error {
	if poll <= 0 {
		return fmt.Errorf("acquire schema lock: poll interval must be positive, got %v", poll)
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		held, err := l.tryAcquire(ctx)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("acquire schema lock: %w", ctxErr)
			}
			return err
		}
		if held {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("acquire schema lock: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// renew extends our lease to now + lease. It returns ErrSchemaLockLost if we no longer own the
// lock, because another instance took over an expired lease or the lock was released.
func (l *schemaLock) renew(ctx context.Context) error {
	update := mongo.Pipeline{{{Key: "$set", Value: bson.D{{Key: "expiresAt", Value: l.leaseEnd()}}}}}
	res, err := l.coll.UpdateOne(ctx, l.ownedFilter(), update)
	if err != nil {
		return fmt.Errorf("renew schema lock: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrSchemaLockLost
	}
	return nil
}

// release gives up the lock if we own it. Releasing a lock we don't own deletes nothing,
// so release is idempotent.
func (l *schemaLock) release(ctx context.Context) error {
	if _, err := l.coll.DeleteOne(ctx, l.ownedFilter()); err != nil {
		return fmt.Errorf("release schema lock: %w", err)
	}
	return nil
}

// ownedFilter matches the lock document only while we own it.
func (l *schemaLock) ownedFilter() bson.D {
	return bson.D{{Key: "_id", Value: schemaLockDocID}, {Key: "owner", Value: l.owner}}
}

// leaseEnd is the aggregation expression for the server's now + lease.
func (l *schemaLock) leaseEnd() bson.D {
	return bson.D{{Key: "$add", Value: bson.A{"$$NOW", l.lease.Milliseconds()}}}
}

// schemaLockServerTime returns the server's clock (the localTime field of hello).
func schemaLockServerTime(ctx context.Context, db *mongo.Database) (time.Time, error) {
	var reply struct {
		LocalTime time.Time `bson:"localTime"`
	}
	if err := db.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&reply); err != nil {
		return time.Time{}, fmt.Errorf("read server clock: %w", err)
	}
	if reply.LocalTime.IsZero() {
		return time.Time{}, errors.New("read server clock: hello returned no localTime")
	}
	return reply.LocalTime, nil
}
