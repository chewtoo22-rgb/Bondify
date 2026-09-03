package diag

import (
    "encoding/json"
    "io"
    "net/http"
    "testing"
    "time"
)

func TestDiagnosticsEndpointGETOnlyAndJSON(t *testing.T) {
    srv, err := NewServer("127.0.0.1:0", func() any {
        return map[string]any{"paths": 2, "healthy": true}
    })
    if err != nil { t.Fatal(err) }
    defer srv.Close()
    go func() { _ = srv.Serve() }()
    time.Sleep(10 * time.Millisecond)

    resp, err := http.Get("http://" + srv.Addr() + "/api/v1/diagnostics")
    if err != nil { t.Fatal(err) }
    defer resp.Body.Close()
    if resp.StatusCode != http.StatusOK { t.Fatalf("status=%d", resp.StatusCode) }
    if got := resp.Header.Get("Content-Type"); got != "application/json" { t.Fatalf("content-type=%q", got) }
    var payload map[string]any
    if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil { t.Fatal(err) }
    if payload["healthy"] != true { t.Fatalf("payload=%v", payload) }

    req, _ := http.NewRequest(http.MethodPost, "http://"+srv.Addr()+"/api/v1/diagnostics", nil)
    denied, err := http.DefaultClient.Do(req)
    if err != nil { t.Fatal(err) }
    denied.Body.Close()
    if denied.StatusCode != http.StatusMethodNotAllowed { t.Fatalf("post status=%d", denied.StatusCode) }
}

func TestHealthEndpointGETOnly(t *testing.T) {
    srv, err := NewServer("127.0.0.1:0", func() any { return map[string]any{} })
    if err != nil { t.Fatal(err) }
    defer srv.Close()
    go func() { _ = srv.Serve() }()
    time.Sleep(10 * time.Millisecond)

    resp, err := http.Get("http://" + srv.Addr() + "/healthz")
    if err != nil { t.Fatal(err) }
    body, _ := io.ReadAll(resp.Body)
    resp.Body.Close()
    if resp.StatusCode != http.StatusOK || string(body) != "ok" { t.Fatalf("health=%d %q", resp.StatusCode, body) }
}
