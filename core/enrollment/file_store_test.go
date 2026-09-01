package enrollment

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fileStoreTestDeviceRecord(accountID, deviceID, name string) DeviceRecord {
	return DeviceRecord{
		AccountID:  accountID,
		DeviceID:   deviceID,
		Name:       name,
		Platform:   PlatformAndroid,
		EnrolledAt: time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC),
	}
}

func TestFileDeviceStoreRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	store, err := OpenFileDeviceStore(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	record := fileStoreTestDeviceRecord("acct-a", strings.Repeat("a", 32), "Phone")
	if err := store.Put(record); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}

	reopened, err := OpenFileDeviceStore(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.List("acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DeviceID != record.DeviceID {
		t.Fatalf("round trip = %#v", got)
	}
}

func TestFileDeviceStoreDeterministicOrdering(t *testing.T) {
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
	if first < 0 || first >= second || second >= third {
		t.Fatalf("records not deterministically ordered: %s", text)
	}
}

func TestFileDeviceStoreRejectsCorruptAndSymlinkState(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version":99,"devices":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileDeviceStore(bad, 2); err == nil {
		t.Fatal("expected corrupt/version state rejection")
	}

	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"devices":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileDeviceStore(link, 2); err == nil {
		t.Fatal("expected symlink rejection")
	}
}

func TestFileDeviceStoreRejectsSymlinkedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating directory symlinks may require elevated Windows privileges")
	}
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.MkdirAll(filepath.Join(target, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "redirect")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(link, "nested", "devices.json")
	if _, err := OpenFileDeviceStore(path, 2); err == nil {
		t.Fatal("expected symlinked parent rejection")
	}
}

func TestFileDeviceStoreRevalidatesParentBeforePersist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating directory symlinks may require elevated Windows privileges")
	}
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "devices.json")
	store, err := OpenFileDeviceStore(path, 2)
	if err != nil {
		t.Fatal(err)
	}

	realDir := filepath.Join(root, "redirect-target")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, stateDir); err != nil {
		t.Fatal(err)
	}

	record := fileStoreTestDeviceRecord("acct-a", strings.Repeat("d", 32), "Phone")
	if err := store.Put(record); err == nil {
		t.Fatal("expected persist to reject parent replaced with symlink")
	}
	if _, err := os.Stat(filepath.Join(realDir, "devices.json")); !os.IsNotExist(err) {
		t.Fatalf("redirect target was unexpectedly written, err=%v", err)
	}
	got, err := store.List("acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("registry mutation was not rolled back: %#v", got)
	}
}

func TestFileDeviceStoreAllowsMissingSafeParentSuffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "devices.json")
	store, err := OpenFileDeviceStore(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(fileStoreTestDeviceRecord("acct-a", strings.Repeat("e", 32), "Phone")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestFileDeviceStoreRollsBackOnPersistenceFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "devices.json")
	store, err := OpenFileDeviceStore(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	first := fileStoreTestDeviceRecord("acct-a", strings.Repeat("a", 32), "A")
	if err := store.Put(first); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	second := fileStoreTestDeviceRecord("acct-a", strings.Repeat("b", 32), "B")
	if err := store.Put(second); err == nil {
		// Root-like CI environments may still write despite mode bits. Force a
		// deterministic failure by replacing the store path with a directory.
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		third := fileStoreTestDeviceRecord("acct-a", strings.Repeat("c", 32), "C")
		if err := store.Put(third); err == nil {
			t.Fatal("expected persistence failure")
		}
	}
	got, err := store.List("acct-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DeviceID != first.DeviceID {
		t.Fatalf("registry was not rolled back: %#v", got)
	}
}

func TestFileDeviceStoreCapacitySurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	store, err := OpenFileDeviceStore(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(fileStoreTestDeviceRecord("acct-a", strings.Repeat("a", 32), "A")); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileDeviceStore(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Put(fileStoreTestDeviceRecord("acct-a", strings.Repeat("b", 32), "B")); err == nil {
		t.Fatal("expected capacity rejection after restart")
	}
}
