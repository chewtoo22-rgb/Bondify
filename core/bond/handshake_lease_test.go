package bond

import (
	"net"
	"testing"
)

func TestAllocateHandshakeLeaseReleaseReturnsAddress(t *testing.T) {
	pool, err := NewIPPool("10.77.0.0/30")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := allocateHandshakeLease(pool)
	if err != nil {
		t.Fatal(err)
	}
	if got := lease.IP(); !got.Equal(net.IPv4(10, 77, 0, 2)) {
		t.Fatalf("allocated %v, want 10.77.0.2", got)
	}
	if _, err := pool.Allocate(); err == nil {
		t.Fatal("expected single-client pool to be exhausted while lease is owned")
	}

	lease.Release()
	reused, err := pool.Allocate()
	if err != nil {
		t.Fatalf("released address was not reusable: %v", err)
	}
	if !reused.Equal(net.IPv4(10, 77, 0, 2)) {
		t.Fatalf("reused %v, want 10.77.0.2", reused)
	}
}

func TestHandshakeLeasePublishRetainsAddress(t *testing.T) {
	pool, err := NewIPPool("10.77.0.0/30")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := allocateHandshakeLease(pool)
	if err != nil {
		t.Fatal(err)
	}
	lease.Publish()
	lease.Release()

	if _, err := pool.Allocate(); err == nil {
		t.Fatal("published lease was incorrectly returned to the pool")
	}
}

func TestHandshakeLeaseReleaseIsIdempotent(t *testing.T) {
	pool, err := NewIPPool("10.77.0.0/30")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := allocateHandshakeLease(pool)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	lease.Release()

	first, err := pool.Allocate()
	if err != nil {
		t.Fatalf("first reuse failed: %v", err)
	}
	if !first.Equal(net.IPv4(10, 77, 0, 2)) {
		t.Fatalf("first reuse %v, want 10.77.0.2", first)
	}
	if _, err := pool.Allocate(); err == nil {
		t.Fatal("double release inserted duplicate address into free list")
	}
}

func TestHandshakeLeaseIPIsDefensiveCopy(t *testing.T) {
	pool, err := NewIPPool("10.77.0.0/30")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := allocateHandshakeLease(pool)
	if err != nil {
		t.Fatal(err)
	}
	got := lease.IP()
	got[len(got)-1] = 99
	if actual := lease.IP(); !actual.Equal(net.IPv4(10, 77, 0, 2)) {
		t.Fatalf("caller mutated lease address: %v", actual)
	}
	lease.Release()
}

func TestAllocateHandshakeLeaseRejectsNilPool(t *testing.T) {
	if _, err := allocateHandshakeLease(nil); err == nil {
		t.Fatal("expected nil pool to fail closed")
	}
}

func TestNilHandshakeLeaseOperationsAreSafe(t *testing.T) {
	var lease *handshakeLease
	if got := lease.IP(); got != nil {
		t.Fatalf("nil lease IP = %v, want nil", got)
	}
	lease.Publish()
	lease.Release()
}
