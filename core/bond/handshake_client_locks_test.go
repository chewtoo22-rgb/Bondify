package bond

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestHandshakeClientLockIndexStableAndBounded(t *testing.T) {
	var key [32]byte
	key[0], key[7], key[15], key[31] = 0x11, 0x22, 0x44, 0x88
	got := handshakeClientLockIndex(key)
	if got < 0 || got >= handshakeClientLockStripes {
		t.Fatalf("index %d outside [0,%d)", got, handshakeClientLockStripes)
	}
	if again := handshakeClientLockIndex(key); again != got {
		t.Fatalf("index not deterministic: %d then %d", got, again)
	}
}

func TestHandshakeClientLocksSerializeSameClient(t *testing.T) {
	var locks handshakeClientLocks
	var key [32]byte
	key[0] = 1

	unlock := locks.lock(key)
	entered := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		unlockSecond := locks.lock(key)
		close(entered)
		unlockSecond()
	}()

	select {
	case <-entered:
		t.Fatal("same-client handshake entered while first lock was held")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("same-client handshake did not proceed after unlock")
	}
	<-done
}

func TestHandshakeClientLocksAllowIndependentStripes(t *testing.T) {
	var locks handshakeClientLocks
	var first, second [32]byte
	first[0] = 1
	second[0] = 2
	if handshakeClientLockIndex(first) == handshakeClientLockIndex(second) {
		t.Fatal("test keys unexpectedly share a stripe")
	}

	unlockFirst := locks.lock(first)
	defer unlockFirst()

	var entered atomic.Bool
	done := make(chan struct{})
	go func() {
		unlockSecond := locks.lock(second)
		entered.Store(true)
		unlockSecond()
		close(done)
	}()

	select {
	case <-done:
		if !entered.Load() {
			t.Fatal("independent stripe completed without entering critical section")
		}
	case <-time.After(time.Second):
		t.Fatal("independent client stripe was unnecessarily blocked")
	}
}
