package enrollment

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

const deviceStoreVersion = 1

var ErrDeviceStore = errors.New("device store unavailable")

type deviceStoreFile struct {
	Version int            `json:"version"`
	Devices []DeviceRecord `json:"devices"`
}

// FileDeviceStore persists the bounded, non-secret DeviceRegistry using an
// atomic write/rename cycle. Enrollment claims, secrets, nonces, and public
// keys never enter this file.
type FileDeviceStore struct {
	mu       sync.Mutex
	path     string
	registry *DeviceRegistry
}

func OpenFileDeviceStore(path string, maxDevices int) (*FileDeviceStore, error) {
	if path == "" {
		return nil, ErrDeviceStore
	}
	registry, err := NewDeviceRegistry(maxDevices)
	if err != nil {
		return nil, err
	}
	store := &FileDeviceStore{path: path, registry: registry}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *FileDeviceStore) Put(record DeviceRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := cloneAccounts(s.registry.accounts)
	if err := s.registry.Put(record); err != nil {
		return err
	}
	if err := s.persist(); err != nil {
		s.registry.accounts = before
		return err
	}
	return nil
}

func (s *FileDeviceStore) Get(accountID, deviceID string) (DeviceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registry.Get(accountID, deviceID)
}

func (s *FileDeviceStore) List(accountID string) ([]DeviceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.registry.List(accountID)
}

func (s *FileDeviceStore) Remove(accountID, deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := cloneAccounts(s.registry.accounts)
	if err := s.registry.Remove(accountID, deviceID); err != nil {
		return err
	}
	if err := s.persist(); err != nil {
		s.registry.accounts = before
		return err
	}
	return nil
}

func (s *FileDeviceStore) load() error {
	info, err := os.Lstat(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("%w: stat: %v", ErrDeviceStore, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: store path must be a regular file", ErrDeviceStore)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("%w: read: %v", ErrDeviceStore, err)
	}
	var disk deviceStoreFile
	if err := json.Unmarshal(data, &disk); err != nil || disk.Version != deviceStoreVersion {
		return fmt.Errorf("%w: invalid or unsupported store", ErrDeviceStore)
	}
	for _, record := range disk.Devices {
		if err := s.registry.Put(record); err != nil {
			return fmt.Errorf("%w: invalid record: %v", ErrDeviceStore, err)
		}
	}
	return nil
}

func (s *FileDeviceStore) persist() error {
	s.registry.mu.RLock()
	devices := make([]DeviceRecord, 0)
	for _, account := range s.registry.accounts {
		for _, record := range account {
			devices = append(devices, record)
		}
	}
	s.registry.mu.RUnlock()
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
	tmp, err := os.CreateTemp(dir, ".bondify-devices-*")
	if err != nil {
		return fmt.Errorf("%w: create temp: %v", ErrDeviceStore, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: chmod temp: %v", ErrDeviceStore, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: write: %v", ErrDeviceStore, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("%w: sync: %v", ErrDeviceStore, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: close: %v", ErrDeviceStore, err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("%w: replace: %v", ErrDeviceStore, err)
	}
	return nil
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
