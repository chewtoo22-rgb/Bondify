package enrollment

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestAccountServiceEndToEnd(t *testing.T) {
	now := time.Date(2026, 8, 31, 15, 0, 0, 0, time.UTC)
	random := bytes.NewReader(bytes.Repeat([]byte{0x42}, 256))
	claims, err := NewClaimStore(8, 10*time.Minute, random, func() time.Time { return now })
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

	claim, secret, err := service.IssueEnrollmentClaim("acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != GeneratedClaimSecretBytes {
		t.Fatalf("secret length = %d, want %d", len(secret), GeneratedClaimSecretBytes)
	}

	publicKey := bytes.Repeat([]byte{0x11}, 32)
	record, err := service.EnrollDevice("acct-1", claim.ID, secret, Request{
		Version:   ProtocolVersion,
		Name:      "  Matt's Phone  ",
		Platform:  PlatformAndroid,
		PublicKey: publicKey,
		Nonce:     bytes.Repeat([]byte{0x22}, MinNonceBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Name != "Matt's Phone" {
		t.Fatalf("normalized name = %q", record.Name)
	}

	devices, err := service.ListDevices("acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].DeviceID != record.DeviceID {
		t.Fatalf("unexpected inventory: %+v", devices)
	}

	if _, err := service.EnrollDevice("acct-1", claim.ID, secret, Request{
		Version:   ProtocolVersion,
		Name:      "Replay",
		Platform:  PlatformAndroid,
		PublicKey: publicKey,
		Nonce:     bytes.Repeat([]byte{0x33}, MinNonceBytes),
	}); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("replay error = %v, want %v", err, ErrClaimInvalid)
	}

	if err := service.RevokeDevice("acct-1", record.DeviceID); err != nil {
		t.Fatal(err)
	}
	devices, err = service.ListDevices("acct-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 0 {
		t.Fatalf("inventory after revoke = %+v", devices)
	}
}

func TestAccountServiceFailsClosedWithoutDependencies(t *testing.T) {
	registry, err := NewDeviceRegistry(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewAccountService(nil, registry, nil); !errors.Is(err, ErrEnrollmentStore) {
		t.Fatalf("nil claims error = %v", err)
	}

	claims, err := NewClaimStore(1, time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x01}, 128)), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewAccountService(claims, nil, nil); !errors.Is(err, ErrDeviceRecordStore) {
		t.Fatalf("nil store error = %v", err)
	}

	var nilService *AccountService
	if _, _, err := nilService.IssueEnrollmentClaim("acct"); !errors.Is(err, ErrEnrollmentStore) {
		t.Fatalf("nil issue error = %v", err)
	}
	if _, err := nilService.ListDevices("acct"); !errors.Is(err, ErrDeviceDirectory) {
		t.Fatalf("nil list error = %v", err)
	}
	if err := nilService.RevokeDevice("acct", "00000000000000000000000000000000"); !errors.Is(err, ErrDeviceDirectory) {
		t.Fatalf("nil revoke error = %v", err)
	}
}
