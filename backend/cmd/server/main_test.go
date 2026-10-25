package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestNewHTTPServerSetsTimeoutsAndHeaderLimit(t *testing.T) {
	srv := newHTTPServer(":8000", http.NotFoundHandler())

	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 5s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 10*time.Second {
		t.Errorf("ReadTimeout = %v, want 10s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 15*time.Second {
		t.Errorf("WriteTimeout = %v, want 15s", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Errorf("IdleTimeout = %v, want 60s", srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 16<<10 {
		t.Errorf("MaxHeaderBytes = %d, want %d", srv.MaxHeaderBytes, 16<<10)
	}
}

func TestListenHostFromEnv(t *testing.T) {
	tests := []struct {
		name    string
		value   *string // nil means LISTEN_HOST is not set
		want    string
		wantErr bool
	}{
		{name: "unset defaults to loopback", want: "127.0.0.1"},
		{name: "empty defaults to loopback", value: ptr(""), want: "127.0.0.1"},
		{name: "IPv4 any address", value: ptr("0.0.0.0"), want: "0.0.0.0"},
		{name: "IPv4 address", value: ptr("192.168.1.10"), want: "192.168.1.10"},
		{name: "IPv6 loopback", value: ptr("::1"), want: "::1"},
		{name: "IPv6 any address", value: ptr("::"), want: "::"},
		{name: "hostname is rejected", value: ptr("localhost"), wantErr: true},
		{name: "address with port is rejected", value: ptr("127.0.0.1:8000"), wantErr: true},
		{name: "bracketed IPv6 is rejected", value: ptr("[::1]"), wantErr: true},
		{name: "trailing space is rejected", value: ptr("127.0.0.1 "), wantErr: true},
		{name: "garbage is rejected", value: ptr("not-an-ip"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value != nil {
				t.Setenv("LISTEN_HOST", *tt.value)
			} else {
				t.Setenv("LISTEN_HOST", "")
			}
			got, err := listenHostFromEnv()
			if (err != nil) != tt.wantErr {
				t.Fatalf("listenHostFromEnv() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("listenHostFromEnv() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestListenAddrBracketsIPv6(t *testing.T) {
	tests := []struct {
		host string
		port int
		want string
	}{
		{host: "127.0.0.1", port: 8000, want: "127.0.0.1:8000"},
		{host: "0.0.0.0", port: 9000, want: "0.0.0.0:9000"},
		{host: "::1", port: 8000, want: "[::1]:8000"},
		{host: "::", port: 8000, want: "[::]:8000"},
	}
	for _, tt := range tests {
		if got := listenAddr(tt.host, tt.port); got != tt.want {
			t.Errorf("listenAddr(%q, %d) = %q, want %q", tt.host, tt.port, got, tt.want)
		}
	}
}

func TestCheckHealthDialsLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	defer srv.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	if got := checkHealth(port); got != 0 {
		t.Errorf("checkHealth(%d) = %d, want 0 for a healthy server on 127.0.0.1", port, got)
	}
}

func TestCheckHealthFailsWhenNothingListens(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if got := checkHealth(port); got != 1 {
		t.Errorf("checkHealth(%d) = %d, want 1 when nothing listens", port, got)
	}
}

func TestCheckHealthFailsOnNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	if got := checkHealth(port); got != 1 {
		t.Errorf("checkHealth(%d) = %d, want 1 for a 503 response", port, got)
	}
}

func ptr(s string) *string { return &s }
