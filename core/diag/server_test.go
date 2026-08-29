package diag

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

type fakeSnapshot struct {
	Foo string `json:"foo"`
	N   int    `json:"n"`
}

func startTestServer(t *testing.T, snap Snapshot) *Server {
	t.Helper()
	s, err := NewServer("127.0.0.1:0", snap)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() {
		if err := s.Serve(); err != nil {
			t.Logf("serve: %v", err)
		}
	}()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestServerServesSnapshotJSON(t *testing.T) {
	s := startTestServer(t, func() any { return fakeSnapshot{Foo: "bar", N: 42} })

	resp, err := http.Get("http://" + s.Addr() + "/api/v1/diagnostics")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if cache := resp.Header.Get("Cache-Control"); cache != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cache)
	}
	if nosniff := resp.Header.Get("X-Content-Type-Options"); nosniff != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", nosniff)
	}
	if cors := resp.Header.Get("Access-Control-Allow-Origin"); cors != "" {
		t.Errorf("CORS header = %q without Origin request header, want empty", cors)
	}

	var got fakeSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Foo != "bar" || got.N != 42 {
		t.Errorf("got %+v, want {bar 42}", got)
	}
}

func TestServerCORSRejectsRemoteWebOrigin(t *testing.T) {
	s := startTestServer(t, func() any { return fakeSnapshot{Foo: "secret"} })
	req, err := http.NewRequest(http.MethodGet, "http://"+s.Addr()+"/api/v1/diagnostics", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://attacker.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if cors := resp.Header.Get("Access-Control-Allow-Origin"); cors != "" {
		t.Fatalf("remote Origin received CORS permission %q", cors)
	}
}

func TestServerCORSAllowsLoopbackDashboardOrigin(t *testing.T) {
	s := startTestServer(t, func() any { return fakeSnapshot{} })
	for _, origin := range []string{"http://localhost:3000", "http://127.0.0.1:3000", "https://[::1]:3000"} {
		t.Run(origin, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "http://"+s.Addr()+"/api/v1/diagnostics/redacted", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Origin", origin)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if cors := resp.Header.Get("Access-Control-Allow-Origin"); cors != origin {
				t.Fatalf("CORS header = %q, want %q", cors, origin)
			}
			if vary := resp.Header.Get("Vary"); vary != "Origin" {
				t.Fatalf("Vary = %q, want Origin", vary)
			}
		})
	}
}

func TestServerRejectsNonGET(t *testing.T) {
	s := startTestServer(t, func() any { return fakeSnapshot{} })

	resp, err := http.Post("http://"+s.Addr()+"/api/v1/diagnostics", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodGet {
		t.Errorf("Allow = %q, want GET", allow)
	}
}

func TestServerHealthz(t *testing.T) {
	s := startTestServer(t, func() any { return fakeSnapshot{} })

	resp, err := http.Get("http://" + s.Addr() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if cache := resp.Header.Get("Cache-Control"); cache != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cache)
	}
}

func TestServerHealthzRejectsNonGET(t *testing.T) {
	s := startTestServer(t, func() any { return fakeSnapshot{} })
	req, err := http.NewRequest(http.MethodPost, "http://"+s.Addr()+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
	if allow := resp.Header.Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", allow)
	}
}

func TestServerSnapshotCalledPerRequest(t *testing.T) {
	n := 0
	s := startTestServer(t, func() any {
		n++
		return fakeSnapshot{N: n}
	})

	for want := 1; want <= 3; want++ {
		resp, err := http.Get("http://" + s.Addr() + "/api/v1/diagnostics")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		var got fakeSnapshot
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		_ = resp.Body.Close()
		if got.N != want {
			t.Errorf("request %d: N = %d, want %d", want, got.N, want)
		}
	}
}

func TestServerSnapshotPanicReturnsBoundedError(t *testing.T) {
	s := startTestServer(t, func() any { panic("boom") })
	resp, err := http.Get("http://" + s.Addr() + "/api/v1/diagnostics")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "boom") {
		t.Fatalf("panic detail leaked in response: %q", body)
	}
}

func TestServerRejectsOversizedSnapshotBeforeWritingJSON(t *testing.T) {
	s := startTestServer(t, func() any {
		return map[string]string{"payload": strings.Repeat("x", maxSnapshotResponseBytes)}
	})
	resp, err := http.Get("http://" + s.Addr() + "/api/v1/diagnostics")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) >= maxSnapshotResponseBytes {
		t.Fatalf("oversized response leaked %d bytes", len(body))
	}
	if strings.Contains(string(body), strings.Repeat("x", 64)) {
		t.Fatal("oversized snapshot content leaked in error response")
	}
}

func TestServerAddrIsLoopback(t *testing.T) {
	s := startTestServer(t, func() any { return fakeSnapshot{} })
	tcpAddr, err := net.ResolveTCPAddr("tcp", s.Addr())
	if err != nil {
		t.Fatalf("ResolveTCPAddr: %v", err)
	}
	if tcpAddr.IP == nil || !tcpAddr.IP.IsLoopback() {
		t.Fatalf("server bound %q, want loopback", s.Addr())
	}
}

func TestServerRejectsNonLoopbackBind(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "[::]:0"} {
		t.Run(addr, func(t *testing.T) {
			s, err := NewServer(addr, func() any { return fakeSnapshot{} })
			if err == nil {
				_ = s.Close()
				t.Fatalf("NewServer(%q) succeeded; diagnostics must remain loopback-only", addr)
			}
		})
	}
}

func TestServerAcceptsLocalhostName(t *testing.T) {
	s, err := NewServer("localhost:0", func() any { return fakeSnapshot{} })
	if err != nil {
		t.Fatalf("NewServer(localhost): %v", err)
	}
	defer func() { _ = s.Close() }()

	tcpAddr, err := net.ResolveTCPAddr("tcp", s.Addr())
	if err != nil {
		t.Fatalf("ResolveTCPAddr: %v", err)
	}
	if tcpAddr.IP == nil || !tcpAddr.IP.IsLoopback() {
		t.Fatalf("localhost resolved/bound %q, want loopback", s.Addr())
	}
}
