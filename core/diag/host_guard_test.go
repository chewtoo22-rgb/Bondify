package diag

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestServerRejectsNonLoopbackHostBeforeSnapshot(t *testing.T) {
	called := 0
	s := startTestServer(t, func() any {
		called++
		return fakeSnapshot{Foo: "secret"}
	})

	for _, path := range []string{"/api/v1/diagnostics", "/api/v1/diagnostics/redacted", "/healthz"} {
		t.Run(path, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "http://"+s.Addr()+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			// Simulate DNS rebinding: the TCP connection still reaches 127.0.0.1,
			// but the browser believes attacker.example is the request origin/host.
			req.Host = "attacker.example:9090"
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", resp.StatusCode)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "secret") {
				t.Fatalf("diagnostics leaked through rejected Host: %q", body)
			}
			if cache := resp.Header.Get("Cache-Control"); cache != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", cache)
			}
		})
	}
	if called != 0 {
		t.Fatalf("snapshot provider called %d times for rejected Host, want 0", called)
	}
}

func TestServerAllowsCanonicalLoopbackHosts(t *testing.T) {
	s := startTestServer(t, func() any { return fakeSnapshot{Foo: "ok"} })
	for _, host := range []string{"localhost", "localhost:9090", "127.0.0.1", "127.0.0.1:9090", "127.42.0.9:9090", "[::1]:9090"} {
		t.Run(host, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "http://"+s.Addr()+"/healthz", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Host = host
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
		})
	}
}

func TestLoopbackRequestHostRejectsAmbiguousOrMissingHosts(t *testing.T) {
	for _, host := range []string{"", " ", "example.com", "example.com:9090", "127.0.0.1.example.com", "::1", "localhost:bad:port"} {
		t.Run(host, func(t *testing.T) {
			if isLoopbackRequestHost(host) {
				t.Fatalf("isLoopbackRequestHost(%q) = true, want false", host)
			}
		})
	}
}
