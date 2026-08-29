package enrollment

import (
	"bytes"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testSecret = []byte("0123456789abcdef")

func TestClaimIssueConsumeAndReplay(t *testing.T) {
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	store, err := NewClaimStore(8, 10*time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x11}, 16)), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.Issue("acct-1", testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if claim.ID != "11111111111111111111111111111111" {
		t.Fatalf("unexpected claim id: %s", claim.ID)
	}
	if want := now.Add(10 * time.Minute); !claim.ExpiresAt.Equal(want) {
		t.Fatalf("expiresAt=%s want=%s", claim.ExpiresAt, want)
	}
	if err := store.Consume("acct-1", claim.ID, testSecret); err != nil {
		t.Fatalf("consume failed: %v", err)
	}
	if err := store.Consume("acct-1", claim.ID, testSecret); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("replay err=%v want ErrClaimInvalid", err)
	}
}

func TestWrongSecretOrAccountDoesNotConsume(t *testing.T) {
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	store, _ := NewClaimStore(8, 10*time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x22}, 16)), func() time.Time { return now })
	claim, err := store.Issue("acct-1", testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Consume("acct-2", claim.ID, testSecret); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("wrong account err=%v", err)
	}
	if err := store.Consume("acct-1", claim.ID, []byte("fedcba9876543210")); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("wrong secret err=%v", err)
	}
	if err := store.Consume("acct-1", claim.ID, testSecret); err != nil {
		t.Fatalf("valid consume after failures: %v", err)
	}
}

func TestExpiredClaimRejectedAndCapacityRecovered(t *testing.T) {
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	random := append(bytes.Repeat([]byte{0x33}, 16), bytes.Repeat([]byte{0x44}, 16)...)
	store, _ := NewClaimStore(1, time.Minute, bytes.NewReader(random), func() time.Time { return now })
	first, err := store.Issue("acct-1", testSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Issue("acct-1", testSecret); !errors.Is(err, ErrClaimCapacity) {
		t.Fatalf("capacity err=%v want ErrClaimCapacity", err)
	}
	now = now.Add(time.Minute)
	if err := store.Consume("acct-1", first.ID, testSecret); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("expired consume err=%v", err)
	}
	if _, err := store.Issue("acct-1", testSecret); err != nil {
		t.Fatalf("capacity should recover after expiry: %v", err)
	}
}

func TestConcurrentConsumeIsSingleUse(t *testing.T) {
	now := time.Date(2026, 8, 29, 9, 0, 0, 0, time.UTC)
	store, _ := NewClaimStore(8, 10*time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x55}, 16)), func() time.Time { return now })
	claim, err := store.Issue("acct-1", testSecret)
	if err != nil {
		t.Fatal(err)
	}

	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if store.Consume("acct-1", claim.ID, testSecret) == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("successful consumes=%d want=1", got)
	}
}

func TestClaimValidationFailsClosed(t *testing.T) {
	if _, err := NewClaimStore(0, time.Minute, nil, nil); !errors.Is(err, ErrClaimConfig) {
		t.Fatalf("max claims config err=%v", err)
	}
	if _, err := NewClaimStore(1, 30*time.Second, nil, nil); !errors.Is(err, ErrClaimConfig) {
		t.Fatalf("ttl config err=%v", err)
	}

	store, _ := NewClaimStore(1, time.Minute, bytes.NewReader(bytes.Repeat([]byte{0x66}, 16)), nil)
	if _, err := store.Issue("acct\n2", testSecret); !errors.Is(err, ErrAccountID) {
		t.Fatalf("account id err=%v", err)
	}
	if _, err := store.Issue("acct-1", []byte("too-short")); !errors.Is(err, ErrClaimSecret) {
		t.Fatalf("secret err=%v", err)
	}
	if err := store.Consume("acct-1", "not-a-claim", testSecret); !errors.Is(err, ErrClaimInvalid) {
		t.Fatalf("claim id err=%v", err)
	}
}
