package bond

import (
	"fmt"
	"net"
)

// handshakeLease makes ownership of a freshly allocated tunnel address explicit while a
// handshake is being assembled. Until Publish is called, Release must be deferred by the
// caller so every pre-publication failure path returns the address to the pool.
//
// The type is intentionally tiny and single-goroutine: authenticated client handshakes are
// serialized by handshakeClientLocks before this ownership object is used.
type handshakeLease struct {
	pool  *IPPool
	ip    net.IP
	owned bool
}

func allocateHandshakeLease(pool *IPPool) (*handshakeLease, error) {
	if pool == nil {
		return nil, fmt.Errorf("bond: nil handshake ip pool")
	}
	ip, err := pool.Allocate()
	if err != nil {
		return nil, err
	}
	return &handshakeLease{pool: pool, ip: append(net.IP(nil), ip...), owned: true}, nil
}

// IP returns a defensive copy so callers cannot mutate the address later returned to the
// pool on an unsuccessful handshake.
func (l *handshakeLease) IP() net.IP {
	if l == nil {
		return nil
	}
	return append(net.IP(nil), l.ip...)
}

// Publish transfers ownership from the temporary handshake to the live relay session.
// After publication, deferred Release becomes a no-op.
func (l *handshakeLease) Publish() {
	if l == nil {
		return
	}
	l.owned = false
}

// Release returns an unpublished address exactly once. It is intentionally idempotent so
// callers can safely defer it immediately after allocation without special-case cleanup at
// each later failure site.
func (l *handshakeLease) Release() {
	if l == nil || !l.owned {
		return
	}
	l.owned = false
	l.pool.Release(l.ip)
}
