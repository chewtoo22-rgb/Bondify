package enrollment

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestServiceEnrollSuccess(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	claims, err := NewClaimStore(8, 5*time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x42}, 64)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(claims, func() time.Time { return now.Add(time.Second) })
	if err != nil {
		t.Fatal(err)
	}

	secret := bytes.Repeat([]byte{0x33}, MinClaimSecretBytes)
	claim, err := claims.Issue("acct-1", secret)
	if err != nil {
		t.Fatal(err)
	}
	pub := bytes.Repeat([]byte{0x11}, 32)
	req := Request{
		Version: ProtocolVersion,
		Name: "  Matt\tPhone  ",
		Platform: PlatformAndroid,
		PublicKey: pub,
		Nonce: bytes.Repeat([]byte{0x22}, MinNonceBytes),
	}

	record, err := service.Enroll("acct-1", claim.ID, secret, req)
	if err != nil {
		t.Fatal(err)
	}
	wantID, _ := DeviceID(pub)
	if record.AccountID != "acct-1" || record.DeviceID != wantID || record.Name != "Matt Phone" || record.Platform != PlatformAndroid {
		t.Fatalf("unexpected record: %+v", record)
	}
	if !record.EnrolledAt.Equal(now.Add(time.Second)) {
		t.Fatalf("unexpected enrolled time: %v", record.EnrolledAt)
	}
	if err := claims.Consume("acct-1", claim.ID, secret); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("claim replay should fail, got %v", err)
	}
}

func TestServiceInvalidRequestDoesNotConsumeClaim(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	claims, err := NewClaimStore(8, 5*time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x44}, 64)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(claims, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte{0x55}, MinClaimSecretBytes)
	claim, err := claims.Issue("acct-2", secret)
	if err != nil {
		t.Fatal(err)
	}

	bad := Request{Version: ProtocolVersion, Name: "Phone", Platform: PlatformAndroid, PublicKey: []byte{1}, Nonce: bytes.Repeat([]byte{2}, MinNonceBytes)}
	if _, err := service.Enroll("acct-2", claim.ID, secret, bad); !errors.Is(err, ErrPublicKey) {
		t.Fatalf("expected public key error, got %v", err)
	}

	good := Request{Version: ProtocolVersion, Name: "Phone", Platform: PlatformAndroid, PublicKey: bytes.Repeat([]byte{1}, 32), Nonce: bytes.Repeat([]byte{2}, MinNonceBytes)}
	if _, err := service.Enroll("acct-2", claim.ID, secret, good); err != nil {
		t.Fatalf("valid retry should consume preserved claim: %v", err)
	}
}

func TestServiceRejectsWrongAccountWithoutConsumingClaim(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.UTC)
	claims, err := NewClaimStore(8, 5*time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x66}, 64)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(claims, nil)
	if err != nil {
		t.Fatal(err)
	}
	secret := bytes.Repeat([]byte{0x77}, MinClaimSecretBytes)
	claim, err := claims.Issue("acct-3", secret)
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Version: ProtocolVersion, Name: "Windows PC", Platform: PlatformWindows, PublicKey: bytes.Repeat([]byte{3}, 32), Nonce: bytes.Repeat([]byte{4}, MinNonceBytes)}

	if _, err := service.Enroll("acct-wrong", claim.ID, secret, req); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("expected invalid claim for wrong account, got %v", err)
	}
	if _, err := service.Enroll("acct-3", claim.ID, secret, req); err != nil {
		t.Fatalf("claim should remain usable by owning account: %v", err)
	}
}

func TestNewServiceRejectsNilStore(t *testing.T) {
	if _, err := NewService(nil, nil); !errors.Is(err, ErrEnrollmentStore) {
		t.Fatalf("expected enrollment store error, got %v", err)
	}
}
