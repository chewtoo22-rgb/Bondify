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
// the destination atomically. The destination parent must already exist and must
// not itself be a symlink. Existing symlink and non-regular destinations are
// rejected so callers cannot redirect an export outside the reviewed path.
func WriteSupportExportFile(path string, snapshot Snapshot) error {
	if !filepath.IsAbs(path) {
		return ErrSupportExportPathRelative
	}

	path = filepath.Clean(path)
	dir := filepath.Dir(path)

	parentInfo, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect support export parent: %w", err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return ErrSupportExportPathUnsafe
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
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("commit support export: %w", err)
	}
	committed = true
	return nil
}
