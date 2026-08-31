package enrollment

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const MaxEnrollmentWireBytes = 2048

var ErrEnrollmentWire = errors.New("invalid enrollment wire payload")

// ClaimGrant is the transport-safe representation returned after an
// authenticated account requests a one-time enrollment claim. The secret is
// returned exactly once and must never be logged or persisted in inventory.
type ClaimGrant struct {
	ClaimID   string `json:"claim_id"`
	Secret    string `json:"secret"`
	ExpiresAt string `json:"expires_at"`
}

// EnrollmentWireRequest is the platform-neutral request body shared by Android
// and Windows clients. Account identity is intentionally excluded: the
// transport layer authenticates the account separately and supplies accountID
// to AccountService.EnrollDevice.
type EnrollmentWireRequest struct {
	ClaimID     string   `json:"claim_id"`
	Secret      string   `json:"secret"`
	Version     int      `json:"version"`
	Name        string   `json:"name"`
	Platform    Platform `json:"platform"`
	PublicKey   string   `json:"public_key"`
	Nonce       string   `json:"nonce"`
}

// EncodeClaimGrant serializes a one-time claim without exposing any additional
// account or device state. Secret bytes use standard base64 for broad platform
// interoperability.
func EncodeClaimGrant(claim EnrollmentClaim, secret []byte) ([]byte, error) {
	if !validClaimID(claim.ID) || claim.ExpiresAt.IsZero() || len(secret) < MinClaimSecretBytes || len(secret) > MaxClaimSecretBytes {
		return nil, ErrEnrollmentWire
	}
	payload := ClaimGrant{
		ClaimID:   claim.ID,
		Secret:    base64.StdEncoding.EncodeToString(secret),
		ExpiresAt: claim.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) > MaxEnrollmentWireBytes {
		return nil, ErrEnrollmentWire
	}
	return encoded, nil
}

// DecodeEnrollmentWire strictly decodes and validates an enrollment request.
// It rejects oversized payloads, unknown fields, trailing JSON values, malformed
// credentials, and invalid device identity before AccountService sees input.
func DecodeEnrollmentWire(payload []byte) (string, []byte, Request, error) {
	if len(payload) == 0 || len(payload) > MaxEnrollmentWireBytes {
		return "", nil, Request{}, ErrEnrollmentWire
	}

	var wire EnrollmentWireRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return "", nil, Request{}, ErrEnrollmentWire
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", nil, Request{}, ErrEnrollmentWire
	}

	if !validClaimID(wire.ClaimID) {
		return "", nil, Request{}, ErrEnrollmentWire
	}
	secret, err := base64.StdEncoding.Strict().DecodeString(wire.Secret)
	if err != nil || len(secret) < MinClaimSecretBytes || len(secret) > MaxClaimSecretBytes {
		return "", nil, Request{}, ErrEnrollmentWire
	}
	publicKey, err := base64.StdEncoding.Strict().DecodeString(wire.PublicKey)
	if err != nil {
		return "", nil, Request{}, ErrEnrollmentWire
	}
	nonce, err := base64.StdEncoding.Strict().DecodeString(wire.Nonce)
	if err != nil {
		return "", nil, Request{}, ErrEnrollmentWire
	}

	request := Request{
		Version:   wire.Version,
		Name:      wire.Name,
		Platform:  wire.Platform,
		PublicKey: publicKey,
		Nonce:     nonce,
	}
	if err := request.Validate(); err != nil {
		return "", nil, Request{}, ErrEnrollmentWire
	}
	return wire.ClaimID, secret, request, nil
}
