package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileStoreRejectsSymlinkedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}

	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(root, "linked")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}

	_, err := NewFileStore(filepath.Join(linkDir, "nested", "settings.json"))
	if err == nil || !strings.Contains(err.Error(), "store parent") {
		t.Fatalf("NewFileStore symlinked parent error = %v", err)
	}
}

func TestFileStoreRejectsParentSwapBeforeSave(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}

	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	if err := os.Mkdir(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "settings.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}

	attackerDir := filepath.Join(root, "redirected")
	if err := os.Mkdir(attackerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attackerDir, stateDir); err != nil {
		t.Fatal(err)
	}

	if err := store.Save(testConfig()); err == nil || !strings.Contains(err.Error(), "store parent") {
		t.Fatalf("Save after parent swap error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(attackerDir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("redirected settings file exists or stat failed unexpectedly: %v", err)
	}
}

func TestFileStoreAllowsMissingParentSuffix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new", "nested", "settings.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(testConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
}
