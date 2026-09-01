package enrollment

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"
)

const (
	DeviceManagementWireVersion = 1
	MaxDeviceManagementWireBytes = 16 * 1024
	MaxDeviceRevokeWireBytes     = 256
)

var ErrDeviceManagementWire = errors.New("invalid device management wire payload")

// DeviceInventory is the transport-safe account device list shared by Android
// and Windows clients. Account identity is intentionally excluded because the
// transport authenticates it separately.
type DeviceInventory struct {
	Version int                   `json:"version"`
	Devices []DeviceInventoryItem `json:"devices"`
}

// DeviceInventoryItem contains only durable, non-secret device metadata.
type DeviceInventoryItem struct {
	DeviceID   string   `json:"device_id"`
	Name       string   `json:"name"`
	Platform   Platform `json:"platform"`
	EnrolledAt string   `json:"enrolled_at"`
}

// DeviceRevokeRequest is the complete client-controlled body for revocation.
// The authenticated account identity never appears in this payload.
type DeviceRevokeRequest struct {
	DeviceID string `json:"device_id"`
}

// EncodeDeviceInventory validates, sorts, and serializes deterministic device
// inventory without exposing account IDs or enrollment credentials.
func EncodeDeviceInventory(records []DeviceRecord) ([]byte, error) {
	if len(records) > DefaultMaxDevicesPerAccount {
		return nil, ErrDeviceManagementWire
	}

	normalized := make([]DeviceRecord, 0, len(records))
	for _, record := range records {
		clean, err := normalizeDeviceRecord(record)
		if err != nil {
			return nil, ErrDeviceManagementWire
		}
		normalized = append(normalized, clean)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].DeviceID < normalized[j].DeviceID })

	items := make([]DeviceInventoryItem, 0, len(normalized))
	lastID := ""
	for _, record := range normalized {
		if lastID == record.DeviceID {
			return nil, ErrDeviceManagementWire
		}
		lastID = record.DeviceID
		items = append(items, DeviceInventoryItem{
			DeviceID:   record.DeviceID,
			Name:       record.Name,
			Platform:   record.Platform,
			EnrolledAt: record.EnrolledAt.UTC().Format(time.RFC3339Nano),
		})
	}

	encoded, err := json.Marshal(DeviceInventory{Version: DeviceManagementWireVersion, Devices: items})
	if err != nil || len(encoded) > MaxDeviceManagementWireBytes {
		return nil, ErrDeviceManagementWire
	}
	return encoded, nil
}

// DecodeDeviceRevokeWire strictly accepts exactly one validated device ID.
func DecodeDeviceRevokeWire(payload []byte) (string, error) {
	if len(payload) == 0 || len(payload) > MaxDeviceRevokeWireBytes {
		return "", ErrDeviceManagementWire
	}

	var request DeviceRevokeRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return "", ErrDeviceManagementWire
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", ErrDeviceManagementWire
	}
	if !validDeviceID(request.DeviceID) {
		return "", ErrDeviceManagementWire
	}
	return request.DeviceID, nil
}

// ListDevicesWire returns canonical account-scoped device inventory for an
// already-authenticated account.
func (s *AccountService) ListDevicesWire(accountID string) ([]byte, error) {
	records, err := s.ListDevices(accountID)
	if err != nil {
		return nil, err
	}
	return EncodeDeviceInventory(records)
}

// RevokeDeviceWire strictly decodes a device ID and revokes it only from the
// separately authenticated account.
func (s *AccountService) RevokeDeviceWire(accountID string, payload []byte) error {
	deviceID, err := DecodeDeviceRevokeWire(payload)
	if err != nil {
		return err
	}
	return s.RevokeDevice(accountID, deviceID)
}
