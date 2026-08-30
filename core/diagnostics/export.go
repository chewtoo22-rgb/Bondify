package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
)

const (
	SupportExportSchemaVersion = 1
	MaxSupportExportBytes      = 64 * 1024
)

var ErrSupportExportTooLarge = errors.New("diagnostics support export exceeds maximum size")

// SupportExport is the stable, versioned payload used for user-visible support
// exports. The embedded Snapshot remains privacy-preserving and intentionally
// excludes addresses, interface identifiers, endpoint data, keys, and tokens.
type SupportExport struct {
	SchemaVersion int      `json:"schema_version"`
	Product       string   `json:"product"`
	Snapshot      Snapshot `json:"snapshot"`
}

// MarshalSupportExport re-normalizes the supplied snapshot before serialization
// so callers cannot bypass the diagnostics bounds by constructing Snapshot
// values directly. The result is deterministic JSON terminated by one newline.
func MarshalSupportExport(snapshot Snapshot) ([]byte, error) {
	normalized := BuildSnapshot(
		snapshot.GeneratedAt,
		snapshot.Mode,
		snapshot.Connected,
		snapshot.Paths,
	)

	export := SupportExport{
		SchemaVersion: SupportExportSchemaVersion,
		Product:       "bondify",
		Snapshot:      normalized,
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(true)
	if err := enc.Encode(export); err != nil {
		return nil, err
	}
	if buf.Len() > MaxSupportExportBytes {
		return nil, ErrSupportExportTooLarge
	}
	return buf.Bytes(), nil
}
