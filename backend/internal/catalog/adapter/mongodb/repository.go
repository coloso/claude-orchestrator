package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"orch-test/backend/internal/catalog/domain"
)

// CountersCollection holds the ID counters; the books counter is {_id: "books", seq}.
const CountersCollection = "counters"

// operationTimeout bounds every repository call, including all MongoDB round trips it makes.
// An HTTP request context ends only when the client goes away, so without it a stalled database
// would hold the request's goroutine and connection indefinitely.
const operationTimeout = 5 * time.Second

// withDeadline derives the context of one repository call from the caller's context. The driver
// turns the deadline into the server-side limit (maxTimeMS) of each command too, so the server
// stops working on it as well. The resulting error (context.DeadlineExceeded, a network timeout)
// is deliberately not a domain error: it reaches the HTTP layer unmapped and becomes a generic 500.
func withDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, operationTimeout)
}

// Repository is the MongoDB domain.Repository of the catalog. The schema must be migrated (catalog/0001).
type Repository struct {
	books    *mongo.Collection
	counters *mongo.Collection
}

var _ domain.Repository = (*Repository)(nil)

// NewRepository returns the MongoDB domain.Repository. The schema must be migrated (catalog/0001).
func NewRepository(db *mongo.Database) *Repository {
	return &Repository{
		books:    db.Collection(BooksCollection),
		counters: db.Collection(CountersCollection),
	}
}

// bookDoc is the stored form of a book. It has exactly the fields of booksSchema().
// Optional fields are absent when unset, never null.
type bookDoc struct {
	ID            int64     `bson:"_id"`
	ISBN          string    `bson:"isbn"`
	Title         string    `bson:"title"`
	Authors       []string  `bson:"authors"`
	PublishedYear *int      `bson:"publishedYear,omitempty"`
	Language      *string   `bson:"language,omitempty"`
	Pages         *int      `bson:"pages,omitempty"`
	CreatedAt     time.Time `bson:"createdAt"`
	UpdatedAt     time.Time `bson:"updatedAt"`
	Version       int64     `bson:"version"`
}

func toDoc(b domain.Book) bookDoc {
	return bookDoc{
		ID:            int64(b.ID),
		ISBN:          b.ISBN.String(),
		Title:         b.Title,
		Authors:       b.Authors,
		PublishedYear: b.PublishedYear,
		Language:      b.Language,
		Pages:         b.Pages,
		CreatedAt:     b.CreatedAt,
		UpdatedAt:     b.UpdatedAt,
		Version:       b.Version,
	}
}

func fromDoc(d bookDoc) domain.Book {
	return domain.Book{
		ID:            domain.BookID(d.ID),
		ISBN:          domain.ISBN(d.ISBN),
		Title:         d.Title,
		Authors:       d.Authors,
		PublishedYear: d.PublishedYear,
		Language:      d.Language,
		Pages:         d.Pages,
		CreatedAt:     d.CreatedAt,
		UpdatedAt:     d.UpdatedAt,
		Version:       d.Version,
	}
}

// Add stores b under the next ID from the books counter. A failed insert leaves a gap in the IDs.
func (r *Repository) Add(ctx context.Context, b domain.Book) (domain.Book, error) {
	ctx, cancel := withDeadline(ctx)
	defer cancel()
	id, err := r.nextID(ctx)
	if err != nil {
		return domain.Book{}, err
	}
	b.ID = domain.BookID(id)
	if _, err := r.books.InsertOne(ctx, toDoc(b)); err != nil {
		if isDuplicateISBN(err) {
			return domain.Book{}, domain.ErrDuplicateISBN
		}
		return domain.Book{}, fmt.Errorf("insert book: %w", err)
	}
	return b, nil
}

// nextID increments the books counter and returns the new value. Two very first calls can race
// on creating the counter document; the loser gets a duplicate key error and retries once.
func (r *Repository) nextID(ctx context.Context) (int64, error) {
	var res struct {
		Seq int64 `bson:"seq"`
	}
	for attempt := 0; ; attempt++ {
		err := r.counters.FindOneAndUpdate(ctx,
			bson.D{{Key: "_id", Value: BooksCollection}},
			bson.D{{Key: "$inc", Value: bson.D{{Key: "seq", Value: int64(1)}}}},
			options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
		).Decode(&res)
		if err == nil {
			return res.Seq, nil
		}
		if attempt == 0 && mongo.IsDuplicateKeyError(err) {
			continue
		}
		return 0, fmt.Errorf("assign book id: %w", err)
	}
}

