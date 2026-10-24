// Package bootstrap is the composition root: the only place that knows every
// bounded context and wires its adapters, use cases and repositories together.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"orch-test/backend/internal/handler"
	"orch-test/backend/internal/platform/mongodb"
	"orch-test/backend/internal/server"
)

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
}

// ConfigFromEnv reads STORAGE, MONGO_URI and MONGO_DB.
func ConfigFromEnv() Config {
	cfg := Config{
		Storage:  Storage(os.Getenv("STORAGE")),
		MongoURI: os.Getenv("MONGO_URI"),
		MongoDB:  os.Getenv("MONGO_DB"),
	}
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
//
// Wiring a bounded context picks its repository from the configured storage:
//
//	var todoRepo tododomain.Repository = todomemory.NewRepository()
//	if db != nil {
//		todoRepo = todomongodb.NewRepository(db)
//	}
//	h.TodoHandler = todohttp.NewTodoHandler(todoapp.NewService(todoRepo))
func New(ctx context.Context, cfg Config) (*App, error) {
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
		h.Ping = func(ctx context.Context) error { return mongodb.Ping(ctx, client) }
	default:
		return nil, fmt.Errorf("unknown STORAGE %q (want memory or mongo)", cfg.Storage)
	}
	_ = db // used by the bounded contexts' mongo repositories, see the example above

	srv, err := server.New(h)
	if err != nil {
		_ = app.Close(ctx)
		return nil, err
	}
	app.Handler = srv
	return app, nil
}
