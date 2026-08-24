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
	"net/url"
	"strings"
	"time"
)

// Snapshot is called once per request to produce the current diagnostics payload. Callers
// pass a closure over *bond.ClientTunnel.Diagnostics or *bond.Relay.Diagnostics -- this
// package deliberately has no dependency on core/bond, so it stays reusable for any future
// JSON-shaped diagnostics source (e.g. the Android/desktop shells).
type Snapshot func() any

// Server is a minimal HTTP server exposing one Snapshot as JSON plus a liveness check.
// It is not a general-purpose API: no auth, no TLS, read-only endpoints -- appropriate only
// because NewServer enforces a loopback-only listener.
type Server struct {
	http *http.Server
	ln   net.Listener
}

// NewServer builds a diagnostics server bound to addr (host:port). The resolved address must
// be loopback (127.0.0.0/8 or ::1). Diagnostics intentionally have no authentication or TLS,
// and the full endpoint contains live tunnel/session metadata, so accepting a wildcard or
// routable bind here would violate this package's local-only security boundary.
//
// Resolve once and pass that exact *net.TCPAddr to ListenTCP rather than validating a hostname
// and then asking net.Listen to resolve it a second time. That avoids a DNS-rebinding/TOCTOU
// gap between the security check and the address that is actually bound.
func NewServer(addr string, snap Snapshot) (*Server, error) {
	tcpAddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("diag: resolve %s: %w", addr, err)
	}
	if tcpAddr.IP == nil || !tcpAddr.IP.IsLoopback() {
		return nil, fmt.Errorf("diag: refusing non-loopback diagnostics address %q (resolved %s)", addr, tcpAddr)
	}
	ln, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return nil, fmt.Errorf("diag: listen %s: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/diagnostics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// The listener is loopback-only, but a remote web page can still issue browser requests
		// to 127.0.0.1. Only opt that page into reading the response when its own Origin is also
		// loopback; wildcard CORS would turn the local diagnostics endpoint into a cross-origin
		// exfiltration primitive.
		setLoopbackCORS(w, r)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(snap()); err != nil {
			log.Printf("diag: encode snapshot: %v", err)
		}
	})
	mux.HandleFunc("/api/v1/diagnostics/redacted", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		redacted, err := RedactSnapshot(snap())
		if err != nil {
			log.Printf("diag: redact snapshot: %v", err)
			http.Error(w, "diagnostics redaction failed", http.StatusInternalServerError)
			return
		}
		setLoopbackCORS(w, r)
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(redacted); err != nil {
			log.Printf("diag: encode redacted snapshot: %v", err)
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

func setLoopbackCORS(w http.ResponseWriter, r *http.Request) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return
	}
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return
	}
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Add("Vary", "Origin")
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
