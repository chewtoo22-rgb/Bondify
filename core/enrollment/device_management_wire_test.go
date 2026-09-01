package enrollment

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func newDeviceManagementTestService(t *testing.T) (*AccountService, *DeviceRegistry) {
	t.Helper()
	now := time.Date(2026, 9, 1, 3, 20, 0, 0, time.UTC)
	claims, err := NewClaimStore(4, time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x42}, 256)), func() time.Time { return now })
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
	return service, registry
}

func TestListDevicesWireExcludesAccountAndSecrets(t *testing.T) {
	service, registry := newDeviceManagementTestService(t)
	record := DeviceRecord{
		AccountID:  "acct-private",
		DeviceID:   "00112233445566778899aabbccddeeff",
		Name:       "  Phone  One ",
		Platform:   PlatformAndroid,
		EnrolledAt: time.Date(2026, 9, 1, 3, 0, 0, 0, time.FixedZone("offset", -4*60*60)),
	}
	if err := registry.Put(record); err != nil {
		t.Fatal(err)
	}

	payload, err := service.ListDevicesWire("acct-private")
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, forbidden := range []string{"acct-private", "claim_id", "secret", "public_key", "nonce"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("wire inventory leaked %q: %s", forbidden, text)
		}
	}

	var inventory DeviceInventory
	if err := json.Unmarshal(payload, &inventory); err != nil {
		t.Fatal(err)
	}
	if inventory.Version != DeviceManagementWireVersion || len(inventory.Devices) != 1 {
		t.Fatalf("unexpected inventory: %+v", inventory)
	}
	item := inventory.Devices[0]
	if item.DeviceID != record.DeviceID || item.Name != "Phone One" || item.Platform != PlatformAndroid {
		t.Fatalf("unexpected item: %+v", item)
	}
	if item.EnrolledAt != "2026-09-01T07:00:00Z" {
		t.Fatalf("enrolled_at = %q", item.EnrolledAt)
	}
}

func TestRevokeDeviceWireIsAccountScoped(t *testing.T) {
	service, registry := newDeviceManagementTestService(t)
	deviceID := "00112233445566778899aabbccddeeff"
	for _, accountID := range []string{"acct-a", "acct-b"} {
		if err := registry.Put(DeviceRecord{
			AccountID:  accountID,
			DeviceID:   deviceID,
			Name:       "Shared Device ID",
			Platform:   PlatformWindows,
			EnrolledAt: time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatal(err)
		}
	}

	payload := []byte("{\"device_id\":\"00112233445566778899aabbccddeeff\"}")
	if err := service.RevokeDeviceWire("acct-a", payload); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Get("acct-a", deviceID); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("acct-a record error = %v", err)
	}
	if _, err := registry.Get("acct-b", deviceID); err != nil {
		t.Fatalf("acct-b record was affected: %v", err)
	}
}

func TestRevokeDeviceResultWireReturnsCanonicalSuccess(t *testing.T) {
	service, registry := newDeviceManagementTestService(t)
	deviceID := "00112233445566778899aabbccddeeff"
	if err := registry.Put(DeviceRecord{
		AccountID:  "acct-a",
		DeviceID:   deviceID,
		Name:       "Phone",
		Platform:   PlatformAndroid,
		EnrolledAt: time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}

	payload := []byte("{\"device_id\":\"" + deviceID + "\"}")
	result, err := service.RevokeDeviceResultWire("acct-a", payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) > MaxDeviceRevokeResultBytes {
		t.Fatalf("result length = %d", len(result))
	}
	if strings.Contains(string(result), "acct-a") {
		t.Fatalf("revoke result leaked account identity: %s", result)
	}
	var decoded DeviceRevokeResult
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != DeviceManagementWireVersion || decoded.DeviceID != deviceID || !decoded.Revoked {
		t.Fatalf("unexpected revoke result: %+v", decoded)
	}
	if _, err := registry.Get("acct-a", deviceID); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("device still present after success: %v", err)
	}
}

func TestRevokeDeviceResultWireReturnsNoSuccessOnFailure(t *testing.T) {
	service, _ := newDeviceManagementTestService(t)
	deviceID := "00112233445566778899aabbccddeeff"
	payload := []byte("{\"device_id\":\"" + deviceID + "\"}")
	result, err := service.RevokeDeviceResultWire("acct-a", payload)
	if !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("error = %v", err)
	}
	if result != nil {
		t.Fatalf("result = %q, want nil on failed revocation", result)
	}
}

func TestEncodeDeviceRevokeResultRejectsInvalidID(t *testing.T) {
	if result, err := EncodeDeviceRevokeResult("bad"); !errors.Is(err, ErrDeviceManagementWire) || result != nil {
		t.Fatalf("result = %q error = %v", result, err)
	}
}

func TestDecodeDeviceRevokeWireFailsClosed(t *testing.T) {
	validID := "00112233445566778899aabbccddeeff"
	tests := [][]byte{
		nil,
		[]byte("{}"),
		[]byte("{\"device_id\":\"bad\"}"),
		[]byte("{\"device_id\":\"" + validID + "\",\"account_id\":\"acct-a\"}"),
		[]byte("{\"device_id\":\"" + validID + "\"} {}"),
		bytes.Repeat([]byte{'x'}, MaxDeviceRevokeWireBytes+1),
	}
	for _, payload := range tests {
		if _, err := DecodeDeviceRevokeWire(payload); !errors.Is(err, ErrDeviceManagementWire) {
			t.Fatalf("payload %q error = %v", payload, err)
		}
	}
}

func TestDeviceManagementWireNilServiceFailsClosed(t *testing.T) {
	var service *AccountService
	if _, err := service.ListDevicesWire("acct-a"); !errors.Is(err, ErrDeviceDirectory) {
		t.Fatalf("list error = %v", err)
	}
	payload := []byte("{\"device_id\":\"00112233445566778899aabbccddeeff\"}")
	if err := service.RevokeDeviceWire("acct-a", payload); !errors.Is(err, ErrDeviceDirectory) {
		t.Fatalf("revoke error = %v", err)
	}
	if result, err := service.RevokeDeviceResultWire("acct-a", payload); !errors.Is(err, ErrDeviceDirectory) || result != nil {
		t.Fatalf("revoke result = %q error = %v", result, err)
	}
}
