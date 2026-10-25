// Package bootstrap is the composition root: the only place that knows every
// bounded context and wires its adapters, use cases and repositories together.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"

	cataloghttp "orch-test/backend/internal/catalog/adapter/httpapi"
	catalogmongo "orch-test/backend/internal/catalog/adapter/mongodb"
	catalogapp "orch-test/backend/internal/catalog/app"
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

// Config configures the application.
type Config struct {
	// MongoURI and MongoDB locate the database; both are required.
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

// ConfigFromEnv reads MONGO_URI (default mongodb://localhost:27017), MONGO_DB (default orch)
// and SEED_DEMO_DATA. SEED_DEMO_DATA is parsed with strconv.ParseBool; unset or unparsable
// means false. There is no storage setting: a STORAGE variable is ignored.
func ConfigFromEnv() Config {
	cfg := Config{
		MongoURI: os.Getenv("MONGO_URI"),
		MongoDB:  os.Getenv("MONGO_DB"),
	}
	cfg.SeedDemoData, _ = strconv.ParseBool(os.Getenv("SEED_DEMO_DATA"))
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

// redactMongoURI returns raw without credentials, query and fragment, so it is safe to log
// and to put in errors: "mongodb://alice:s3cret@db.example:27017/admin?authSource=admin"
// becomes "mongodb://db.example:27017/admin".
func redactMongoURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "(unparsable MongoDB URI)"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// New builds the application. cmd/server and the e2e tests both use it.
// It always connects to MongoDB and migrates the schema before returning, so cmd/server
// only serves HTTP after the migrations finished. If MongoDB can't be reached, the error
// names the URI without its credentials.
//
// Wiring a bounded context builds its repository on db:
//
//	h.TodoHandler = todohttp.NewTodoHandler(todoapp.NewService(todomongodb.NewRepository(db)))
func New(ctx context.Context, cfg Config) (*App, error) {
	if cfg.MongoURI == "" || cfg.MongoDB == "" {
		return nil, errors.New("bootstrap: MongoURI and MongoDB are required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	app := &App{}
	h := &handler.Handler{}

	client, err := mongodb.Connect(ctx, cfg.MongoURI)
	if err != nil {
		return nil, fmt.Errorf("connect to MongoDB at %s: %w", redactMongoURI(cfg.MongoURI), err)
	}
	app.closers = append(app.closers, client.Disconnect)
	db := client.Database(cfg.MongoDB)
	if err := mongodb.Migrate(ctx, db, migrationSets(), mongodb.MigrateOptions{Logger: logger}); err != nil {
		_ = app.Close(ctx)
		return nil, fmt.Errorf("migrate database schema: %w", err)
	}
	h.Ping = func(ctx context.Context) error { return mongodb.Ping(ctx, client) }

	catalogSvc := catalogapp.NewService(catalogmongo.NewRepository(db))
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
