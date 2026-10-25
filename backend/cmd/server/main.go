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
	host, err := listenHostFromEnv()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	if *healthcheck {
		os.Exit(checkHealth(port))
	}
	if err := run(listenAddr(host, port)); err != nil {
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
	srv := newHTTPServer(addr, app.Handler)

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr, "database", cfg.MongoDB)
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

// newHTTPServer sets timeouts so slow or idle clients can't hold connections open forever,
// and caps request headers at 16 KiB.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
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

// listenHostFromEnv reads LISTEN_HOST (default 127.0.0.1) and rejects anything that is not an IP address.
func listenHostFromEnv() (string, error) {
	v := os.Getenv("LISTEN_HOST")
	if v == "" {
		return "127.0.0.1", nil
	}
	if net.ParseIP(v) == nil {
		return "", fmt.Errorf("LISTEN_HOST must be an IP address, got %q", v)
	}
	return v, nil
}

// listenAddr joins host and port, bracketing IPv6 hosts ("[::1]:8000").
func listenAddr(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
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
