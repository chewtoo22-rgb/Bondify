package enrollment

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fileStoreTestDeviceRecord(account, id, name string) DeviceRecord {
	return DeviceRecord{
		AccountID:  account,
		DeviceID:   id,
		Name:       name,
		Platform:   PlatformAndroid,
		EnrolledAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.FixedZone("test", -4*60*60)),
	}
}

func TestFileDeviceStoreRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "devices.json")
	store, err := OpenFileDeviceStore(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	record := fileStoreTestDeviceRecord(" acct-1 ", strings.Repeat("a", 32), "  Matt's   Pixel  ")
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("store mode = %o, want 600", got)
	}

	reopened, err := OpenFileDeviceStore(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get("acct-1", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Matt's Pixel" {
		t.Fatalf("name = %q", got.Name)
	}
	if got.EnrolledAt.Location() != time.UTC {
		t.Fatalf("timestamp not normalized to UTC: %v", got.EnrolledAt)
	}
}

func TestFileDeviceStorePersistsDeterministically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	store, err := OpenFileDeviceStore(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []DeviceRecord{
		fileStoreTestDeviceRecord("acct-b", strings.Repeat("b", 32), "B"),
		fileStoreTestDeviceRecord("acct-a", strings.Repeat("c", 32), "C"),
		fileStoreTestDeviceRecord("acct-a", strings.Repeat("a", 32), "A"),
	} {
		if err := store.Put(record); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	first := strings.Index(text, strings.Repeat("a", 32))
	second := strings.Index(text, strings.Repeat("c", 32))
	third := strings.Index(text, strings.Repeat("b", 32))
	if !(first >= 0 && first < second && second < third) {
		t.Fatalf("records not deterministically ordered: %s", text)
	}
}

func TestFileDeviceStoreRejectsCorruptAndSymlinkState(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version":99,"devices":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileDeviceStore(bad, 4); !errors.Is(err, ErrDeviceStore) {
		t.Fatalf("unsupported version error = %v", err)
	}

	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"devices":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := OpenFileDeviceStore(link, 4); !errors.Is(err, ErrDeviceStore) {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestFileDeviceStoreRollsBackMemoryWhenPersistenceFails(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "state")
	path := filepath.Join(parent, "devices.json")
	store, err := OpenFileDeviceStore(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	first := fileStoreTestDeviceRecord("acct", strings.Repeat("a", 32), "First")
	if err := store.Put(first); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	second := fileStoreTestDeviceRecord("acct", strings.Repeat("b", 32), "Second")
	if err := store.Put(second); !errors.Is(err, ErrDeviceStore) {
		t.Fatalf("persistence error = %v", err)
	}
	if _, err := store.Get("acct", strings.Repeat("b", 32)); !errors.Is(err, ErrDeviceNotFound) {
		t.Fatalf("failed write remained in memory: %v", err)
	}
	if _, err := store.Get("acct", strings.Repeat("a", 32)); err != nil {
		t.Fatalf("existing record lost after rollback: %v", err)
	}
}

func TestFileDeviceStoreEnforcesCapacityAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	store, err := OpenFileDeviceStore(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(fileStoreTestDeviceRecord("acct", strings.Repeat("a", 32), "One")); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileDeviceStore(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Put(fileStoreTestDeviceRecord("acct", strings.Repeat("b", 32), "Two")); !errors.Is(err, ErrDeviceCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
}
