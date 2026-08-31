package enrollment

import "errors"

var ErrDeviceDirectory = errors.New("device directory unavailable")

// DeviceDirectory is the account-facing, non-secret device inventory boundary.
// DeviceRegistry and FileDeviceStore both satisfy it, allowing account APIs to
// list and revoke enrolled devices without knowing how inventory is persisted.
type DeviceDirectory interface {
	List(accountID string) ([]DeviceRecord, error)
	Remove(accountID, deviceID string) error
}

// DeviceManager exposes the bounded management operations needed after initial
// enrollment. It deliberately has no access to claim secrets or device keys.
type DeviceManager struct {
	directory DeviceDirectory
}

func NewDeviceManager(directory DeviceDirectory) (*DeviceManager, error) {
	if directory == nil {
		return nil, ErrDeviceDirectory
	}
	return &DeviceManager{directory: directory}, nil
}

// ListDevices returns the deterministic account-scoped inventory supplied by
// the directory implementation.
func (m *DeviceManager) ListDevices(accountID string) ([]DeviceRecord, error) {
	if m == nil || m.directory == nil {
		return nil, ErrDeviceDirectory
	}
	return m.directory.List(accountID)
}

// RevokeDevice removes one enrolled device from an account. With
// FileDeviceStore the removal is atomic and durable; with DeviceRegistry it is
// an in-memory operation suitable for tests and ephemeral deployments.
func (m *DeviceManager) RevokeDevice(accountID, deviceID string) error {
	if m == nil || m.directory == nil {
		return ErrDeviceDirectory
	}
	return m.directory.Remove(accountID, deviceID)
}
