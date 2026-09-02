// Package diag serves a bonded tunnel's live statistics as JSON on a localhost-only HTTP
// endpoint -- the same numbers the CLI's stats log prints (per-path RTT/loss/throughput,
// bonded aggregate, reorder buffer occupancy), for a dashboard or any other local tool to
// poll instead of scraping log lines. See PROTOCOL.md §6's `stats` control message for the
// data this mirrors, and ARCHITECTURE.md §7: no telemetry leaves the box, so this endpoint
// must never be reachable from anywhere but the machine running it.
package diag

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

type Snapshot func() any

type Server struct {
	http *http.Server
	ln   net.Listener
}

// NewServer fail-closes diagnostics to loopback only. Live tunnel statistics are sensitive
// operational data and must never be exposed on a routable interface by accident.
func NewServer(addr string, snap Snapshot) (*Server, error) {
	if snap == nil {
		return nil, fmt.Errorf("diag: nil snapshot")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("diag: listen %s: %w", addr, err)
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !tcpAddr.IP.IsLoopback() {
		_ = ln.Close()
		return nil, fmt.Errorf("diag: diagnostics must bind to loopback, got %s", ln.Addr())
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(snap()); err != nil {
			// The response may already have started, so there is no safe status rewrite here.
			return
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	return &Server{
		http: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		ln: ln,
	}, nil
}

func (s *Server) Addr() string { return s.ln.Addr().String() }

func (s *Server) Serve() error {
	err := s.http.Serve(s.ln)
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}
