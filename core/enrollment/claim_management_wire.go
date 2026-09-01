package enrollment

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	ClaimManagementWireVersion = 1
	MaxClaimRevokeWireBytes     = 256
	MaxClaimRevokeResultBytes   = 256
)

var ErrClaimManagementWire = errors.New("invalid claim management wire payload")

// ClaimRevokeRequest is the complete client-controlled body for cancelling an
// outstanding enrollment claim. Account identity is supplied separately by the
// authenticated transport and is intentionally absent from this payload.
type ClaimRevokeRequest struct {
	ClaimID string `json:"claim_id"`
}

// ClaimRevokeResult is the canonical cross-platform acknowledgement returned
// only after the authenticated account successfully invalidates the claim.
type ClaimRevokeResult struct {
	Version int    `json:"version"`
	ClaimID string `json:"claim_id"`
	Revoked bool   `json:"revoked"`
}

// DecodeClaimRevokeWire strictly accepts exactly one validated claim ID.
func DecodeClaimRevokeWire(payload []byte) (string, error) {
	if len(payload) == 0 || len(payload) > MaxClaimRevokeWireBytes {
		return "", ErrClaimManagementWire
	}

	var request ClaimRevokeRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return "", ErrClaimManagementWire
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", ErrClaimManagementWire
	}
	if !validClaimID(request.ClaimID) {
		return "", ErrClaimManagementWire
	}
	return request.ClaimID, nil
}

// EncodeClaimRevokeResult returns a bounded deterministic acknowledgement.
func EncodeClaimRevokeResult(claimID string) ([]byte, error) {
	if !validClaimID(claimID) {
		return nil, ErrClaimManagementWire
	}
	encoded, err := json.Marshal(ClaimRevokeResult{
		Version: ClaimManagementWireVersion,
		ClaimID: claimID,
		Revoked: true,
	})
	if err != nil || len(encoded) > MaxClaimRevokeResultBytes {
		return nil, ErrClaimManagementWire
	}
	return encoded, nil
}

// RevokeEnrollmentClaimResultWire strictly decodes a claim ID, revokes it only
// for the separately authenticated account, and then returns one canonical
// Android/Windows acknowledgement. Failed or cross-account revocation returns
// no success payload.
func (s *AccountService) RevokeEnrollmentClaimResultWire(accountID string, payload []byte) ([]byte, error) {
	claimID, err := DecodeClaimRevokeWire(payload)
	if err != nil {
		return nil, err
	}
	if err := s.RevokeEnrollmentClaim(accountID, claimID); err != nil {
		return nil, err
	}
	return EncodeClaimRevokeResult(claimID)
}
