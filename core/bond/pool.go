package bond

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
)

// IPPool hands out sequential IPv4 addresses from a CIDR, reserving the first address
// (the network's .1) as the relay's own gateway address. Not a general-purpose IPAM: no
// fragmentation-aware reuse strategy beyond a simple free list, which is all a relay
// mapping client static keys to tunnel IPs needs.
type IPPool struct {
	mu       sync.Mutex
	network  *net.IPNet
	next     uint32
	end      uint32
	released []uint32
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
	base := binary.BigEndian.Uint32(network.IP.To4())
	ones, bits := network.Mask.Size()
	size := uint32(1) << uint(bits-ones)
	if size < 4 {
		return nil, fmt.Errorf("bond: pool cidr %q too small", cidr)
	}
	return &IPPool{
		network: network,
		next:    base + 2, // base+1 reserved for the relay gateway itself
		end:     base + size - 1,
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
		return uint32ToIP(v), nil
	}
	if p.next > p.end {
		return nil, fmt.Errorf("bond: ip pool exhausted")
	}
	v := p.next
	p.next++
	return uint32ToIP(v), nil
}

// Release returns an address to the pool.
func (p *IPPool) Release(ip net.IP) {
	ip4 := ip.To4()
	if ip4 == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.released = append(p.released, binary.BigEndian.Uint32(ip4))
}

func uint32ToIP(v uint32) net.IP {
	b := make(net.IP, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
