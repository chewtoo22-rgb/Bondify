//go:build !windows

package enrollment

import "os"

func replaceDeviceStoreFile(from, to string) error {
	return os.Rename(from, to)
}
