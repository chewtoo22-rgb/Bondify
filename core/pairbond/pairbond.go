// Package pairbond contains the security-sensitive local pairing primitives used by
// Bondify share mode. It deliberately does not auto-discover or auto-trust peers.
package pairbond

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"sync"
	"time"
)

const (
	PairPort = 51821
	CodeLen  = 6
	MaxPeers = 4
	CodeTTL  = 5 * time.Minute
)

const codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	ErrBadCode       = errors.New("pairbond: invalid or expired pairing code")
	ErrCodeUsed      = errors.New("pairbond: pairing code already used")
	ErrTooManyPeers  = errors.New("pairbond: too many paired peers")
	ErrNotPaired     = errors.New("pairbond: peer not paired")
	ErrAlreadyPaired = errors.New("pairbond: already paired with this peer")
	ErrInvalidPeer   = errors.New("pairbond: invalid peer")
)

const (
	MsgPairRequest byte = 0x01
	MsgPairAccept  byte = 0x02
	MsgPairReject  byte = 0x03
	MsgRevoke      byte = 0x04
	MsgKeepalive   byte = 0x05
	MsgData        byte = 0x10
)

type PairRequest struct {
	Code    string
	PeerID  string
	PeerPub [32]byte
	Version uint8
}

type PairAccept struct {
	SessionHint uint32
	RelayHost   string
}

type PairReject struct{ Reason string }

func MarshalHeader(msgType byte, payloadLen uint16) []byte {
	b := make([]byte, 4)
	b[0] = msgType
	binary.BigEndian.PutUint16(b[2:], payloadLen)
	return b
}

func UnmarshalHeader(b []byte) (byte, uint16, error) {
	if len(b) < 4 {
		return 0, 0, errors.New("pairbond: short header")
	}
	return b[0], binary.BigEndian.Uint16(b[2:4]), nil
}

// GenerateCode uses rejection sampling through crypto/rand.Int so the alphabet is uniform.
func GenerateCode() (string, error) {
	limit := big.NewInt(int64(len(codeAlphabet)))
	out := make([]byte, CodeLen)
	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("pairbond: generate code: %w", err)
		}
		out[i] = codeAlphabet[n.Int64()]
	}
	return string(out), nil
}

type clockFn func() time.Time

type codeEntry struct {
	code    [CodeLen]byte
	expires time.Time
	used    bool
}

// CodeStore keeps short-lived, single-use pairing codes. Validation scans active entries
// and compares fixed-size arrays in constant time rather than doing a secret map lookup.
type CodeStore struct {
	mu      sync.Mutex
	entries []codeEntry
	now     clockFn
}

func NewCodeStore() *CodeStore { return newCodeStore(time.Now) }
func newCodeStore(now clockFn) *CodeStore { return &CodeStore{now: now} }

func (s *CodeStore) Issue() (string, error) {
	code, err := GenerateCode()
	if err != nil {
		return "", err
	}
	var fixed [CodeLen]byte
	copy(fixed[:], code)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	s.entries = append(s.entries, codeEntry{code: fixed, expires: s.now().Add(CodeTTL)})
	return code, nil
}

func (s *CodeStore) Consume(code string) error {
	if len(code) != CodeLen {
		return ErrBadCode
	}
	var candidate [CodeLen]byte
	copy(candidate[:], code)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()

	match := -1
	for i := range s.entries {
		if subtle.ConstantTimeCompare(candidate[:], s.entries[i].code[:]) == 1 {
			match = i
		}
	}
	if match < 0 {
		return ErrBadCode
	}
	if s.entries[match].used {
		return ErrCodeUsed
	}
	s.entries[match].used = true
	return nil
}

func (s *CodeStore) gcLocked() {
	now := s.now()
	kept := s.entries[:0]
	for _, e := range s.entries {
		if !now.After(e.expires) {
			kept = append(kept, e)
		}
	}
	s.entries = kept
}

// Peer is a value snapshot. Registry never exposes internal mutable pointers.
type Peer struct {
	ID         string
	RemoteAddr string
	AcceptedAt time.Time
}

type Registry struct {
	mu    sync.RWMutex
	peers map[string]Peer
}

func NewRegistry() *Registry { return &Registry{peers: make(map[string]Peer)} }

func (r *Registry) Add(p Peer) error {
	if p.ID == "" || p.RemoteAddr == "" {
		return ErrInvalidPeer
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.peers[p.ID]; exists {
		return ErrAlreadyPaired
	}
	if len(r.peers) >= MaxPeers {
		return ErrTooManyPeers
	}
	r.peers[p.ID] = p
	return nil
}

func (r *Registry) Revoke(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.peers[id]; !ok {
		return ErrNotPaired
	}
	delete(r.peers, id)
	return nil
}

func (r *Registry) Get(id string) (Peer, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.peers[id]
	return p, ok
}

func (r *Registry) List() []Peer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Peer, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.peers)
}
