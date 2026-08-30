package enrollment

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestIssueGeneratedReturnsConsumableOneTimeSecret(t *testing.T) {
	now := time.Date(2026, 8, 30, 23, 30, 0, 0, time.UTC)
	random := bytes.NewReader(bytes.Repeat([]byte{0x6a}, GeneratedClaimSecretBytes+16))
	store, err := NewClaimStore(4, 10*time.Minute, random, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewClaimStore: %v", err)
	}

	claim, secret, err := store.IssueGenerated("acct-1")
	if err != nil {
		t.Fatalf("IssueGenerated: %v", err)
	}
	if len(secret) != GeneratedClaimSecretBytes {
		t.Fatalf("secret length = %d, want %d", len(secret), GeneratedClaimSecretBytes)
	}
	if !validClaimID(claim.ID) {
		t.Fatalf("invalid claim id %q", claim.ID)
	}
	if want := now.Add(10 * time.Minute); !claim.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", claim.ExpiresAt, want)
	}

	original := append([]byte(nil), secret...)
	secret[0] ^= 0xff
	if err := store.Consume("acct-1", claim.ID, secret); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("mutated secret error = %v, want ErrClaimInvalid", err)
	}
	if err := store.Consume("acct-1", claim.ID, original); err != nil {
		t.Fatalf("Consume generated secret: %v", err)
	}
	if err := store.Consume("acct-1", claim.ID, original); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("replay error = %v, want ErrClaimInvalid", err)
	}
}

func TestIssueGeneratedFailsClosedWhenRandomSourceFails(t *testing.T) {
	store, err := NewClaimStore(4, 10*time.Minute, bytes.NewReader(make([]byte, GeneratedClaimSecretBytes-1)), nil)
	if err != nil {
		t.Fatalf("NewClaimStore: %v", err)
	}

	claim, secret, err := store.IssueGenerated("acct-1")
	if !errors.Is(err, ErrClaimRandom) {
		t.Fatalf("IssueGenerated error = %v, want ErrClaimRandom", err)
	}
	if claim != (EnrollmentClaim{}) {
		t.Fatalf("claim = %+v, want zero value", claim)
	}
	if secret != nil {
		t.Fatalf("secret returned on random failure")
	}
	if len(store.claims) != 0 {
		t.Fatalf("claims created after random failure = %d, want 0", len(store.claims))
	}
}

func TestIssueGeneratedCapacityFailureReturnsNoSecret(t *testing.T) {
	random := bytes.NewReader(bytes.Repeat([]byte{0x7b}, 2*(GeneratedClaimSecretBytes+16)))
	store, err := NewClaimStore(1, 10*time.Minute, random, nil)
	if err != nil {
		t.Fatalf("NewClaimStore: %v", err)
	}
	if _, _, err := store.IssueGenerated("acct-1"); err != nil {
		t.Fatalf("first IssueGenerated: %v", err)
	}

	claim, secret, err := store.IssueGenerated("acct-2")
	if !errors.Is(err, ErrClaimCapacity) {
		t.Fatalf("second IssueGenerated error = %v, want ErrClaimCapacity", err)
	}
	if claim != (EnrollmentClaim{}) || secret != nil {
		t.Fatalf("capacity failure leaked claim=%+v secretLen=%d", claim, len(secret))
	}
}
