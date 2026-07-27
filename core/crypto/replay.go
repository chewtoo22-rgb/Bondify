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
// marking it seen — used to gate work (AEAD verification) that should not run at all for
// packets that are replays on their face.
func (w *replayWindow) CheckOnly(counter uint64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
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

// MarkSeen records counter as consumed. Callers must only call this after successful AEAD
// verification (see Session.Open) so an attacker cannot burn counter values that belong to
// a legitimate sender by sending garbage with a guessed nonce.
func (w *replayWindow) MarkSeen(counter uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
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
		}
		bits[i] = v
	}
}
