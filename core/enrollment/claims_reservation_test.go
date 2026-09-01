package enrollment

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestConsumeAndCommitDoesNotBlockUnrelatedClaims(t *testing.T) {
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	randomBytes := append(bytes.Repeat([]byte{0x71}, 16), bytes.Repeat([]byte{0x72}, 16)...)
	store, err := NewClaimStore(8, 10*time.Minute, bytes.NewReader(randomBytes), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Issue("acct-1", testSecret)
	if err != nil {
		t.Fatal(err)
	}

	commitStarted := make(chan struct{})
	releaseCommit := make(chan struct{})
	consumeDone := make(chan error, 1)
	go func() {
		consumeDone <- store.ConsumeAndCommit("acct-1", claim.ID, testSecret, func() error {
			close(commitStarted)
			<-releaseCommit
			return nil
		})
	}()
	<-commitStarted

	issueDone := make(chan error, 1)
	go func() {
		_, err := store.Issue("acct-2", testSecret)
		issueDone <- err
	}()

	select {
	case err := <-issueDone:
		if err != nil {
			t.Fatalf("unrelated issue failed while commit reserved: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated claim issue blocked behind durable commit")
	}

	close(releaseCommit)
	if err := <-consumeDone; err != nil {
		t.Fatalf("consume-and-commit failed: %v", err)
	}
}

func TestReservedClaimRejectsConcurrentConsumeAndRevoke(t *testing.T) {
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	store, err := NewClaimStore(8, 10*time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x72}, 16)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Issue("acct-1", testSecret)
	if err != nil {
		t.Fatal(err)
	}

	commitStarted := make(chan struct{})
	releaseCommit := make(chan struct{})
	consumeDone := make(chan error, 1)
	go func() {
		consumeDone <- store.ConsumeAndCommit("acct-1", claim.ID, testSecret, func() error {
			close(commitStarted)
			<-releaseCommit
			return nil
		})
	}()
	<-commitStarted

	if err := store.Consume("acct-1", claim.ID, testSecret); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("concurrent consume err=%v want ErrClaimInvalid", err)
	}
	if err := store.Revoke("acct-1", claim.ID); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("revoke during reservation err=%v want ErrClaimInvalid", err)
	}

	close(releaseCommit)
	if err := <-consumeDone; err != nil {
		t.Fatalf("reserved consume failed: %v", err)
	}
	if err := store.Consume("acct-1", claim.ID, testSecret); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("replay err=%v want ErrClaimInvalid", err)
	}
}

func TestFailedCommitReleasesReservationForRetry(t *testing.T) {
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	store, err := NewClaimStore(8, 10*time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x73}, 16)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Issue("acct-1", testSecret)
	if err != nil {
		t.Fatal(err)
	}

	persistErr := errors.New("persist failed")
	if err := store.ConsumeAndCommit("acct-1", claim.ID, testSecret, func() error { return persistErr }); !errors.Is(err, persistErr) {
		t.Fatalf("first commit err=%v want persist failure", err)
	}
	if err := store.ConsumeAndCommit("acct-1", claim.ID, testSecret, func() error { return nil }); err != nil {
		t.Fatalf("retry after failed commit: %v", err)
	}
}

func TestFailedCommitBurnsClaimIfItExpiresWhileReserved(t *testing.T) {
	now := time.Date(2026, 9, 1, 16, 0, 0, 0, time.UTC)
	store, err := NewClaimStore(8, time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x74}, 16)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Issue("acct-1", testSecret)
	if err != nil {
		t.Fatal(err)
	}

	persistErr := errors.New("persist failed")
	if err := store.ConsumeAndCommit("acct-1", claim.ID, testSecret, func() error {
		now = now.Add(time.Minute)
		return persistErr
	}); !errors.Is(err, persistErr) {
		t.Fatalf("commit err=%v want persist failure", err)
	}
	if err := store.Consume("acct-1", claim.ID, testSecret); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("expired retry err=%v want ErrClaimInvalid", err)
	}
}