func (r *Repository) Get(ctx context.Context, id domain.BookID) (domain.Book, error) {
	ctx, cancel := withDeadline(ctx)
	defer cancel()
	return r.findOne(ctx, bson.D{{Key: "_id", Value: int64(id)}})
}

func (r *Repository) FindByISBN(ctx context.Context, isbn domain.ISBN) (domain.Book, error) {
	ctx, cancel := withDeadline(ctx)
	defer cancel()
	return r.findOne(ctx, bson.D{{Key: "isbn", Value: isbn.String()}})
}

func (r *Repository) findOne(ctx context.Context, filter bson.D) (domain.Book, error) {
	var d bookDoc
	if err := r.books.FindOne(ctx, filter).Decode(&d); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return domain.Book{}, domain.ErrNotFound
		}
		return domain.Book{}, fmt.Errorf("find book: %w", err)
	}
	return fromDoc(d), nil
}

func (r *Repository) List(ctx context.Context, f domain.ListFilter, p domain.Page) ([]domain.Book, int64, error) {
	ctx, cancel := withDeadline(ctx)
	defer cancel()
	filter := bson.D{}
	if f.Author != "" {
		// Equality on an array field matches any element exactly (uses authors_lookup).
		filter = bson.D{{Key: "authors", Value: f.Author}}
	}
	// total is exact on purpose, also without an author filter: the API promises it and the
	// contract suite pins it, while EstimatedDocumentCount reads collection metadata and may be
	// off after an unclean shutdown. The deadline above (and the maxTimeMS the driver derives
	// from it) bounds the worst case of the exact count. Revisit with EstimatedDocumentCount for
	// the unfiltered case if the collection grows to millions of books.
	total, err := r.books.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("count books: %w", err)
	}
	cur, err := r.books.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "_id", Value: 1}}).
		SetSkip(int64(p.Offset)).
		SetLimit(int64(p.Limit)))
	if err != nil {
		return nil, 0, fmt.Errorf("list books: %w", err)
	}
	var docs []bookDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, 0, fmt.Errorf("read books: %w", err)
	}
	books := make([]domain.Book, len(docs))
	for i, d := range docs {
		books[i] = fromDoc(d)
	}
	return books, total, nil
}

// Update replaces the whole document, so an optional field that b lacks is removed. The version
// in the filter makes it safe against concurrent updates: only one of them matches.
func (r *Repository) Update(ctx context.Context, b domain.Book, expectedVersion int64) error {
	ctx, cancel := withDeadline(ctx)
	defer cancel()
	res, err := r.books.ReplaceOne(ctx,
		bson.D{{Key: "_id", Value: int64(b.ID)}, {Key: "version", Value: expectedVersion}},
		toDoc(b))
	if err != nil {
		if isDuplicateISBN(err) {
			return domain.ErrDuplicateISBN
		}
		return fmt.Errorf("update book: %w", err)
	}
	if res.MatchedCount > 0 {
		return nil
	}
	if _, err := r.Get(ctx, b.ID); err != nil {
		return err // ErrNotFound or a driver error
	}
	return domain.ErrVersionConflict
}

func (r *Repository) Remove(ctx context.Context, id domain.BookID) error {
	ctx, cancel := withDeadline(ctx)
	defer cancel()
	res, err := r.books.DeleteOne(ctx, bson.D{{Key: "_id", Value: int64(id)}})
	if err != nil {
		return fmt.Errorf("remove book: %w", err)
	}
	if res.DeletedCount == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// isDuplicateISBN reports whether err is a duplicate key error of the isbn_unique index. It reads
// the key pattern the server puts into the write error ({isbn: 1}), not the error text.
// A duplicate _id (key pattern {_id: 1}) is not one: it means the counter is behind the data.
func isDuplicateISBN(err error) bool {
	if !mongo.IsDuplicateKeyError(err) {
		return false
	}
	var we mongo.WriteException
	if !errors.As(err, &we) {
		return false
	}
	for _, e := range we.WriteErrors {
		if e.Code == 11000 && keyPatternIsISBN(e.Raw) {
			return true
		}
	}
	return false
}

// keyPatternIsISBN reports whether the server's write error says that an index on isbn alone was violated.
func keyPatternIsISBN(raw bson.Raw) bool {
	pattern, ok := raw.Lookup("keyPattern").DocumentOK()
	if !ok {
		return false
	}
	elems, err := pattern.Elements()
	if err != nil || len(elems) != 1 {
		return false
	}
	return elems[0].Key() == "isbn"
}
