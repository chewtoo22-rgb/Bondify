package crypto

import "sync"

// replayWindowSize matches PROTOCOL.md's REPLAY_WINDOW constant (§Appendix). PROTOCOL.md
// specifies this window over GSN at the reassembly layer (core/bond, phase 2+); this
// package applies the identical window size one layer lower, directly over each path's raw
// AEAD nonce counter, as defense in depth against nonce/ciphertext replay independent of
// anything GSN-level dedup does or doesn't catch.
const replayWindowSize = 8192

// replayWindow is a sliding-window duplicate/replay filter over a monotonically-labelled
// counter space, the same structure WireGuard uses for its own transport counter.
type replayWindow struct {
	mu     sync.Mutex
	max    uint64 // highest counter accepted so far
	bits   []uint64
	inited bool
}

func newReplayWindow(size int) *replayWindow {
	return &replayWindow{bits: make([]uint64, (size+63)/64)}
}

// CheckOnly reports whether counter is acceptable (not already seen, not too old) without
// marking it seen. Session.Open no longer composes CheckOnly and MarkSeen itself because
// doing so leaves a race where two concurrent copies of the same authenticated packet can
// both pass the check before either marks the nonce. Use authenticateAndMark for receive
// admission that must be atomic with authentication.
func (w *replayWindow) CheckOnly(counter uint64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.acceptableLocked(counter)
}

func (w *replayWindow) acceptableLocked(counter uint64) bool {
	if !w.inited {
		return true
	}
	if counter > w.max {
		return true
	}
	back := w.max - counter
	if back >= uint64(len(w.bits)*64) {
		return false // too old, outside the window
	}
	word, bit := back/64, back%64
	return w.bits[word]&(1<<bit) == 0
}

// authenticateAndMark serializes replay admission and successful authentication for one
// path. Holding the window lock across authenticate is deliberate: a nonce must have one
// winner. We still do not mark the nonce until authenticate succeeds, so unauthenticated
// garbage cannot burn a legitimate future counter value.
func (w *replayWindow) authenticateAndMark(counter uint64, authenticate func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.acceptableLocked(counter) {
		return ErrReplay
	}
	if err := authenticate(); err != nil {
		return err
	}
	w.markSeenLocked(counter)
	return nil
}

// MarkSeen records counter as consumed. Callers must only call this after successful AEAD
// verification. Receive-side Session.Open uses authenticateAndMark so the check+mark pair
// cannot be raced; MarkSeen remains available to the replay-window unit tests and helpers.
func (w *replayWindow) MarkSeen(counter uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.markSeenLocked(counter)
}

func (w *replayWindow) markSeenLocked(counter uint64) {
	if !w.inited {
		w.max = counter
		w.inited = true
		w.bits[0] = 1
		return
	}
	switch {
	case counter > w.max:
		shift := counter - w.max
		if shift >= uint64(len(w.bits)*64) {
			for i := range w.bits {
				w.bits[i] = 0
			}
		} else {
			shiftWords(w.bits, shift)
		}
		w.max = counter
		w.bits[0] |= 1
	case counter == w.max:
		w.bits[0] |= 1
	default:
		back := w.max - counter
		word, bit := back/64, back%64
		if int(word) < len(w.bits) {
			w.bits[word] |= 1 << bit
		}
	}
}

// shiftWords shifts the bitset left by n bits (n may exceed 64), dropping bits that fall
// off the top end, i.e. advancing the window forward.
func shiftWords(bits []uint64, n uint64) {
	wordShift := int(n / 64)
	bitShift := uint(n % 64)
	if wordShift >= len(bits) {
		for i := range bits {
			bits[i] = 0
		}
		return
	}
	for i := len(bits) - 1; i >= 0; i-- {
		var v uint64
		if i-wordShift >= 0 {
			v = bits[i-wordShift]
			if bitShift != 0 {
				v <<= bitShift
				if i-wordShift-1 >= 0 {
					v |= bits[i-wordShift-1] >> (64 - bitShift)
				}
		}
		bits[i] = v
	}
}
