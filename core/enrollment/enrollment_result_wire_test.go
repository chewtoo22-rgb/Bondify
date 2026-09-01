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

func TestEncodeEnrollmentResultExcludesAccountAndCredentials(t *testing.T) {
	record := DeviceRecord{
		AccountID:  "acct-private",
		DeviceID:   "00112233445566778899aabbccddeeff",
		Name:       "  Phone   One  ",
		Platform:   PlatformAndroid,
		EnrolledAt: time.Date(2026, 9, 1, 4, 0, 0, 123, time.FixedZone("offset", -4*60*60)),
	}

	payload, err := EncodeEnrollmentResult(record)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > MaxEnrollmentResultWireBytes {
		t.Fatalf("payload size = %d", len(payload))
	}
	text := string(payload)
	for _, forbidden := range []string{"acct-private", "claim_id", "secret", "public_key", "nonce"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("result leaked %q: %s", forbidden, text)
		}
	}

	var result EnrollmentResult
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if result.Version != EnrollmentResultWireVersion || result.DeviceID != record.DeviceID {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Name != "Phone One" || result.Platform != PlatformAndroid {
		t.Fatalf("unexpected normalized result: %+v", result)
	}
	if result.EnrolledAt != "2026-09-01T08:00:00.000000123Z" {
		t.Fatalf("enrolled_at = %q", result.EnrolledAt)
	}
}

func TestEnrollDeviceResultWireCommitsCanonicalResult(t *testing.T) {
	now := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
	claims, err := NewClaimStore(4, time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x4a}, 256)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewDeviceRegistry(4)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewAccountService(claims, registry, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	claim, secret, err := service.IssueEnrollmentClaim("acct-private")
	if err != nil {
		t.Fatal(err)
	}
	publicKey := bytes.Repeat([]byte{0x21}, 32)
	nonce := bytes.Repeat([]byte{0x31}, MinNonceBytes)
	request, err := json.Marshal(EnrollmentWireRequest{
		ClaimID:   claim.ID,
		Secret:    base64.StdEncoding.EncodeToString(secret),
		Version:   ProtocolVersion,
		Name:      " Windows   Laptop ",
		Platform:  PlatformWindows,
		PublicKey: base64.StdEncoding.EncodeToString(publicKey),
		Nonce:     base64.StdEncoding.EncodeToString(nonce),
	})
	if err != nil {
		t.Fatal(err)
	}

	payload, err := service.EnrollDeviceResultWire("acct-private", request)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, forbidden := range []string{"acct-private", claim.ID, base64.StdEncoding.EncodeToString(secret), base64.StdEncoding.EncodeToString(publicKey), base64.StdEncoding.EncodeToString(nonce)} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("result leaked enrollment material %q: %s", forbidden, text)
		}
	}

	deviceID, err := DeviceID(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	record, err := registry.Get("acct-private", deviceID)
	if err != nil {
		t.Fatalf("enrollment was not committed: %v", err)
	}
	if record.Name != "Windows Laptop" || record.Platform != PlatformWindows || !record.EnrolledAt.Equal(now) {
		t.Fatalf("unexpected committed record: %+v", record)
	}

	var result EnrollmentResult
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if result.Version != EnrollmentResultWireVersion || result.DeviceID != deviceID || result.Name != "Windows Laptop" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestEncodeEnrollmentResultFailsClosed(t *testing.T) {
	tests := []DeviceRecord{
		{},
		{AccountID: "acct-a", DeviceID: "bad", Name: "Phone", Platform: PlatformAndroid, EnrolledAt: time.Now()},
		{AccountID: "acct-a", DeviceID: "00112233445566778899aabbccddeeff", Name: "Phone", Platform: "other", EnrolledAt: time.Now()},
	}
	for _, record := range tests {
		if _, err := EncodeEnrollmentResult(record); !errors.Is(err, ErrEnrollmentResultWire) {
			t.Fatalf("record %+v error = %v", record, err)
		}
	}
}
