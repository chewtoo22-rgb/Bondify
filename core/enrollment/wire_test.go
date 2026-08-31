package enrollment

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func validEnrollmentWirePayload(t *testing.T) []byte {
	t.Helper()
	payload, err := json.Marshal(EnrollmentWireRequest{
		ClaimID:   strings.Repeat("a", 32),
		Secret:    base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, GeneratedClaimSecretBytes)),
		Version:   ProtocolVersion,
		Name:      "  Test Phone  ",
		Platform:  PlatformAndroid,
		PublicKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, 32)),
		Nonce:     base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x22}, MinNonceBytes)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestEncodeClaimGrant(t *testing.T) {
	claim := EnrollmentClaim{
		ID:        strings.Repeat("b", 32),
		ExpiresAt: time.Date(2026, 8, 31, 18, 30, 0, 123, time.FixedZone("test", 3600)),
	}
	secret := bytes.Repeat([]byte{0x55}, GeneratedClaimSecretBytes)
	encoded, err := EncodeClaimGrant(claim, secret)
	if err != nil {
		t.Fatal(err)
	}

	var grant ClaimGrant
	if err := json.Unmarshal(encoded, &grant); err != nil {
		t.Fatal(err)
	}
	if grant.ClaimID != claim.ID {
		t.Fatalf("claim id = %q", grant.ClaimID)
	}
	decodedSecret, err := base64.StdEncoding.Strict().DecodeString(grant.Secret)
	if err != nil || !bytes.Equal(decodedSecret, secret) {
		t.Fatalf("unexpected secret encoding: %v", err)
	}
	if grant.ExpiresAt != claim.ExpiresAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("expires_at = %q", grant.ExpiresAt)
	}
}

func TestDecodeEnrollmentWire(t *testing.T) {
	claimID, secret, request, err := DecodeEnrollmentWire(validEnrollmentWirePayload(t))
	if err != nil {
		t.Fatal(err)
	}
	if claimID != strings.Repeat("a", 32) {
		t.Fatalf("claim id = %q", claimID)
	}
	if len(secret) != GeneratedClaimSecretBytes {
		t.Fatalf("secret length = %d", len(secret))
	}
	if request.Name != "  Test Phone  " || request.Platform != PlatformAndroid || len(request.PublicKey) != 32 || len(request.Nonce) != MinNonceBytes {
		t.Fatalf("unexpected request: %+v", request)
	}
}

func TestDecodeEnrollmentWireFailsClosed(t *testing.T) {
	base := string(validEnrollmentWirePayload(t))
	tests := map[string][]byte{
		"empty":        nil,
		"oversized":    bytes.Repeat([]byte{'x'}, MaxEnrollmentWireBytes+1),
		"unknown field": []byte(strings.TrimSuffix(base, "}") + `,"account_id":"acct"}`),
		"trailing":     []byte(base + `{}`),
		"bad claim id": []byte(strings.Replace(base, strings.Repeat("a", 32), "nope", 1)),
		"bad base64":   []byte(strings.Replace(base, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, GeneratedClaimSecretBytes)), "%%%", 1)),
	}

	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := DecodeEnrollmentWire(payload); !errors.Is(err, ErrEnrollmentWire) {
				t.Fatalf("error = %v, want %v", err, ErrEnrollmentWire)
			}
		})
	}
}

func TestEncodeClaimGrantRejectsInvalidInputs(t *testing.T) {
	validClaim := EnrollmentClaim{ID: strings.Repeat("c", 32), ExpiresAt: time.Now().UTC().Add(time.Minute)}
	if _, err := EncodeClaimGrant(EnrollmentClaim{}, bytes.Repeat([]byte{1}, GeneratedClaimSecretBytes)); !errors.Is(err, ErrEnrollmentWire) {
		t.Fatalf("invalid claim error = %v", err)
	}
	if _, err := EncodeClaimGrant(validClaim, []byte{1}); !errors.Is(err, ErrEnrollmentWire) {
		t.Fatalf("short secret error = %v", err)
	}
}
