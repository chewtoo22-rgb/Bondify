//go:build windows

package diagnostics

import "golang.org/x/sys/windows"

func replaceSupportExportFile(from, to string) error {
	fromPtr, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(fromPtr, toPtr, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// MoveFileEx with MOVEFILE_WRITE_THROUGH flushes the replacement before it
// returns. Windows does not provide the same portable directory fsync contract
// used on Unix, so there is no additional directory handle flush here.
func syncSupportExportDir(string) error { return nil }
