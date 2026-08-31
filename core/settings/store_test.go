package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testConfig() Config {
	return Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeSpeed,
		Interfaces: []InterfacePreference{
			{ID: " wifi ", Enabled: true},
			{ID: "ethernet", Enabled: false},
		},
	}
}

func TestFileStoreRoundTripCanonicalAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(testConfig()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}

	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeSpeed || len(cfg.Interfaces) != 2 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if cfg.Interfaces[0].ID != "ethernet" || cfg.Interfaces[1].ID != "wifi" {
		t.Fatalf("interfaces not canonicalized: %#v", cfg.Interfaces)
	}
}

func TestFileStoreRejectsInvalidBeforeReplacingValidState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(testConfig()); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	bad := testConfig()
	bad.Mode = ModeStream
	if err := store.Save(bad); err == nil {
		t.Fatal("Save accepted reserved mode")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("invalid Save modified durable state")
	}
}

func TestFileStoreRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":  `{"schema_version":1,"mode":"speed","interfaces":[{"id":"wifi","enabled":true}],"future":true}`,
		"trailing": `{"schema_version":1,"mode":"speed","interfaces":[{"id":"wifi","enabled":true}]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := NewFileStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(); err == nil {
				t.Fatalf("Load accepted %s JSON", name)
			}
		})
	}
}

func TestFileStoreRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte(`{"schema_version":1,"mode":"speed","interfaces":[{"id":"wifi","enabled":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "settings.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("Load symlink error = %v", err)
	}
	if err := store.Save(testConfig()); err == nil {
		t.Fatal("Save replaced symlink")
	}
}

func TestFileStoreRejectsOversizeAndRelativePath(t *testing.T) {
	if _, err := NewFileStore("relative/settings.json"); err == nil {
		t.Fatal("NewFileStore accepted relative path")
	}

	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxConfigBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load accepted oversized store")
	}
}
