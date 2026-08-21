package diag

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const redactedValue = "[redacted]"

// RedactSnapshot converts an arbitrary JSON-shaped diagnostics value into a support-safe
// copy. Live diagnostics stay untouched for local dashboards; this copy is intended for
// screenshots, bug reports, and support bundles that may leave the machine.
//
// The policy is intentionally name-based and recursive so newly nested structures cannot
// accidentally bypass it. Network identities/addresses and credential-like fields are
// replaced while operational counters, path IDs, scheduler state, timing, loss and pacing
// telemetry remain useful for debugging.
func RedactSnapshot(v any) (any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("diag: marshal snapshot for redaction: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("diag: decode snapshot for redaction: %w", err)
	}
	return redactValue(out), nil
}

func redactValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if sensitiveField(k) {
				x[k] = redactedValue
				continue
			}
			x[k] = redactValue(child)
		}
		return x
	case []any:
		for i := range x {
			x[i] = redactValue(x[i])
		}
		return x
	default:
		return v
	}
}

func sensitiveField(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "session_index" || name == "tunnel_ip" {
		return true
	}
	for _, suffix := range []string{"_ip", "_addr", "_address", "_key", "_token", "_secret", "_password", "_credential"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}
