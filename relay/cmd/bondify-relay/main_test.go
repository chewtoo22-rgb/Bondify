package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chewtoo22-rgb/bondify/core/crypto"
)

func TestLoadOrGenerateKeyCreatesMissingFileAndReloadsIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "relay.key")

	first, err := loadOrGenerateKey(path)
	if err != nil {
		t.Fatalf("first loadOrGenerateKey: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("generated key file missing: %v", err)
	}

	second, err := loadOrGenerateKey(path)
	if err != nil {
		t.Fatalf("second loadOrGenerateKey: %v", err)
	}
	if first.Private != second.Private || first.Public != second.Public {
		t.Fatalf("relay identity changed across reload")
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated key: %v", err)
	}
	decoded, err := crypto.DecodeKey(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("decode generated key: %v", err)
	}
	if decoded != first.Private {
		t.Fatalf("persisted private key does not match generated identity")
	}
}

func TestLoadOrGenerateKeyFailsClosedOnNonNotExistReadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.key")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatalf("mkdir key-path sentinel: %v", err)
	}

	_, err := loadOrGenerateKey(path)
	if err == nil {
		t.Fatal("expected non-ENOENT key read failure")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected not-exist classification: %v", err)
	}
	if !strings.Contains(err.Error(), "read key file") {
		t.Fatalf("expected read-key failure, got: %v", err)
	}

	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("key-path sentinel disappeared: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("key-path sentinel was replaced; expected directory")
	}
}

func TestCreateKeyFileExclusiveRefusesExistingPathWithoutTruncating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.key")
	const sentinel = "do-not-truncate\n"
	if err := os.WriteFile(path, []byte(sentinel), 0600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	err := createKeyFileExclusive(path, []byte("replacement-private-key\n"))
	if err == nil {
		t.Fatal("expected exclusive create to fail for an existing path")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected EEXIST classification, got: %v", err)
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read sentinel after refused create: %v", readErr)
	}
	if string(got) != sentinel {
		t.Fatalf("existing key path was modified: got %q want %q", got, sentinel)
	}
}
