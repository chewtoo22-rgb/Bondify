package enrollment

import (
	"errors"
	"time"
)

var (
	ErrEnrollmentStore = errors.New("enrollment claim store unavailable")
	ErrDeviceRecordStore = errors.New("device record store unavailable")
)

// DeviceRecord is the durable, non-secret result of a successful enrollment
// exchange. Authentication credentials are intentionally not carried forward.
type DeviceRecord struct {
	AccountID  string
	DeviceID   string
	Name       string
	Platform   Platform
	EnrolledAt time.Time
}

// DeviceRecordStore is the minimal durable sink needed by the enrollment
// service. FileDeviceStore satisfies this contract; future database-backed
// stores can do the same without changing enrollment semantics.
type DeviceRecordStore interface {
	Put(DeviceRecord) error
}

// Service joins the validated device identity contract with one-time account
// claims. It remains transport-agnostic so Android/Windows clients and a future
// account API can share the same enrollment rules.
type Service struct {
	claims *ClaimStore
	store  DeviceRecordStore
	now    func() time.Time
}

func NewService(claims *ClaimStore, now func() time.Time) (*Service, error) {
	if claims == nil {
		return nil, ErrEnrollmentStore
	}
	if now == nil {
		now = time.Now
	}
	return &Service{claims: claims, now: now}, nil
}

// NewPersistentService requires a durable device-record sink and guarantees
// that a one-time claim is not burned unless the resulting record is committed.
func NewPersistentService(claims *ClaimStore, store DeviceRecordStore, now func() time.Time) (*Service, error) {
	if claims == nil {
		return nil, ErrEnrollmentStore
	}
	if store == nil {
		return nil, ErrDeviceRecordStore
	}
	if now == nil {
		now = time.Now
	}
	return &Service{claims: claims, store: store, now: now}, nil
}

// Enroll validates the device request before consuming the one-time claim. A
// malformed device therefore cannot burn a valid enrollment claim. When a
// durable store is configured, the record is committed while the claim is
// exclusively reserved and the claim is deleted only after persistence succeeds.
func (s *Service) Enroll(accountID, claimID string, secret []byte, request Request) (DeviceRecord, error) {
	if err := request.Validate(); err != nil {
		return DeviceRecord{}, err
	}

	name, err := NormalizeDeviceName(request.Name)
	if err != nil {
		return DeviceRecord{}, err
	}
	deviceID, err := DeviceID(request.PublicKey)
	if err != nil {
		return DeviceRecord{}, err
	}
	accountID, err = normalizeAccountID(accountID)
	if err != nil {
		return DeviceRecord{}, err
	}

	record := DeviceRecord{
		AccountID:  accountID,
		DeviceID:   deviceID,
		Name:       name,
		Platform:   request.Platform,
		EnrolledAt: s.now().UTC(),
	}

	if s.store == nil {
		if err := s.claims.Consume(accountID, claimID, secret); err != nil {
			return DeviceRecord{}, err
		}
		return record, nil
	}

	if err := s.claims.ConsumeAndCommit(accountID, claimID, secret, func() error {
		return s.store.Put(record)
	}); err != nil {
		return DeviceRecord{}, err
	}
	return record, nil
}
