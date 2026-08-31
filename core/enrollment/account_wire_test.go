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

func newWireAccountService(t *testing.T) *AccountService {
	t.Helper()
	now := time.Date(2026, 8, 31, 17, 0, 0, 0, time.UTC)
	claims, err := NewClaimStore(8, 10*time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x5a}, 1024)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewDeviceRegistry(8)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewAccountService(claims, registry, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func wirePayloadForGrant(t *testing.T, grantBytes []byte, name string) []byte {
	t.Helper()
	var grant ClaimGrant
	if err := json.Unmarshal(grantBytes, &grant); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(EnrollmentWireRequest{
		ClaimID:   grant.ClaimID,
		Secret:    grant.Secret,
		Version:   ProtocolVersion,
		Name:      name,
		Platform:  PlatformAndroid,
		PublicKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x11}, 32)),
		Nonce:     base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x22}, MinNonceBytes)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestAccountServiceWireEndToEnd(t *testing.T) {
	service := newWireAccountService(t)
	grant, err := service.IssueEnrollmentGrant("acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(grant) == 0 || len(grant) > MaxEnrollmentWireBytes {
		t.Fatalf("grant length = %d", len(grant))
	}

	record, err := service.EnrollDeviceWire("acct-a", wirePayloadForGrant(t, grant, "  Android Phone  "))
	if err != nil {
		t.Fatal(err)
	}
	if record.Name != "Android Phone" || record.Platform != PlatformAndroid {
		t.Fatalf("unexpected record: %+v", record)
	}
	devices, err := service.ListDevices("acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].DeviceID != record.DeviceID {
		t.Fatalf("unexpected inventory: %+v", devices)
	}
}

func TestAccountServiceWireUsesAuthenticatedAccount(t *testing.T) {
	service := newWireAccountService(t)
	grant, err := service.IssueEnrollmentGrant("acct-owner")
	if err != nil {
		t.Fatal(err)
	}
	payload := wirePayloadForGrant(t, grant, "Phone")

	if _, err := service.EnrollDeviceWire("acct-other", payload); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("cross-account error = %v, want %v", err, ErrClaimInvalid)
	}

	// A mismatched authenticated account must not consume the owner's claim.
	if _, err := service.EnrollDeviceWire("acct-owner", payload); err != nil {
		t.Fatalf("owner enrollment after mismatch: %v", err)
	}
}

func TestAccountServiceWireRejectsMalformedBeforeEnrollment(t *testing.T) {
	service := newWireAccountService(t)
	grant, err := service.IssueEnrollmentGrant("acct-a")
	if err != nil {
		t.Fatal(err)
	}
	valid := wirePayloadForGrant(t, grant, "Phone")
	malformed := []byte(strings.TrimSuffix(string(valid), "}") + `,"account_id":"acct-other"}`)

	if _, err := service.EnrollDeviceWire("acct-a", malformed); !errors.Is(err, ErrEnrollmentWire) {
		t.Fatalf("malformed error = %v, want %v", err, ErrEnrollmentWire)
	}

	// Wire rejection must happen before claim consumption.
	if _, err := service.EnrollDeviceWire("acct-a", valid); err != nil {
		t.Fatalf("valid enrollment after malformed payload: %v", err)
	}
}

func TestAccountServiceWireFailsClosedOnNilService(t *testing.T) {
	var service *AccountService
	if _, err := service.IssueEnrollmentGrant("acct"); !errors.Is(err, ErrEnrollmentStore) {
		t.Fatalf("issue error = %v", err)
	}
	if _, err := service.EnrollDeviceWire("acct", nil); !errors.Is(err, ErrEnrollmentWire) {
		t.Fatalf("enroll error = %v", err)
	}
}
