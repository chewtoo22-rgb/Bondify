package diag

import (
	"encoding/json"
	"net/http"
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
	if cors := resp.Header.Get("Access-Control-Allow-Origin"); cors != "*" {
		t.Errorf("CORS header = %q, want *", cors)
	}

	var got fakeSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Foo != "bar" || got.N != 42 {
		t.Errorf("got %+v, want {bar 42}", got)
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

func TestServerAddrIsLoopback(t *testing.T) {
	s := startTestServer(t, func() any { return fakeSnapshot{} })
	if s.Addr() == "" {
		t.Fatal("empty addr")
	}
}
