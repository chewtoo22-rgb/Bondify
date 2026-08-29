package enrollment

import (
	"errors"
	"time"
)

var (
	ErrEnrollmentStore = errors.New("enrollment claim store unavailable")
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

// Service joins the validated device identity contract with one-time account
// claims. It remains transport- and persistence-agnostic so Android/Windows
// clients and a future account API can share the same enrollment rules.
type Service struct {
	claims *ClaimStore
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

// Enroll validates the device request before consuming the one-time claim. A
// malformed device therefore cannot burn a valid enrollment claim. Successful
// claim consumption remains atomic and one-shot inside ClaimStore.
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

	if err := s.claims.Consume(accountID, claimID, secret); err != nil {
		return DeviceRecord{}, err
	}

	return DeviceRecord{
		AccountID:  accountID,
		DeviceID:   deviceID,
		Name:       name,
		Platform:   request.Platform,
		EnrolledAt: s.now().UTC(),
	}, nil
}
