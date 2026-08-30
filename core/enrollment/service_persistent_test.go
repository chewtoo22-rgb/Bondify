package enrollment

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"
)

type recordingDeviceStore struct {
	mu      sync.Mutex
	err     error
	records []DeviceRecord
}

func (s *recordingDeviceStore) Put(record DeviceRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.records = append(s.records, record)
	return nil
}

func (s *recordingDeviceStore) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *recordingDeviceStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records)
}

func persistentTestRequest() Request {
	return Request{
		Version:   ProtocolVersion,
		Name:      "Matt's S22+",
		Platform:  PlatformAndroid,
		PublicKey: bytes.Repeat([]byte{0x42}, 32),
		Nonce:     bytes.Repeat([]byte{0x24}, MinNonceBytes),
	}
}

func newPersistentTestClaims(t *testing.T) (*ClaimStore, EnrollmentClaim, []byte) {
	t.Helper()
	now := time.Date(2026, 8, 29, 23, 0, 0, 0, time.UTC)
	claims, err := NewClaimStore(8, 10*time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x11}, 64)), func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewClaimStore: %v", err)
	}
	secret := bytes.Repeat([]byte{0x5a}, MinClaimSecretBytes)
	claim, err := claims.Issue("acct-1", secret)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return claims, claim, secret
}

func TestPersistentServiceRetainsClaimWhenStoreFails(t *testing.T) {
	claims, claim, secret := newPersistentTestClaims(t)
	storeFailure := errors.New("disk unavailable")
	store := &recordingDeviceStore{err: storeFailure}
	svc, err := NewPersistentService(claims, store, func() time.Time {
		return time.Date(2026, 8, 29, 23, 1, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("NewPersistentService: %v", err)
	}

	if _, err := svc.Enroll("acct-1", claim.ID, secret, persistentTestRequest()); !errors.Is(err, storeFailure) {
		t.Fatalf("first Enroll error = %v, want store failure", err)
	}
	if got := store.count(); got != 0 {
		t.Fatalf("stored records after failed persistence = %d, want 0", got)
	}

	store.setError(nil)
	record, err := svc.Enroll("acct-1", claim.ID, secret, persistentTestRequest())
	if err != nil {
		t.Fatalf("retry Enroll: %v", err)
	}
	if record.AccountID != "acct-1" || record.Platform != PlatformAndroid {
		t.Fatalf("unexpected record: %+v", record)
	}
	if got := store.count(); got != 1 {
		t.Fatalf("stored records after retry = %d, want 1", got)
	}

	if _, err := svc.Enroll("acct-1", claim.ID, secret, persistentTestRequest()); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("replay error = %v, want ErrClaimInvalid", err)
	}
}

func TestPersistentServiceConcurrentConsumeCommitsOnce(t *testing.T) {
	claims, claim, secret := newPersistentTestClaims(t)
	store := &recordingDeviceStore{}
	svc, err := NewPersistentService(claims, store, nil)
	if err != nil {
		t.Fatalf("NewPersistentService: %v", err)
	}

	const workers = 16
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Enroll("acct-1", claim.ID, secret, persistentTestRequest())
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	invalid := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrClaimInvalid):
			invalid++
		default:
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if successes != 1 || invalid != workers-1 {
		t.Fatalf("successes=%d invalid=%d, want 1/%d", successes, invalid, workers-1)
	}
	if got := store.count(); got != 1 {
		t.Fatalf("stored records = %d, want exactly 1", got)
	}
}

func TestNewPersistentServiceRejectsNilStore(t *testing.T) {
	claims, _, _ := newPersistentTestClaims(t)
	if _, err := NewPersistentService(claims, nil, nil); !errors.Is(err, ErrDeviceRecordStore) {
		t.Fatalf("error = %v, want ErrDeviceRecordStore", err)
	}
}
