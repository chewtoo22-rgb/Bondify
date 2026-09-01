package enrollment

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const deviceStoreVersion = 1

var ErrDeviceStore = errors.New("device store error")

type deviceStoreFile struct {
	Version int            `json:"version"`
	Devices []DeviceRecord `json:"devices"`
}

// FileDeviceStore wraps DeviceRegistry with deterministic, atomic persistence.
type FileDeviceStore struct {
	path     string
	registry *DeviceRegistry
}

func OpenFileDeviceStore(path string, maxDevicesPerAccount int) (*FileDeviceStore, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: empty path", ErrDeviceStore)
	}
	if err := validateDeviceStoreParent(path); err != nil {
		return nil, err
	}
	registry, err := NewDeviceRegistry(maxDevicesPerAccount)
	if err != nil {
		return nil, err
	}
	s := &FileDeviceStore{path: path, registry: registry}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *FileDeviceStore) Put(record DeviceRecord) error {
	if s == nil || s.registry == nil {
		return fmt.Errorf("%w: nil store", ErrDeviceStore)
	}
	s.registry.mu.Lock()
	defer s.registry.mu.Unlock()
	backup := cloneAccounts(s.registry.accounts)
	if err := s.registry.putLocked(record); err != nil {
		return err
	}
	if err := s.persistLocked(); err != nil {
		s.registry.accounts = backup
		return err
	}
	return nil
}

func (s *FileDeviceStore) Remove(accountID, deviceID string) error {
	if s == nil || s.registry == nil {
		return fmt.Errorf("%w: nil store", ErrDeviceStore)
	}
	s.registry.mu.Lock()
	defer s.registry.mu.Unlock()
	backup := cloneAccounts(s.registry.accounts)
	if err := s.registry.removeLocked(accountID, deviceID); err != nil {
		return err
	}
	if err := s.persistLocked(); err != nil {
		s.registry.accounts = backup
		return err
	}
	return nil
}

func (s *FileDeviceStore) List(accountID string) ([]DeviceRecord, error) {
	if s == nil || s.registry == nil {
		return nil, fmt.Errorf("%w: nil store", ErrDeviceStore)
	}
	return s.registry.List(accountID)
}

func (s *FileDeviceStore) load() error {
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: stat: %v", ErrDeviceStore, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: state path must be a regular file", ErrDeviceStore)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("%w: read: %v", ErrDeviceStore, err)
	}
	var file deviceStoreFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrDeviceStore, err)
	}
	if file.Version != deviceStoreVersion {
		return fmt.Errorf("%w: unsupported version %d", ErrDeviceStore, file.Version)
	}
	for _, record := range file.Devices {
		if err := s.registry.Put(record); err != nil {
			return fmt.Errorf("%w: invalid record: %v", ErrDeviceStore, err)
		}
	}
	return nil
}

func (s *FileDeviceStore) persistLocked() error {
	if err := validateDeviceStoreParent(s.path); err != nil {
		return err
	}

	devices := make([]DeviceRecord, 0)
	for _, accountDevices := range s.registry.accounts {
		for _, record := range accountDevices {
			devices = append(devices, record)
		}
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].AccountID == devices[j].AccountID {
			return devices[i].DeviceID < devices[j].DeviceID
		}
		return devices[i].AccountID < devices[j].AccountID
	})
	data, err := json.MarshalIndent(deviceStoreFile{Version: deviceStoreVersion, Devices: devices}, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: encode: %v", ErrDeviceStore, err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: create directory: %v", ErrDeviceStore, err)
	}
	if err := validateDeviceStoreParent(s.path); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".bondify-devices-*")
	if err != nil {
		return fmt.Errorf("%w: create temp: %v", ErrDeviceStore, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	closeOnError := func() { _ = tmp.Close() }
	if err := tmp.Chmod(0o600); err != nil {
		closeOnError()
		return fmt.Errorf("%w: chmod temp: %v", ErrDeviceStore, err)
	}
	if _, err := tmp.Write(data); err != nil {
		closeOnError()
		return fmt.Errorf("%w: write: %v", ErrDeviceStore, err)
	}
	if err := tmp.Sync(); err != nil {
		closeOnError()
		return fmt.Errorf("%w: sync: %v", ErrDeviceStore, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: close: %v", ErrDeviceStore, err)
	}
	if err := replaceDeviceStoreFile(tmpName, s.path); err != nil {
		return fmt.Errorf("%w: replace: %v", ErrDeviceStore, err)
	}
	return nil
}

// validateDeviceStoreParent rejects any existing symlink in the directory chain
// that contains the enrollment registry. This keeps an apparently local state
// path from being redirected elsewhere by a symlinked ancestor. Missing suffix
// directories are allowed because persistLocked creates them with restrictive
// permissions before validating the chain again.
func validateDeviceStoreParent(path string) error {
	dir := filepath.Clean(filepath.Dir(path))
	probe := dir
	for {
		info, err := os.Lstat(probe)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("%w: state parent must be a directory", ErrDeviceStore)
			}
			resolved, err := filepath.EvalSymlinks(probe)
			if err != nil {
				return fmt.Errorf("%w: resolve state parent: %v", ErrDeviceStore, err)
			}
			absProbe, err := filepath.Abs(probe)
			if err != nil {
				return fmt.Errorf("%w: resolve state parent path: %v", ErrDeviceStore, err)
			}
			absResolved, err := filepath.Abs(resolved)
			if err != nil {
				return fmt.Errorf("%w: resolve state parent target: %v", ErrDeviceStore, err)
			}
			rel, err := filepath.Rel(absProbe, absResolved)
			if err != nil || rel != "." {
				return fmt.Errorf("%w: symlinked state parent is not allowed", ErrDeviceStore)
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: stat state parent: %v", ErrDeviceStore, err)
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return fmt.Errorf("%w: no existing state parent", ErrDeviceStore)
		}
		probe = parent
	}
}

func cloneAccounts(src map[string]map[string]DeviceRecord) map[string]map[string]DeviceRecord {
	out := make(map[string]map[string]DeviceRecord, len(src))
	for accountID, devices := range src {
		copyDevices := make(map[string]DeviceRecord, len(devices))
		for deviceID, record := range devices {
			copyDevices[deviceID] = record
		}
		out[accountID] = copyDevices
	}
	return out
}
