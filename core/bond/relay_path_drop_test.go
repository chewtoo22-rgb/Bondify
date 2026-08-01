package bond

import (
	"testing"

	"github.com/chewtoo22-rgb/bondify/core/sched"
)

func TestRelaySessionRemovePath(t *testing.T) {
	rs := &relaySession{paths: make(map[uint8]*Path)}
	rs.schedPathView.Store([]sched.Path(nil))

	if _, isNew := rs.getOrCreatePath(0, nil); !isNew {
		t.Fatal("path 0 should be new")
	}
	if _, isNew := rs.getOrCreatePath(1, nil); !isNew {
		t.Fatal("path 1 should be new")
	}
	if got := len(rs.schedPaths()); got != 2 {
		t.Fatalf("schedPaths() = %d, want 2", got)
	}

	removed := rs.removePath(1)
	if removed == nil || removed.ID() != 1 {
		t.Fatalf("removePath(1) = %v, want path 1", removed)
	}
	if got := len(rs.schedPaths()); got != 1 {
		t.Fatalf("schedPaths() after remove = %d, want 1", got)
	}
	if rs.pathByID(1) != nil {
		t.Fatal("path 1 should no longer be registered")
	}
	if rs.pathByID(0) == nil {
		t.Fatal("path 0 should be unaffected")
	}

	if rs.removePath(1) != nil {
		t.Fatal("removing an already-removed path should be a no-op, not resurrect it")
	}
}
