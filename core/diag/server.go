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
	"log"
	"net"
	"net/http"
	"time"
)

// Snapshot is called once per request to produce the current diagnostics payload. Callers
// pass a closure over *bond.ClientTunnel.Diagnostics or *bond.Relay.Diagnostics -- this
// package deliberately has no dependency on core/bond, so it stays reusable for any future
// JSON-shaped diagnostics source (e.g. the Android/desktop shells).
type Snapshot func() any

// Server is a minimal HTTP server exposing one Snapshot as JSON plus a liveness check.
// It is not a general-purpose API: no auth, no TLS, one read-only endpoint -- appropriate
// only because it is meant to bind to loopback only, never a routable address.
type Server struct {
	http *http.Server
	ln   net.Listener
}

// NewServer builds a diagnostics server bound to addr (host:port). addr should resolve to
// a loopback address (127.0.0.1/::1) -- binding anywhere else would expose live network
// statistics, including relay-side per-client path addresses, to whatever network that
// address is reachable from. NewServer does not silently rewrite a non-loopback addr; it
// logs a loud warning instead, since a caller may have a deliberate reason (e.g. a
// container's own isolated network namespace) and refusing outright would be presumptuous.
func NewServer(addr string, snap Snapshot) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("diag: listen %s: %w", addr, err)
	}
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok && !tcpAddr.IP.IsLoopback() {
		log.Printf("diag: WARNING: diagnostics endpoint bound to non-loopback address %s -- "+
			"live tunnel statistics (including per-path remote addresses) are reachable from "+
			"anywhere that can route to it. Bind to 127.0.0.1/::1 unless you specifically "+
			"intend this.", ln.Addr())
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// Permissive CORS: this endpoint only ever listens on loopback, so any page able to
		// reach it is already running on the same machine -- no cross-origin risk to guard
		// against, and a browser-based local dashboard needs the header to fetch() it at all.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(snap()); err != nil {
			log.Printf("diag: encode snapshot: %v", err)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
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

// Addr returns the address actually bound (useful when addr was passed as "127.0.0.1:0").
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Serve blocks, serving requests until Close is called. Run it in its own goroutine.
func (s *Server) Serve() error {
	err := s.http.Serve(s.ln)
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Close shuts the server down.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}
