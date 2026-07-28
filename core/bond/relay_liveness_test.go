package bond

import (
	"testing"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/sched"
)

func TestUpdateRelayPathLivenessTransitions(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	p := NewPath(1, nil)
	p.SetActive()
	p.lastProbeAckAt.Store(base.UnixNano())

	updateRelayPathLiveness(p, base.Add(RelayPathDegradeTimeout-time.Nanosecond))
	if got := p.State(); got != sched.StateActive {
		t.Fatalf("state immediately before degrade threshold = %s, want ACTIVE", got)
	}

	updateRelayPathLiveness(p, base.Add(RelayPathDegradeTimeout))
	if got := p.State(); got != sched.StateDegraded {
		t.Fatalf("state at degrade threshold = %s, want DEGRADED", got)
	}

	updateRelayPathLiveness(p, base.Add(PathDeadTimeout-time.Nanosecond))
	if got := p.State(); got != sched.StateDegraded {
		t.Fatalf("state immediately before dead threshold = %s, want DEGRADED", got)
	}

	updateRelayPathLiveness(p, base.Add(PathDeadTimeout))
	if got := p.State(); got != sched.StateDead {
		t.Fatalf("state at dead threshold = %s, want DEAD", got)
	}
}

func TestUpdateRelayPathLivenessIgnoresNeverActivePath(t *testing.T) {
	p := NewPath(1, nil)
	updateRelayPathLiveness(p, time.Unix(1_700_000_000, 0))
	if got := p.State(); got != sched.StateJoining {
		t.Fatalf("never-active path state = %s, want JOINING", got)
	}
}

func TestRestoreRelayPathFromAuthenticatedProbe(t *testing.T) {
	for _, initial := range []sched.State{sched.StateDegraded, sched.StateDead} {
		t.Run(initial.String(), func(t *testing.T) {
			p := NewPath(1, nil)
			p.state.Store(int32(initial))

			restoreRelayPathFromProbe(p, false)

			if got := p.State(); got != sched.StateActive {
				t.Fatalf("restored path state = %s, want ACTIVE", got)
			}
		})
	}
}

func TestRestoreRelayPathDoesNotAuthorizeUnknownPath(t *testing.T) {
	p := NewPath(1, nil)

	restoreRelayPathFromProbe(p, true)

	if got := p.State(); got != sched.StateJoining {
		t.Fatalf("unknown path state = %s, want JOINING", got)
	}
}
