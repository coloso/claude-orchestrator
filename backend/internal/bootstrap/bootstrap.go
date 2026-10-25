// Package bootstrap is the composition root: the only place that knows every
// bounded context and wires its adapters, use cases and repositories together.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"strconv"

	"go.mongodb.org/mongo-driver/v2/mongo"

	cataloghttp "orch-test/backend/internal/catalog/adapter/httpapi"
	catalogmemory "orch-test/backend/internal/catalog/adapter/memory"
	catalogmongo "orch-test/backend/internal/catalog/adapter/mongodb"
	catalogapp "orch-test/backend/internal/catalog/app"
	catalogdomain "orch-test/backend/internal/catalog/domain"
	"orch-test/backend/internal/handler"
	"orch-test/backend/internal/middleware"
	"orch-test/backend/internal/platform/mongodb"
	"orch-test/backend/internal/server"
)

// migrationSets lists every bounded context's migration set. Register new contexts here.
func migrationSets() [][]mongodb.Migration {
	return [][]mongodb.Migration{
		catalogmongo.Migrations(),
	}
}

// Storage selects the driven adapters for all repositories.
type Storage string

const (
	StorageMemory Storage = "memory" // default; used by tests
	StorageMongo  Storage = "mongo"
)

// Config configures the application.
type Config struct {
	Storage  Storage
	MongoURI string
	MongoDB  string

	// SeedDemoData adds the demo catalog at startup; books already present are skipped.
	SeedDemoData bool
	// RateLimit sets the per-client rate limits; the zero value means
	// middleware.DefaultRateLimitConfig. Any other value is used as given and must be complete.
	RateLimit middleware.RateLimitConfig
	// Logger receives startup logs; nil means slog.Default().
	Logger *slog.Logger
}

// ConfigFromEnv reads STORAGE, MONGO_URI, MONGO_DB and SEED_DEMO_DATA.
// SEED_DEMO_DATA is parsed with strconv.ParseBool; unset or unparsable means false.
func ConfigFromEnv() Config {
	cfg := Config{
		Storage:  Storage(os.Getenv("STORAGE")),
		MongoURI: os.Getenv("MONGO_URI"),
		MongoDB:  os.Getenv("MONGO_DB"),
	}
	cfg.SeedDemoData, _ = strconv.ParseBool(os.Getenv("SEED_DEMO_DATA"))
	if cfg.Storage == "" {
		cfg.Storage = StorageMemory
	}
	if cfg.MongoURI == "" {
		cfg.MongoURI = "mongodb://localhost:27017"
	}
	if cfg.MongoDB == "" {
		cfg.MongoDB = "orch"
	}
	return cfg
}

// App is the fully wired application.
type App struct {
	Handler http.Handler
	closers []func(context.Context) error
}

// Close releases the application's resources (database connections).
func (a *App) Close(ctx context.Context) error {
	var errs []error
	for _, c := range a.closers {
		errs = append(errs, c(ctx))
	}
	return errors.Join(errs...)
}

// New builds the application. cmd/server and the e2e tests both use it.
// With STORAGE=mongo it migrates the schema before returning, so cmd/server
// only serves HTTP after the migrations finished. STORAGE=memory migrates nothing.
//
// Wiring a bounded context picks its repository from the configured storage:
//
//	var todoRepo tododomain.Repository = todomemory.NewRepository()
//	if db != nil {
//		todoRepo = todomongodb.NewRepository(db)
//	}
//	h.TodoHandler = todohttp.NewTodoHandler(todoapp.NewService(todoRepo))
func New(ctx context.Context, cfg Config) (*App, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	app := &App{}
	h := &handler.Handler{}

	var db *mongo.Database // nil unless Storage is mongo
	switch cfg.Storage {
	case StorageMemory, "":
	case StorageMongo:
		client, err := mongodb.Connect(ctx, cfg.MongoURI)
		if err != nil {
			return nil, err
		}
		app.closers = append(app.closers, client.Disconnect)
		db = client.Database(cfg.MongoDB)
		if err := mongodb.Migrate(ctx, db, migrationSets(), mongodb.MigrateOptions{Logger: logger}); err != nil {
			_ = app.Close(ctx)
			return nil, fmt.Errorf("migrate database schema: %w", err)
		}
		h.Ping = func(ctx context.Context) error { return mongodb.Ping(ctx, client) }
	default:
		return nil, fmt.Errorf("unknown STORAGE %q (want memory or mongo)", cfg.Storage)
	}

	var catalogRepo catalogdomain.Repository = catalogmemory.NewRepository()
	if db != nil {
		catalogRepo = catalogmongo.NewRepository(db)
	}
	catalogSvc := catalogapp.NewService(catalogRepo)
	h.CatalogHandler = cataloghttp.NewCatalogHandler(catalogSvc)

	if cfg.SeedDemoData {
		report, err := catalogSvc.SeedDemo(ctx)
		if err != nil {
			_ = app.Close(ctx)
			return nil, fmt.Errorf("seed demo data: %w", err)
		}
		for _, b := range report.Seeded {
			logger.DebugContext(ctx, "seeded demo book", "id", int64(b.ID), "isbn", b.ISBN.String(), "title", b.Title)
		}
		logger.InfoContext(ctx, fmt.Sprintf("seeded %d demo books, skipped %d", len(report.Seeded), len(report.Skipped)))
	}

	limits := cfg.RateLimit
	if reflect.ValueOf(limits).IsZero() {
		limits = middleware.DefaultRateLimitConfig()
	}
	srv, err := server.New(h, server.WithRateLimit(limits))
	if err != nil {
		_ = app.Close(ctx)
		return nil, err
	}
	app.Handler = srv
	return app, nil
}
