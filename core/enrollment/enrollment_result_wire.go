package enrollment

import (
	"encoding/json"
	"errors"
	"time"
)

const (
	EnrollmentResultWireVersion = 1
	MaxEnrollmentResultWireBytes = 1024
)

var ErrEnrollmentResultWire = errors.New("invalid enrollment result wire payload")

// EnrollmentResult is the transport-safe representation returned after a
// device has been durably enrolled. Account identity and all enrollment
// credentials are intentionally excluded from the client-visible payload.
type EnrollmentResult struct {
	Version    int      `json:"version"`
	DeviceID   string   `json:"device_id"`
	Name       string   `json:"name"`
	Platform   Platform `json:"platform"`
	EnrolledAt string   `json:"enrolled_at"`
}

// EncodeEnrollmentResult revalidates the durable record before serialization
// so transports cannot accidentally publish malformed or secret-bearing state.
func EncodeEnrollmentResult(record DeviceRecord) ([]byte, error) {
	normalized, err := normalizeDeviceRecord(record)
	if err != nil {
		return nil, ErrEnrollmentResultWire
	}

	payload := EnrollmentResult{
		Version:    EnrollmentResultWireVersion,
		DeviceID:   normalized.DeviceID,
		Name:       normalized.Name,
		Platform:   normalized.Platform,
		EnrolledAt: normalized.EnrolledAt.UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > MaxEnrollmentResultWireBytes {
		return nil, ErrEnrollmentResultWire
	}
	return encoded, nil
}

// EnrollDeviceResultWire strictly decodes and commits an enrollment, then
// returns the canonical cross-platform success payload. The authenticated
// account ID remains transport-owned and is never accepted from or returned to
// the client payload.
func (s *AccountService) EnrollDeviceResultWire(accountID string, payload []byte) ([]byte, error) {
	record, err := s.EnrollDeviceWire(accountID, payload)
	if err != nil {
		return nil, err
	}
	return EncodeEnrollmentResult(record)
}
