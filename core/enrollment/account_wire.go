package enrollment

// IssueEnrollmentGrant issues a one-time claim for an authenticated account and
// returns the canonical cross-platform wire representation. Transport layers
// should return these bytes directly rather than re-encoding claim secrets.
func (s *AccountService) IssueEnrollmentGrant(accountID string) ([]byte, error) {
	claim, secret, err := s.IssueEnrollmentClaim(accountID)
	if err != nil {
		return nil, err
	}
	return EncodeClaimGrant(claim, secret)
}

// EnrollDeviceWire strictly decodes a platform-neutral enrollment payload and
// enrolls it under the separately authenticated account identity. Account
// identity is intentionally never accepted from the wire payload.
func (s *AccountService) EnrollDeviceWire(accountID string, payload []byte) (DeviceRecord, error) {
	claimID, secret, request, err := DecodeEnrollmentWire(payload)
	if err != nil {
		return DeviceRecord{}, err
	}
	return s.EnrollDevice(accountID, claimID, secret, request)
}
