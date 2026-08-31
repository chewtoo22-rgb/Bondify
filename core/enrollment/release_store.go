package enrollment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// MaxDeviceStoreBytes bounds startup memory and parsing work for the release
// enrollment registry. The registry contains non-secret device metadata only.
const MaxDeviceStoreBytes int64 = 256 * 1024

// OpenReleaseFileDeviceStore is the fail-closed entry point platform/account
// services should use for durable enrollment state. It adds path, size, and
// schema checks before delegating record validation to OpenFileDeviceStore.
func OpenReleaseFileDeviceStore(path string, maxDevicesPerAccount int) (*FileDeviceStore, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, fmt.Errorf("%w: state path must be absolute", ErrDeviceStore)
	}

	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return OpenFileDeviceStore(path, maxDevicesPerAccount)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: stat: %v", ErrDeviceStore, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: state path must be a regular file", ErrDeviceStore)
	}
	if info.Size() > MaxDeviceStoreBytes {
		return nil, fmt.Errorf("%w: state exceeds %d bytes", ErrDeviceStore, MaxDeviceStoreBytes)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read: %v", ErrDeviceStore, err)
	}
	if int64(len(data)) > MaxDeviceStoreBytes {
		return nil, fmt.Errorf("%w: state exceeds %d bytes", ErrDeviceStore, MaxDeviceStoreBytes)
	}

	var file deviceStoreFile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrDeviceStore, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("%w: trailing JSON value", ErrDeviceStore)
		}
		return nil, fmt.Errorf("%w: trailing data: %v", ErrDeviceStore, err)
	}

	return OpenFileDeviceStore(path, maxDevicesPerAccount)
}
