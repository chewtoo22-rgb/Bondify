package bond

import "sync"

const handshakeClientLockStripes = 64

// handshakeClientLocks serializes first-handshake publication for the same
// authenticated client identity without holding the relay's global session mutex
// across cryptographic response construction. The fixed stripe table is bounded,
// so hostile client-key churn cannot grow a lock map without limit.
type handshakeClientLocks struct {
	stripes [handshakeClientLockStripes]sync.Mutex
}

// lock returns an unlock function for the stripe assigned to clientKey.
// Callers must authenticate the key before using it; this helper is deliberately
// allocation-free and carries no client lifecycle state of its own.
func (l *handshakeClientLocks) lock(clientKey [32]byte) func() {
	stripe := &l.stripes[handshakeClientLockIndex(clientKey)]
	stripe.Lock()
	return stripe.Unlock
}

func handshakeClientLockIndex(clientKey [32]byte) int {
	// Mix four independent key bytes rather than using only the first byte. This
	// does not need cryptographic hashing: the authenticated key is already
	// uniformly distributed, and the mask is valid because stripe count is a
	// power of two.
	mixed := clientKey[0] ^ clientKey[7] ^ clientKey[15] ^ clientKey[31]
	return int(mixed) & (handshakeClientLockStripes - 1)
}
