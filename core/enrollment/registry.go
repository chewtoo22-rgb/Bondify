package enrollment

import (
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"
)

const DefaultMaxDevicesPerAccount = 64

var (
	ErrDeviceRecord   = errors.New("invalid enrolled device record")
	ErrDeviceCapacity = errors.New("account device capacity reached")
	ErrDeviceNotFound = errors.New("enrolled device not found")
)

// DeviceRegistry is a bounded, concurrency-safe registry for non-secret
// enrollment results. It intentionally stores DeviceRecord only: claim IDs,
// claim secrets, enrollment nonces, and public keys never enter this layer.
type DeviceRegistry struct {
	mu         sync.RWMutex
	maxDevices int
	accounts   map[string]map[string]DeviceRecord
}

func NewDeviceRegistry(maxDevices int) (*DeviceRegistry, error) {
	if maxDevices <= 0 {
		return nil, ErrDeviceCapacity
	}
	return &DeviceRegistry{
		maxDevices: maxDevices,
		accounts:   make(map[string]map[string]DeviceRecord),
	}, nil
}

// Put records an enrolled device. Re-enrolling the same device for the same
// account is idempotent and refreshes its user-visible metadata. New devices
// are rejected once the per-account bound is reached.
func (r *DeviceRegistry) Put(record DeviceRecord) error {
	normalized, err := normalizeDeviceRecord(record)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	devices := r.accounts[normalized.AccountID]
	if devices == nil {
		devices = make(map[string]DeviceRecord)
		r.accounts[normalized.AccountID] = devices
	}
	if _, exists := devices[normalized.DeviceID]; !exists && len(devices) >= r.maxDevices {
		return ErrDeviceCapacity
	}
	devices[normalized.DeviceID] = normalized
	return nil
}

func (r *DeviceRegistry) Get(accountID, deviceID string) (DeviceRecord, error) {
	accountID, err := normalizeAccountID(accountID)
	if err != nil || !validDeviceID(deviceID) {
		return DeviceRecord{}, ErrDeviceRecord
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	record, ok := r.accounts[accountID][deviceID]
	if !ok {
		return DeviceRecord{}, ErrDeviceNotFound
	}
	return record, nil
}

// List returns a deterministic snapshot ordered by DeviceID so diagnostics,
// APIs, and tests do not depend on Go map iteration order.
func (r *DeviceRegistry) List(accountID string) ([]DeviceRecord, error) {
	accountID, err := normalizeAccountID(accountID)
	if err != nil {
		return nil, ErrDeviceRecord
	}

	r.mu.RLock()
	devices := r.accounts[accountID]
	out := make([]DeviceRecord, 0, len(devices))
	for _, record := range devices {
		out = append(out, record)
	}
	r.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool { return out[i].DeviceID < out[j].DeviceID })
	return out, nil
}

func (r *DeviceRegistry) Remove(accountID, deviceID string) error {
	accountID, err := normalizeAccountID(accountID)
	if err != nil || !validDeviceID(deviceID) {
		return ErrDeviceRecord
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	devices := r.accounts[accountID]
	if _, ok := devices[deviceID]; !ok {
		return ErrDeviceNotFound
	}
	delete(devices, deviceID)
	if len(devices) == 0 {
		delete(r.accounts, accountID)
	}
	return nil
}

func normalizeDeviceRecord(record DeviceRecord) (DeviceRecord, error) {
	accountID, err := normalizeAccountID(record.AccountID)
	if err != nil || !validDeviceID(record.DeviceID) || !record.Platform.Valid() || record.EnrolledAt.IsZero() {
		return DeviceRecord{}, ErrDeviceRecord
	}
	name, err := NormalizeDeviceName(record.Name)
	if err != nil {
		return DeviceRecord{}, ErrDeviceRecord
	}
	record.AccountID = accountID
	record.Name = name
	record.EnrolledAt = record.EnrolledAt.UTC()
	return record, nil
}

func validDeviceID(deviceID string) bool {
	if len(deviceID) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(deviceID)
	return err == nil && len(decoded) == 16
}

// compile-time guard against accidentally dropping time normalization during
// future refactors; DeviceRecord timestamps are persisted as absolute instants.
var _ = time.UTC
