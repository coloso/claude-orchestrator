package app_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"orch-test/backend/internal/catalog/app"
	"orch-test/backend/internal/catalog/domain"
)

func demoISBNs(t *testing.T) []domain.ISBN {
	t.Helper()
	var out []domain.ISBN
	for _, d := range domain.DemoCatalog() {
		isbn, err := domain.ParseISBN(d.ISBN)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, isbn)
	}
	return out
}

func TestSeedDemo_EmptyCatalog(t *testing.T) {
	svc, _ := newService(t)
	rep, err := svc.SeedDemo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Seeded) != 12 || len(rep.Skipped) != 0 {
		t.Fatalf("seeded %d, skipped %d; want 12, 0", len(rep.Seeded), len(rep.Skipped))
	}
	want := demoISBNs(t)
	for i, b := range rep.Seeded {
		if b.ISBN != want[i] {
			t.Errorf("Seeded[%d].ISBN = %s, want %s", i, b.ISBN, want[i])
		}
		if b.ID <= 0 || (i > 0 && b.ID <= rep.Seeded[i-1].ID) {
			t.Errorf("Seeded[%d].ID = %d, not ascending", i, b.ID)
		}
	}
}

func TestSeedDemo_Idempotent(t *testing.T) {
	svc, _ := newService(t)
	if _, err := svc.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	rep, err := svc.SeedDemo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Seeded) != 0 || !reflect.DeepEqual(rep.Skipped, demoISBNs(t)) {
		t.Fatalf("report = %+v, want nothing seeded and all skipped", rep)
	}
	page, err := svc.ListBooks(ctx, app.ListQuery{Limit: 100})
	if err != nil || page.Total != 12 {
		t.Fatalf("total = %d, err = %v; want 12", page.Total, err)
	}
}

func TestSeedDemo_RestoresRemovedBook(t *testing.T) {
	svc, _ := newService(t)
	first, err := svc.SeedDemo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	removed := first.Seeded[4]
	if err := svc.RemoveBook(ctx, removed.ID); err != nil {
		t.Fatal(err)
	}
	rep, err := svc.SeedDemo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Seeded) != 1 || len(rep.Skipped) != 11 || rep.Seeded[0].ISBN != removed.ISBN {
		t.Fatalf("report = %+v", rep)
	}
}

func TestSeedDemo_LeavesRevisedBookAlone(t *testing.T) {
	svc, _ := newService(t)
	first, err := svc.SeedDemo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := first.Seeded[0]
	d := domain.DemoCatalog()[0]
	d.Title = "My own title"
	revised, err := svc.ReviseBook(ctx, b.ID, d, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := svc.SeedDemo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Seeded) != 0 || len(rep.Skipped) != 12 {
		t.Fatalf("report = %+v", rep)
	}
	got, err := svc.GetBook(ctx, b.ID)
	if err != nil || got.Title != "My own title" || got.Version != revised.Version {
		t.Fatalf("book = %+v, err = %v", got, err)
	}
}

func TestSeedDemo_SeedsAgainWhenEditChangedISBN(t *testing.T) {
	svc, _ := newService(t)
	first, err := svc.SeedDemo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := first.Seeded[0]
	d := domain.DemoCatalog()[0]
	d.ISBN = "9780306406157" // valid, not in the demo catalog
	if _, err := svc.ReviseBook(ctx, b.ID, d, b.Version); err != nil {
		t.Fatal(err)
	}
	rep, err := svc.SeedDemo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Seeded) != 1 || rep.Seeded[0].ISBN != b.ISBN {
		t.Fatalf("report = %+v", rep)
	}
}

// addErrRepo reports every book as missing and fails Add with err.
type addErrRepo struct {
	domain.Repository
	err error
}

func (r addErrRepo) FindByISBN(context.Context, domain.ISBN) (domain.Book, error) {
	return domain.Book{}, domain.ErrNotFound
}

func (r addErrRepo) Add(context.Context, domain.Book) (domain.Book, error) {
	return domain.Book{}, r.err
}

func TestSeedDemo_AddDuplicateIsSkipped(t *testing.T) {
	svc := app.NewService(addErrRepo{newFakeRepository(), domain.ErrDuplicateISBN})
	rep, err := svc.SeedDemo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Seeded) != 0 || len(rep.Skipped) != 12 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestSeedDemo_AddErrorStopsSeeding(t *testing.T) {
	boom := errors.New("boom")
	svc := app.NewService(addErrRepo{newFakeRepository(), boom})
	rep, err := svc.SeedDemo(ctx)
	first := demoISBNs(t)[0]
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), first.String()) {
		t.Fatalf("err = %v, want boom mentioning %s", err, first)
	}
	if len(rep.Seeded) != 0 || len(rep.Skipped) != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

// findErrRepo fails FindByISBN with err.
type findErrRepo struct {
	domain.Repository
	err error
}

func (r findErrRepo) FindByISBN(context.Context, domain.ISBN) (domain.Book, error) {
	return domain.Book{}, r.err
}

func TestSeedDemo_FindErrorIsReturned(t *testing.T) {
	boom := errors.New("db down")
	svc := app.NewService(findErrRepo{newFakeRepository(), boom})
	_, err := svc.SeedDemo(ctx)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), demoISBNs(t)[0].String()) {
		t.Fatalf("err = %v", err)
	}
}
