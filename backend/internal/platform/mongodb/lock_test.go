package mongodb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

const lockTestLease = 30 * time.Second

// lockTestDoc is the stored schema lock document, as the tests read it.
type lockTestDoc struct {
	ID        string    `bson:"_id"`
	Owner     string    `bson:"owner"`
	ExpiresAt time.Time `bson:"expiresAt"`
}

// readLockTestDoc returns the schema lock document, and false if there is none.
func readLockTestDoc(t *testing.T, db *mongo.Database) (lockTestDoc, bool) {
	t.Helper()
	var doc lockTestDoc
	err := db.Collection(LockCollection).FindOne(t.Context(), bson.D{{Key: "_id", Value: "schema"}}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return lockTestDoc{}, false
	}
	if err != nil {
		t.Fatalf("read schema lock: %v", err)
	}
	return doc, true
}

// expireLockForTest moves the lease of the current holder into the past, as if it had run out.
func expireLockForTest(t *testing.T, db *mongo.Database) {
	t.Helper()
	res, err := db.Collection(LockCollection).UpdateOne(t.Context(),
		bson.D{{Key: "_id", Value: "schema"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "expiresAt", Value: time.Now().Add(-time.Minute)}}}})
	if err != nil {
		t.Fatalf("expire schema lock: %v", err)
	}
	if res.MatchedCount != 1 {
		t.Fatal("expire schema lock: no lock document")
	}
}

func mustTryAcquireLock(t *testing.T, l *schemaLock, want bool) {
	t.Helper()
	got, err := l.tryAcquire(t.Context())
	if err != nil {
		t.Fatalf("tryAcquire(%s): %v", l.owner, err)
	}
	if got != want {
		t.Fatalf("tryAcquire(%s) = %v, want %v", l.owner, got, want)
	}
}

func assertLockOwner(t *testing.T, db *mongo.Database, want string) lockTestDoc {
	t.Helper()
	doc, ok := readLockTestDoc(t, db)
	if !ok {
		t.Fatalf("no schema lock document, want one owned by %s", want)
	}
	if doc.Owner != want {
		t.Fatalf("schema lock owner = %q, want %q", doc.Owner, want)
	}
	return doc
}

func TestSchemaLock_AcquiresFreeLock(t *testing.T) {
	db := newTestDB(t)
	l := newSchemaLock(db, "instance-a", lockTestLease)

	before := time.Now()
	mustTryAcquireLock(t, l, true)

	doc := assertLockOwner(t, db, "instance-a")
	// The server sets expiresAt; allow for some clock difference between client and server.
	if doc.ExpiresAt.Before(before.Add(lockTestLease-5*time.Second)) ||
		doc.ExpiresAt.After(time.Now().Add(lockTestLease+5*time.Second)) {
		t.Fatalf("expiresAt = %v, want about now + %v", doc.ExpiresAt, lockTestLease)
	}
}

func TestSchemaLock_SecondOwnerCannotAcquireHeldLock(t *testing.T) {
	db := newTestDB(t)
	a := newSchemaLock(db, "instance-a", lockTestLease)
	b := newSchemaLock(db, "instance-b", lockTestLease)

	mustTryAcquireLock(t, a, true)
	mustTryAcquireLock(t, b, false)

	assertLockOwner(t, db, "instance-a")
}

func TestSchemaLock_SameOwnerReacquires(t *testing.T) {
	db := newTestDB(t)

	mustTryAcquireLock(t, newSchemaLock(db, "instance-a", lockTestLease), true)
	mustTryAcquireLock(t, newSchemaLock(db, "instance-a", lockTestLease), true)

	assertLockOwner(t, db, "instance-a")
}

func TestSchemaLock_RenewExtendsExpiry(t *testing.T) {
	db := newTestDB(t)
	l := newSchemaLock(db, "instance-a", lockTestLease)
	mustTryAcquireLock(t, l, true)
	first := assertLockOwner(t, db, "instance-a")

	time.Sleep(50 * time.Millisecond)
	if err := l.renew(t.Context()); err != nil {
		t.Fatalf("renew: %v", err)
	}

	renewed := assertLockOwner(t, db, "instance-a")
	if !renewed.ExpiresAt.After(first.ExpiresAt) {
		t.Fatalf("expiresAt after renew = %v, want later than %v", renewed.ExpiresAt, first.ExpiresAt)
	}
}

