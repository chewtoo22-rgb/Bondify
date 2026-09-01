package diagnostics

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	ErrSupportExportPathRelative = errors.New("diagnostics support export path must be absolute")
	ErrSupportExportPathUnsafe   = errors.New("diagnostics support export path is unsafe")
)

// WriteSupportExportFile serializes a privacy-bounded support export and replaces
// the destination atomically. The destination parent and every ancestor up to
// the filesystem root must already exist as real directories rather than
// symlinks. Existing symlink and non-regular destinations are rejected so
// callers cannot redirect an export outside the reviewed path.
//
// The temporary file and containing directory are synced before success is
// returned. This makes a reported successful export durable across an immediate
// crash or power loss instead of merely durable in the process page cache.
func WriteSupportExportFile(path string, snapshot Snapshot) error {
	if !filepath.IsAbs(path) {
		return ErrSupportExportPathRelative
	}

	path = filepath.Clean(path)
	dir := filepath.Dir(path)

	if err := validateSupportExportDirectoryChain(dir); err != nil {
		return err
	}

	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return ErrSupportExportPathUnsafe
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect support export destination: %w", err)
	}

	payload, err := MarshalSupportExport(snapshot)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".bondify-support-*.tmp")
	if err != nil {
		return fmt.Errorf("create support export temp file: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("restrict support export permissions: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		return fmt.Errorf("write support export: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync support export: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close support export: %w", err)
	}
	if err := replaceSupportExportFile(tmpName, path); err != nil {
		return fmt.Errorf("commit support export: %w", err)
	}
	committed = true

	if err := syncSupportExportDir(dir); err != nil {
		return fmt.Errorf("sync support export directory: %w", err)
	}
	return nil
}

func validateSupportExportDirectoryChain(dir string) error {
	for current := filepath.Clean(dir); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("inspect support export ancestor %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrSupportExportPathUnsafe
		}

		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}
