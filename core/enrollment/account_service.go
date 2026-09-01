package enrollment

import "time"

// AccountDeviceStore is the release-facing persistence boundary for account
// enrollment. FileDeviceStore and DeviceRegistry both satisfy it.
type AccountDeviceStore interface {
	DeviceRecordStore
	DeviceDirectory
}

// AccountService composes claim issuance, durable enrollment, device inventory,
// and revocation behind one account-facing boundary. Transport layers should
// authenticate the account separately and then call these methods; claim
// secrets remain short-lived credentials and never enter device inventory.
type AccountService struct {
	claims     *ClaimStore
	enrollment *Service
	devices    *DeviceManager
}

// NewAccountService constructs the complete account/device enrollment surface.
// A durable AccountDeviceStore such as FileDeviceStore should be used in release
// deployments. DeviceRegistry remains useful for tests and ephemeral runs.
func NewAccountService(claims *ClaimStore, store AccountDeviceStore, now func() time.Time) (*AccountService, error) {
	if claims == nil {
		return nil, ErrEnrollmentStore
	}
	if store == nil {
		return nil, ErrDeviceRecordStore
	}

	enrollmentService, err := NewPersistentService(claims, store, now)
	if err != nil {
		return nil, err
	}
	deviceManager, err := NewDeviceManager(store)
	if err != nil {
		return nil, err
	}

	return &AccountService{
		claims:     claims,
		enrollment: enrollmentService,
		devices:    deviceManager,
	}, nil
}

// IssueEnrollmentClaim returns a one-time claim and its plaintext secret.
// Only the hash is retained by ClaimStore; callers must avoid logging or
// persisting the returned secret.
func (s *AccountService) IssueEnrollmentClaim(accountID string) (EnrollmentClaim, []byte, error) {
	if s == nil || s.claims == nil {
		return EnrollmentClaim{}, nil, ErrEnrollmentStore
	}
	return s.claims.IssueGenerated(accountID)
}

// RevokeEnrollmentClaim cancels an outstanding one-time claim for the
// separately authenticated account. This is safe to call without presenting
// the claim secret because account authentication is the authority boundary.
func (s *AccountService) RevokeEnrollmentClaim(accountID, claimID string) error {
	if s == nil || s.claims == nil {
		return ErrEnrollmentStore
	}
	return s.claims.Revoke(accountID, claimID)
}

// EnrollDevice validates the device identity, commits the durable record, and
// consumes the one-time claim only after persistence succeeds.
func (s *AccountService) EnrollDevice(accountID, claimID string, secret []byte, request Request) (DeviceRecord, error) {
	if s == nil || s.enrollment == nil {
		return DeviceRecord{}, ErrEnrollmentStore
	}
	return s.enrollment.Enroll(accountID, claimID, secret, request)
}

// ListDevices returns deterministic account-scoped device inventory.
func (s *AccountService) ListDevices(accountID string) ([]DeviceRecord, error) {
	if s == nil || s.devices == nil {
		return nil, ErrDeviceDirectory
	}
	return s.devices.ListDevices(accountID)
}

// RevokeDevice atomically removes one enrolled device from the account store.
func (s *AccountService) RevokeDevice(accountID, deviceID string) error {
	if s == nil || s.devices == nil {
		return ErrDeviceDirectory
	}
	return s.devices.RevokeDevice(accountID, deviceID)
}
