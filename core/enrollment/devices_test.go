package enrollment

import (
	"errors"
	"testing"
	"time"
)

func TestDeviceManagerListAndRevokeAreAccountScoped(t *testing.T) {
	registry, err := NewDeviceRegistry(4)
	if err != nil {
		t.Fatalf("NewDeviceRegistry: %v", err)
	}
	now := time.Date(2026, 8, 31, 1, 0, 0, 0, time.UTC)
	records := []DeviceRecord{
		{AccountID: "acct-a", DeviceID: "00112233445566778899aabbccddeeff", Name: "Phone", Platform: PlatformAndroid, EnrolledAt: now},
		{AccountID: "acct-a", DeviceID: "ffeeddccbbaa99887766554433221100", Name: "PC", Platform: PlatformWindows, EnrolledAt: now},
		{AccountID: "acct-b", DeviceID: "11112222333344445555666677778888", Name: "Other", Platform: PlatformAndroid, EnrolledAt: now},
	}
	for _, record := range records {
		if err := registry.Put(record); err != nil {
			t.Fatalf("Put(%s): %v", record.DeviceID, err)
		}
	}

	manager, err := NewDeviceManager(registry)
	if err != nil {
		t.Fatalf("NewDeviceManager: %v", err)
	}
	got, err := manager.ListDevices("acct-a")
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(got) != 2 || got[0].DeviceID != records[0].DeviceID || got[1].DeviceID != records[1].DeviceID {
		t.Fatalf("unexpected deterministic inventory: %+v", got)
	}

	if err := manager.RevokeDevice("acct-a", records[0].DeviceID); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	if _, err := registry.Get("acct-a", records[0].DeviceID); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("revoked device lookup error = %v, want ErrDeviceNotFound", err)
	}
	other, err := manager.ListDevices("acct-b")
	if err != nil || len(other) != 1 || other[0].DeviceID != records[2].DeviceID {
		t.Fatalf("other account changed: devices=%+v err=%v", other, err)
	}
}

func TestDeviceManagerRejectsMissingDirectory(t *testing.T) {
	if _, err := NewDeviceManager(nil); !errors.Is(err, ErrDeviceDirectory) {
		t.Fatalf("NewDeviceManager(nil) error = %v, want ErrDeviceDirectory", err)
	}
	var manager *DeviceManager
	if _, err := manager.ListDevices("acct-a"); !errors.Is(err, ErrDeviceDirectory) {
		t.Fatalf("nil ListDevices error = %v, want ErrDeviceDirectory", err)
	}
	if err := manager.RevokeDevice("acct-a", "00112233445566778899aabbccddeeff"); !errors.Is(err, ErrDeviceDirectory) {
		t.Fatalf("nil RevokeDevice error = %v, want ErrDeviceDirectory", err)
	}
}
