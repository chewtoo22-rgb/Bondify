package enrollment

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenReleaseFileDeviceStoreRequiresAbsolutePath(t *testing.T) {
	if _, err := OpenReleaseFileDeviceStore("devices.json", 2); err == nil {
		t.Fatal("expected relative path rejection")
	}
}

func TestOpenReleaseFileDeviceStoreAcceptsMissingAbsolutePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	store, err := OpenReleaseFileDeviceStore(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("nil store")
	}
}

func TestOpenReleaseFileDeviceStoreRejectsOversizedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", int(MaxDeviceStoreBytes)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReleaseFileDeviceStore(path, 2); err == nil {
		t.Fatal("expected oversized state rejection")
	}
}

func TestOpenReleaseFileDeviceStoreRejectsUnknownAndTrailingJSON(t *testing.T) {
	for name, data := range map[string]string{
		"unknown":  `{"version":1,"devices":[],"secret":"must-not-be-accepted"}`,
		"trailing": `{"version":1,"devices":[]} {"version":1,"devices":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "devices.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenReleaseFileDeviceStore(path, 2); err == nil {
				t.Fatal("expected strict JSON rejection")
			}
		})
	}
}

func TestOpenReleaseFileDeviceStoreRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"devices":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "devices.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReleaseFileDeviceStore(link, 2); err == nil {
		t.Fatal("expected symlink rejection")
	}
}
