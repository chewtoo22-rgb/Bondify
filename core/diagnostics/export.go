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

type SupportExport struct {
	SchemaVersion int      `json:"schema_version"`
	Product       string   `json:"product"`
	Snapshot      Snapshot `json:"snapshot"`
}

func MarshalSupportExport(snapshot Snapshot) ([]byte, error) {
	normalized := BuildSnapshot(snapshot.GeneratedAt, snapshot.Mode, snapshot.Connected, snapshot.Paths)
	export := SupportExport{SchemaVersion: SupportExportSchemaVersion, Product: "bondify", Snapshot: normalized}
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
