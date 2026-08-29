package diagnostics

import (
	"sort"
	"strings"
	"time"
)

const (
	MaxPaths       = 32
	MaxLabelLength = 64
)

// PathState is a release-safe view of one Bondify path. It intentionally
// excludes addresses, interface identifiers, keys, tokens, and endpoint data.
type PathState struct {
	Label        string `json:"label"`
	Role         string `json:"role"`
	Status       string `json:"status"`
	RTTMillis    int64  `json:"rtt_ms"`
	LossPermille int64  `json:"loss_permille"`
	TxKbps       int64  `json:"tx_kbps"`
	RxKbps       int64  `json:"rx_kbps"`
}

// Snapshot is the stable diagnostics payload intended for Android/Windows UI,
// support export, and tests. It is deliberately small and privacy-preserving.
type Snapshot struct {
	GeneratedAt time.Time   `json:"generated_at"`
	Mode        string      `json:"mode"`
	Connected   bool        `json:"connected"`
	PathCount   int         `json:"path_count"`
	Paths       []PathState `json:"paths"`
}

// BuildSnapshot normalizes untrusted runtime labels and metrics into a bounded,
// deterministic diagnostics contract. Negative counters are clamped to zero,
// excessive paths are dropped, and output ordering is stable.
func BuildSnapshot(now time.Time, mode string, connected bool, paths []PathState) Snapshot {
	if now.IsZero() {
		now = time.Unix(0, 0).UTC()
	} else {
		now = now.UTC()
	}

	bounded := make([]PathState, 0, min(len(paths), MaxPaths))
	for i := 0; i < len(paths) && i < MaxPaths; i++ {
		p := paths[i]
		p.Label = sanitizeLabel(p.Label)
		p.Role = normalizeToken(p.Role, "unknown")
		p.Status = normalizeToken(p.Status, "unknown")
		p.RTTMillis = clampNonNegative(p.RTTMillis)
		p.LossPermille = clampRange(p.LossPermille, 0, 1000)
		p.TxKbps = clampNonNegative(p.TxKbps)
		p.RxKbps = clampNonNegative(p.RxKbps)
		bounded = append(bounded, p)
	}

	sort.SliceStable(bounded, func(i, j int) bool {
		if bounded[i].Label != bounded[j].Label {
			return bounded[i].Label < bounded[j].Label
		}
		if bounded[i].Role != bounded[j].Role {
			return bounded[i].Role < bounded[j].Role
		}
		return bounded[i].Status < bounded[j].Status
	})

	return Snapshot{
		GeneratedAt: now,
		Mode:        normalizeToken(mode, "unknown"),
		Connected:   connected,
		PathCount:   len(bounded),
		Paths:       bounded,
	}
}

func sanitizeLabel(v string) string {
	v = strings.TrimSpace(v)
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
	if v == "" {
		return "path"
	}
	r := []rune(v)
	if len(r) > MaxLabelLength {
		v = string(r[:MaxLabelLength])
	}
	return v
}

func normalizeToken(v, fallback string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return fallback
	}
	var b strings.Builder
	for _, r := range v {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return fallback
	}
	return b.String()
}

func clampNonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func clampRange(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
