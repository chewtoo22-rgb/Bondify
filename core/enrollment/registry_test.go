package enrollment

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func testDeviceRecord(accountID, deviceID, name string, platform Platform) DeviceRecord {
	return DeviceRecord{
		AccountID:  accountID,
		DeviceID:   deviceID,
		Name:       name,
		Platform:   platform,
		EnrolledAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.FixedZone("test", -4*60*60)),
	}
}

func TestDeviceRegistryPutGetNormalizesRecord(t *testing.T) {
	registry, err := NewDeviceRegistry(4)
	if err != nil {
		t.Fatal(err)
	}
	record := testDeviceRecord(" account-1 ", "00112233445566778899aabbccddeeff", " Matt\tNUC ", PlatformWindows)
	if err := registry.Put(record); err != nil {
		t.Fatal(err)
	}
	got, err := registry.Get("account-1", record.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != "account-1" || got.Name != "Matt NUC" || got.EnrolledAt.Location() != time.UTC {
		t.Fatalf("record was not normalized: %#v", got)
	}
}

func TestDeviceRegistryCapacityAllowsSameDeviceRefresh(t *testing.T) {
	registry, err := NewDeviceRegistry(1)
	if err != nil {
		t.Fatal(err)
	}
	first := testDeviceRecord("acct", "00112233445566778899aabbccddeeff", "Phone", PlatformAndroid)
	if err := registry.Put(first); err != nil {
		t.Fatal(err)
	}
	first.Name = "Phone Renamed"
	if err := registry.Put(first); err != nil {
		t.Fatalf("same-device refresh should not consume capacity: %v", err)
	}
	second := testDeviceRecord("acct", "ffeeddccbbaa99887766554433221100", "PC", PlatformWindows)
	if err := registry.Put(second); !errors.Is(err, ErrDeviceCapacity) {
		t.Fatalf("got %v, want ErrDeviceCapacity", err)
	}
}

func TestDeviceRegistryRemoveReleasesCapacity(t *testing.T) {
	registry, _ := NewDeviceRegistry(1)
	first := testDeviceRecord("acct", "00112233445566778899aabbccddeeff", "Phone", PlatformAndroid)
	second := testDeviceRecord("acct", "ffeeddccbbaa99887766554433221100", "PC", PlatformWindows)
	if err := registry.Put(first); err != nil {
		t.Fatal(err)
	}
	if err := registry.Remove("acct", first.DeviceID); err != nil {
		t.Fatal(err)
	}
	if err := registry.Put(second); err != nil {
		t.Fatalf("capacity was not released: %v", err)
	}
}

func TestDeviceRegistryListDeterministic(t *testing.T) {
	registry, _ := NewDeviceRegistry(4)
	ids := []string{
		"ffeeddccbbaa99887766554433221100",
		"00112233445566778899aabbccddeeff",
		"11112222333344445555666677778888",
	}
	for _, id := range ids {
		if err := registry.Put(testDeviceRecord("acct", id, id[:4], PlatformLinux)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := registry.List("acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].DeviceID != ids[1] || got[1].DeviceID != ids[2] || got[2].DeviceID != ids[0] {
		t.Fatalf("unexpected order: %#v", got)
	}
}

func TestDeviceRegistryRejectsMalformedRecords(t *testing.T) {
	registry, _ := NewDeviceRegistry(4)
	cases := []DeviceRecord{
		testDeviceRecord("", "00112233445566778899aabbccddeeff", "Phone", PlatformAndroid),
		testDeviceRecord("acct", "not-a-device-id", "Phone", PlatformAndroid),
		testDeviceRecord("acct", "00112233445566778899aabbccddeeff", "", PlatformAndroid),
		testDeviceRecord("acct", "00112233445566778899aabbccddeeff", "Phone", Platform("ios")),
	}
	zeroTime := testDeviceRecord("acct", "00112233445566778899aabbccddeeff", "Phone", PlatformAndroid)
	zeroTime.EnrolledAt = time.Time{}
	cases = append(cases, zeroTime)
	for i, record := range cases {
		if err := registry.Put(record); !errors.Is(err, ErrDeviceRecord) {
			t.Fatalf("case %d: got %v, want ErrDeviceRecord", i, err)
		}
	}
}

func TestDeviceRegistryConcurrentSameDevicePutIsBounded(t *testing.T) {
	registry, _ := NewDeviceRegistry(1)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = registry.Put(testDeviceRecord("acct", "00112233445566778899aabbccddeeff", "Phone", PlatformAndroid))
		}()
	}
	wg.Wait()
	got, err := registry.List("acct")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
}

func TestNewDeviceRegistryRejectsNonPositiveCapacity(t *testing.T) {
	if _, err := NewDeviceRegistry(0); !errors.Is(err, ErrDeviceCapacity) {
		t.Fatalf("got %v, want ErrDeviceCapacity", err)
	}
}
