// Package pairbond implements Phase 8 PairBond / share mode (ARCHITECTURE.md §5/§7):
// a LAN peer can contribute an extra uplink (e.g. cellular) to a host's bonded session
// after an explicit pairing-code exchange. Peers are never auto-trusted.
//
// Model (path proxy):
//
//	Host (Bondify client with an active relay session)
//	  <--LAN UDP/TCP-->  Peer (share mode)
//	                       <--WAN UDP-->  Relay
//
// The host treats the peer link as one more Path: packets sealed for the relay are
// written to the peer; the peer forwards ciphertext to the relay address and returns
// replies. The peer never sees plaintext (AEAD already applied). Instant revoke closes
// the path and tells the peer to stop forwarding.
//
// Pairing:
//  1. Host GenerateCode() → short-lived human-readable code + listen on PairPort.
//  2. Peer dials host, sends PairRequest{Code, PeerPubKey, PeerID}.
//  3. Host verifies code (constant-time, single-use, TTL), replies PairAccept.
//  4. Either side may Revoke; both tear down immediately.
package pairbond

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	// PairPort is the default LAN listen port for PairBond pairing + data.
	PairPort = 51821

	// CodeLen is the number of characters in a pairing code (crockford base32 alphabet).
	CodeLen = 6

	// CodeTTL is how long a generated code remains valid.
	CodeTTL = 5 * time.Minute

	// MaxPeers is a soft limit on concurrent paired peers per host.
	MaxPeers = 4
)

// Crockford base32 without confusing I/L/O/U.
const codeAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	ErrBadCode       = errors.New("pairbond: invalid or expired pairing code")
	ErrCodeUsed      = errors.New("pairbond: pairing code already used")
	ErrTooManyPeers  = errors.New("pairbond: too many paired peers")
	ErrNotPaired     = errors.New("pairbond: peer not paired")
	ErrAlreadyPaired = errors.New("pairbond: already paired with this peer")
)

// GenerateCode returns a fresh human-readable pairing code of CodeLen characters.
func GenerateCode() (string, error) {
	buf := make([]byte, CodeLen)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("pairbond: rand: %w", err)
	}
	out := make([]byte, CodeLen)
	for i := 0; i < CodeLen; i++ {
		out[i] = codeAlphabet[int(buf[i])%len(codeAlphabet)]
	}
	return string(out), nil
}

// CodeStore holds outstanding pairing codes on the host. Safe for concurrent use.
type CodeStore struct {
	mu    sync.Mutex
	codes map[string]codeEntry
}

type codeEntry struct {
	expires time.Time
	used    bool
}

// NewCodeStore creates an empty store.
func NewCodeStore() *CodeStore {
	return &CodeStore{codes: make(map[string]codeEntry)}
}

// Issue generates a new code, stores it with CodeTTL, and returns it.
func (s *CodeStore) Issue() (string, error) {
	code, err := GenerateCode()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	s.codes[code] = codeEntry{expires: time.Now().Add(CodeTTL)}
	return code, nil
}

// Consume validates code (constant-time among active codes) and marks it used.
// Returns ErrBadCode if missing/expired, ErrCodeUsed if already consumed.
func (s *CodeStore) Consume(code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gcLocked()
	ent, ok := s.codes[code]
	if !ok || time.Now().After(ent.expires) {
		_ = subtle.ConstantTimeCompare([]byte(code), []byte(code))
		return ErrBadCode
	}
	if ent.used {
		return ErrCodeUsed
	}
	ent.used = true
	s.codes[code] = ent
	return nil
}

func (s *CodeStore) gcLocked() {
	now := time.Now()
	for c, e := range s.codes {
		if now.After(e.expires) || e.used {
			delete(s.codes, c)
		}
	}
}

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

type PairReject struct {
	Reason string
}

func MarshalHeader(msgType byte, payloadLen uint16) []byte {
	buf := make([]byte, 4)
	buf[0] = msgType
	buf[1] = 0
	binary.BigEndian.PutUint16(buf[2:4], payloadLen)
	return buf
}

func UnmarshalHeader(b []byte) (msgType byte, payloadLen uint16, err error) {
	if len(b) < 4 {
		return 0, 0, errors.New("pairbond: short header")
	}
	return b[0], binary.BigEndian.Uint16(b[2:4]), nil
}

type Registry struct {
	mu    sync.Mutex
	peers map[string]*Peer
}

type Peer struct {
	ID         string
	RemoteAddr string
	AcceptedAt time.Time
	Revoked    bool
}

func NewRegistry() *Registry {
	return &Registry{peers: make(map[string]*Peer)}
}

func (r *Registry) Add(p *Peer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.peers) >= MaxPeers {
		return ErrTooManyPeers
	}
	if _, ok := r.peers[p.ID]; ok {
		return ErrAlreadyPaired
	}
	r.peers[p.ID] = p
	return nil
}

func (r *Registry) Revoke(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.peers[id]
	if !ok {
		return ErrNotPaired
	}
	p.Revoked = true
	delete(r.peers, id)
	return nil
}

func (r *Registry) Get(id string) *Peer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peers[id]
}

func (r *Registry) List() []*Peer {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Peer, 0, len(r.peers))
	for _, p := range r.peers {
		out = append(out, p)
	}
	return out
}

func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.peers)
}