func TestSchemaLock_RenewAfterTakeoverReturnsErrSchemaLockLost(t *testing.T) {
	db := newTestDB(t)
	a := newSchemaLock(db, "instance-a", lockTestLease)
	b := newSchemaLock(db, "instance-b", lockTestLease)
	mustTryAcquireLock(t, a, true)

	expireLockForTest(t, db)
	mustTryAcquireLock(t, b, true)

	if err := a.renew(t.Context()); !errors.Is(err, ErrSchemaLockLost) {
		t.Fatalf("renew by previous owner = %v, want ErrSchemaLockLost", err)
	}
	assertLockOwner(t, db, "instance-b")
}

func TestSchemaLock_ReleaseLetsAnotherOwnerAcquire(t *testing.T) {
	db := newTestDB(t)
	a := newSchemaLock(db, "instance-a", lockTestLease)
	b := newSchemaLock(db, "instance-b", lockTestLease)
	mustTryAcquireLock(t, a, true)

	if err := a.release(t.Context()); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, ok := readLockTestDoc(t, db); ok {
		t.Fatal("schema lock document still exists after release")
	}
	if err := a.release(t.Context()); err != nil {
		t.Fatalf("second release: %v, want nil (idempotent)", err)
	}
	if err := a.renew(t.Context()); !errors.Is(err, ErrSchemaLockLost) {
		t.Fatalf("renew after release = %v, want ErrSchemaLockLost", err)
	}

	mustTryAcquireLock(t, b, true)
	assertLockOwner(t, db, "instance-b")
}

func TestSchemaLock_ReleaseByNonOwnerDeletesNothing(t *testing.T) {
	db := newTestDB(t)
	a := newSchemaLock(db, "instance-a", lockTestLease)
	b := newSchemaLock(db, "instance-b", lockTestLease)
	mustTryAcquireLock(t, a, true)

	if err := b.release(t.Context()); err != nil {
		t.Fatalf("release by non-owner: %v", err)
	}

	assertLockOwner(t, db, "instance-a")
	mustTryAcquireLock(t, b, false)
}

func TestSchemaLock_TakesOverExpiredLockOfDeadInstance(t *testing.T) {
	db := newTestDB(t)
	dead := lockTestDoc{ID: "schema", Owner: "dead-instance", ExpiresAt: time.Now().Add(-time.Minute)}
	if _, err := db.Collection(LockCollection).InsertOne(t.Context(), dead); err != nil {
		t.Fatalf("insert lock of dead instance: %v", err)
	}

	mustTryAcquireLock(t, newSchemaLock(db, "instance-a", lockTestLease), true)

	doc := assertLockOwner(t, db, "instance-a")
	if !doc.ExpiresAt.After(time.Now()) {
		t.Fatalf("expiresAt = %v, want in the future", doc.ExpiresAt)
	}
}

func TestSchemaLock_AcquireWaitsForShortLeaseToExpire(t *testing.T) {
	db := newTestDB(t)
	const shortLease = 500 * time.Millisecond
	mustTryAcquireLock(t, newSchemaLock(db, "instance-a", shortLease), true)
	waiter := newSchemaLock(db, "instance-b", lockTestLease)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := waiter.acquire(ctx, 25*time.Millisecond); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	if waited := time.Since(start); waited < shortLease/2 {
		t.Fatalf("acquire returned after %v, want it to wait for the %v lease to expire", waited, shortLease)
	}
	assertLockOwner(t, db, "instance-b")
}

func TestSchemaLock_AcquireReturnsWhenContextCancelled(t *testing.T) {
	db := newTestDB(t)
	mustTryAcquireLock(t, newSchemaLock(db, "instance-a", lockTestLease), true)
	waiter := newSchemaLock(db, "instance-b", lockTestLease)

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err := waiter.acquire(ctx, 25*time.Millisecond)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire = %v, want context.Canceled", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("acquire returned %v after cancellation, want promptly", waited)
	}
	assertLockOwner(t, db, "instance-a")
}

func TestSchemaLock_ConcurrentTryAcquireHasOneWinner(t *testing.T) {
	db := newTestDB(t)
	const racers = 10

	start := make(chan struct{})
	type lockRaceResult struct {
		owner string
		won   bool
		err   error
	}
	results := make(chan lockRaceResult, racers)
	var wg sync.WaitGroup
	for i := range racers {
		l := newSchemaLock(db, fmt.Sprintf("racer-%d", i), lockTestLease)
		wg.Go(func() {
			<-start
			won, err := l.tryAcquire(t.Context())
			results <- lockRaceResult{owner: l.owner, won: won, err: err}
		})
	}
	close(start)
	wg.Wait()
	close(results)

	var winners []string
	for r := range results {
		if r.err != nil {
			t.Errorf("tryAcquire(%s): %v", r.owner, r.err)
		}
		if r.won {
			winners = append(winners, r.owner)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("winners = %v, want exactly one", winners)
	}
	assertLockOwner(t, db, winners[0])
}
