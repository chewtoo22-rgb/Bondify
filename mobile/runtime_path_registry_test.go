package mobile

import (
	"fmt"
	"testing"
)

func TestRuntimePathRegistryRejectsDuplicatePendingLabel(t *testing.T) {
	r := newRuntimePathRegistry([]string{"wifi"})
	id, err := r.reserve("cellular")
	if err != nil {
		t.Fatalf("first reserve failed: %v", err)
	}
	if id != 1 {
		t.Fatalf("first runtime id = %d; want 1", id)
	}
	if _, err := r.reserve("cellular"); err == nil {
		t.Fatal("duplicate pending label unexpectedly reserved")
	}
}

func TestRuntimePathRegistryReleaseMakesFailedReservationRetriable(t *testing.T) {
	r := newRuntimePathRegistry([]string{"wifi"})
	id, err := r.reserve("cellular")
	if err != nil {
		t.Fatal(err)
	}
	r.release("cellular", id)
	if _, err := r.reserve("cellular"); err != nil {
		t.Fatalf("released label was not retriable: %v", err)
	}
}

func TestRuntimePathRegistryWrapSkipsActiveAndPendingIDs(t *testing.T) {
	r := newRuntimePathRegistry(nil)
	r.labelToID["wifi"] = 0
	r.labelToID["cellular"] = 255
	r.pendingLabels["ethernet"] = 1
	r.pendingIDs[1] = struct{}{}
	r.nextID = 255

	id, err := r.reserve("usb")
	if err != nil {
		t.Fatalf("reserve across wrap failed: %v", err)
	}
	if id != 2 {
		t.Fatalf("wrapped reservation = %d; want 2 after skipping 255, 0, and 1", id)
	}
}

func TestRuntimePathRegistryCommitMakesLabelActive(t *testing.T) {
	r := newRuntimePathRegistry(nil)
	id, err := r.reserve("wifi")
	if err != nil {
		t.Fatal(err)
	}
	r.commit("wifi", id)
	got, ok := r.lookup("wifi")
	if !ok || got != id {
		t.Fatalf("committed lookup = (%d, %v); want (%d, true)", got, ok, id)
	}
	if _, err := r.reserve("wifi"); err == nil {
		t.Fatal("active label unexpectedly reserved again")
	}
}

func TestRuntimePathRegistryFailsClosedWhenIDSpaceExhausted(t *testing.T) {
	r := newRuntimePathRegistry(nil)
	for i := 0; i < 256; i++ {
		r.labelToID[fmt.Sprintf("path-%d", i)] = uint8(i)
	}
	if _, err := r.reserve("overflow"); err == nil {
		t.Fatal("reserve unexpectedly succeeded with all 256 path IDs active")
	}
}
