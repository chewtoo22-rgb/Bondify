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
	// A directory at the configured key-file path is an existing filesystem object, but
	// it cannot be read as the relay key. The loader must return that read failure rather
	// than treating it as a missing key and generating a replacement identity.
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
