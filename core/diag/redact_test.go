package diag

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type sensitiveSnapshot struct {
	SessionIndex string `json:"session_index"`
	TunnelIP     string `json:"tunnel_ip"`
	Scheduler    string `json:"scheduler"`
	RelayAddr    string `json:"relay_addr"`
	APIKey       string `json:"api_key"`
	Nested       map[string]any `json:"nested"`
}

func TestRedactSnapshotRemovesSensitiveFieldsRecursively(t *testing.T) {
	in := sensitiveSnapshot{
		SessionIndex: "deadbeef",
		TunnelIP:     "10.77.0.42",
		Scheduler:    "hol-aware",
		RelayAddr:    "203.0.113.9:51820",
		APIKey:       "super-secret-key",
		Nested: map[string]any{
			"client_secret": "shh",
			"remote_ip":     "198.51.100.7",
			"loss_pct":      2.5,
		},
	}

	got, err := RedactSnapshot(in)
	if err != nil {
		t.Fatalf("RedactSnapshot: %v", err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal redacted: %v", err)
	}
	text := string(b)
	for _, secret := range []string{"deadbeef", "10.77.0.42", "203.0.113.9", "super-secret-key", "shh", "198.51.100.7"} {
		if strings.Contains(text, secret) {
			t.Fatalf("redacted JSON leaked %q: %s", secret, text)
		}
	}
	for _, useful := range []string{"hol-aware", "loss_pct", "2.5"} {
		if !strings.Contains(text, useful) {
			t.Fatalf("redacted JSON lost useful telemetry %q: %s", useful, text)
		}
	}
}

func TestRedactSnapshotCoversNetworkIdentityNamingVariants(t *testing.T) {
	in := map[string]any{
		"host":            "relay.example.net",
		"peer_endpoint":   "198.51.100.20:51820",
		"resolver":        "1.1.1.1",
		"fallback_dns":    "8.8.8.8",
		"source_hostname": "work-laptop.local",
		"remote":          "203.0.113.9:50000",
		"scheduler":       "round-robin",
		"tx_packets":      42,
	}

	got, err := RedactSnapshot(in)
	if err != nil {
		t.Fatalf("RedactSnapshot: %v", err)
	}
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("redacted snapshot type = %T, want map[string]any", got)
	}
	for _, key := range []string{"host", "peer_endpoint", "resolver", "fallback_dns", "source_hostname", "remote"} {
		if m[key] != redactedValue {
			t.Errorf("%s = %#v, want %q", key, m[key], redactedValue)
		}
	}
	if m["scheduler"] != "round-robin" {
		t.Errorf("scheduler = %#v, want round-robin", m["scheduler"])
	}
	if gotPackets, ok := m["tx_packets"].(json.Number); !ok || gotPackets.String() != "42" {
		t.Errorf("tx_packets = %#v, want json.Number(42)", m["tx_packets"])
	}
}

func TestServerServesRedactedSnapshot(t *testing.T) {
	s := startTestServer(t, func() any {
		return sensitiveSnapshot{
			SessionIndex: "cafebabe",
			TunnelIP:     "10.77.0.8",
			Scheduler:    "weighted-goodput",
			RelayAddr:    "192.0.2.10:51820",
		}
	})

	resp, err := http.Get("http://" + s.Addr() + "/api/v1/diagnostics/redacted")
	if err != nil {
		t.Fatalf("GET redacted diagnostics: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"session_index", "tunnel_ip", "relay_addr"} {
		if got[key] != redactedValue {
			t.Errorf("%s = %#v, want %q", key, got[key], redactedValue)
		}
	}
	if got["scheduler"] != "weighted-goodput" {
		t.Errorf("scheduler = %#v, want weighted-goodput", got["scheduler"])
	}
}

func TestRedactedEndpointRejectsNonGET(t *testing.T) {
	s := startTestServer(t, func() any { return sensitiveSnapshot{} })
	resp, err := http.Post("http://"+s.Addr()+"/api/v1/diagnostics/redacted", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}
