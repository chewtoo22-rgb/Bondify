package bond

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
)

// IPPool hands out sequential IPv4 addresses from a CIDR, reserving the first usable
// address (the network's .1) as the relay's own gateway and never allocating the subnet's
// network or broadcast address. Not a general-purpose IPAM: released addresses are reused
// from a simple free list, which is sufficient for the relay's client-key-to-tunnel-IP map.
type IPPool struct {
	mu        sync.Mutex
	network   *net.IPNet
	next      uint32
	end       uint32
	released  []uint32
	allocated map[uint32]struct{}
}

func NewIPPool(cidr string) (*IPPool, error) {
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("bond: bad pool cidr %q: %w", cidr, err)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("bond: pool cidr %q is not IPv4", cidr)
	}
	networkIP4 := network.IP.To4()
	if networkIP4 == nil {
		return nil, fmt.Errorf("bond: pool cidr %q is not IPv4", cidr)
	}
	// net.ParseCIDR accepts host-address spellings such as 10.77.0.42/24 and silently
	// normalizes network.IP to 10.77.0.0. A relay configuration should describe the exact
	// subnet it will serve rather than relying on that implicit rewrite, because the pushed
	// gateway/prefix and release diagnostics otherwise disagree with the operator's input.
	// Require the CIDR address itself to be the canonical network address and fail closed on
	// accidental host bits.
	if !ip4.Equal(networkIP4) {
		return nil, fmt.Errorf("bond: pool cidr %q has host bits set (network is %s)", cidr, network.String())
	}
	base := binary.BigEndian.Uint32(networkIP4)
	ones, bits := network.Mask.Size()
	size := uint32(1) << uint(bits-ones)
	// We reserve network, gateway, and broadcast, leaving at least one client address.
	if size < 4 {
		return nil, fmt.Errorf("bond: pool cidr %q too small", cidr)
	}
	return &IPPool{
		network:   network,
		next:      base + 2,        // base+1 reserved for the relay gateway itself
		end:       base + size - 2, // base+size-1 is the IPv4 broadcast address
		allocated: make(map[uint32]struct{}),
	}, nil
}

// Gateway returns the relay's own tunnel IP (network base + 1).
func (p *IPPool) Gateway() net.IP {
	base := binary.BigEndian.Uint32(p.network.IP.To4())
	return uint32ToIP(base + 1)
}

// Prefix returns the pool's subnet prefix length (e.g. 24 for a /24).
func (p *IPPool) Prefix() int {
	ones, _ := p.network.Mask.Size()
	return ones
}

// Allocate hands out the next free address, preferring released addresses (LIFO) over new
// ones so a churning set of clients doesn't monotonically exhaust a small pool.
func (p *IPPool) Allocate() (net.IP, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if n := len(p.released); n > 0 {
		v := p.released[n-1]
		p.released = p.released[:n-1]
		p.allocated[v] = struct{}{}
		return uint32ToIP(v), nil
	}
	if p.next > p.end {
		return nil, fmt.Errorf("bond: ip pool exhausted")
	}
	v := p.next
	p.next++
	p.allocated[v] = struct{}{}
	return uint32ToIP(v), nil
}

// Release returns an address previously handed out by Allocate to the pool. Unknown,
// out-of-range, gateway/broadcast, duplicate, and alternate-address-family releases are
// ignored so a cleanup bug cannot poison the free list or release a live IPv4 lease through
// an IPv4-mapped IPv6 alias. Allocate always returns canonical 4-byte IPv4 addresses, so
// release requires that exact representation too.
func (p *IPPool) Release(ip net.IP) {
	if len(ip) != net.IPv4len {
		return
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return
	}
	v := binary.BigEndian.Uint32(ip4)
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.allocated[v]; !ok {
		return
	}
	delete(p.allocated, v)
	p.released = append(p.released, v)
}

func uint32ToIP(v uint32) net.IP {
	b := make(net.IP, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
