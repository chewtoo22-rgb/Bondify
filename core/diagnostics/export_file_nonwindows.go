//go:build !windows

package diagnostics

import "os"

func replaceSupportExportFile(src, dst string) error {
	return os.Rename(src, dst)
}

func syncSupportExportDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
