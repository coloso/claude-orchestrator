package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"orch-test/backend/internal/bootstrap"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "check /api/health on the local server and exit (for container health checks)")
	flag.Parse()

	port, err := portFromEnv()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	if *healthcheck {
		os.Exit(checkHealth(port))
	}
	if err := run(fmt.Sprintf(":%d", port)); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func run(addr string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := bootstrap.ConfigFromEnv()
	app, err := bootstrap.New(ctx, cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: addr, Handler: app.Handler, ReadHeaderTimeout: 5 * time.Second}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr, "storage", cfg.Storage)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	slog.Info("shutting down")
	err = srv.Shutdown(shutdownCtx)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return errors.Join(err, app.Close(shutdownCtx))
}

// portFromEnv reads PORT (default 8000) and rejects anything that is not a port number.
func portFromEnv() (int, error) {
	v := os.Getenv("PORT")
	if v == "" {
		return 8000, nil
	}
	port, err := strconv.Atoi(v)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("PORT must be a number between 1 and 65535, got %q", v)
	}
	return port, nil
}

// checkHealth queries the local server only; the port is the sole variable part.
func checkHealth(port int) int {
	u := url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Path: "/api/health"}
	client := http.Client{Timeout: 3 * time.Second}
	res, err := client.Get(u.String())
	if err != nil {
		return 1
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
