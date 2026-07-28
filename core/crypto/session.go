package crypto

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/flynn/noise"
	"golang.org/x/crypto/chacha20poly1305"
)

// Session holds the two directional transport keys derived from a completed Noise_IK
// handshake, and hands out per-path AEAD ciphers keyed off of them. All paths within a
// session share the same two keys (send/recv); per-path nonce isolation (not per-path
// keys) is what PROTOCOL.md's nonce construction achieves — see handshake.go's package doc.
type Session struct {
	sendKey [KeyLen]byte
	recvKey [KeyLen]byte

	mu       sync.Mutex
	sendCtrs map[uint8]*uint64 // per-path send counters
	recvWins map[uint8]*replayWindow
}

func newSession(sendCS, recvCS *noise.CipherState) *Session {
	s := &Session{
		sendCtrs: make(map[uint8]*uint64),
		recvWins: make(map[uint8]*replayWindow),
	}
	s.sendKey = sendCS.UnsafeKey()
	s.recvKey = recvCS.UnsafeKey()
	return s
}

// NonceLen matches proto.NonceLen (96 bits); duplicated here to avoid an import cycle with
// core/proto — both are fixed protocol constants, not independently variable.
const NonceLen = 12

// PathID is embedded as the top byte of every nonce this session produces, per
// PROTOCOL.md §3: "nonce = path_id(8 bits) || counter(88 bits)". Embedding it structurally
// prevents nonce reuse across paths sharing the same key, since each path's counter space
// is disjoint from every other path's by construction, not by discipline.
func buildNonce(pathID uint8, counter uint64) [NonceLen]byte {
	var n [NonceLen]byte
	n[0] = pathID
	// counter is 88 bits (11 bytes); we only ever use the low 64 bits of it (a session
	// would need to send 2^64 packets on one path to wrap), big-endian into bytes[1:12].
	n[4] = byte(counter >> 56)
	n[5] = byte(counter >> 48)
	n[6] = byte(counter >> 40)
	n[7] = byte(counter >> 32)
	n[8] = byte(counter >> 24)
	n[9] = byte(counter >> 16)
	n[10] = byte(counter >> 8)
	n[11] = byte(counter)
	return n
}

func nonceCounter(n [NonceLen]byte) uint64 {
	return uint64(n[4])<<56 | uint64(n[5])<<48 | uint64(n[6])<<40 | uint64(n[7])<<32 |
		uint64(n[8])<<24 | uint64(n[9])<<16 | uint64(n[10])<<8 | uint64(n[11])
}

var (
	ErrAuthFailed = errors.New("crypto: AEAD authentication failed")
	ErrReplay     = errors.New("crypto: replayed or too-old nonce")
)

// Seal encrypts plaintext for transmission on the given path, returning the ciphertext
// (with appended AEAD tag) and the nonce used — the caller places the nonce in the BOND/1
// outer header (see proto.OuterHeader.Nonce) since the receiver needs it before it can
// decrypt. ad is additional authenticated data (typically the outer header bytes).
func (s *Session) Seal(pathID uint8, plaintext, ad []byte) (ciphertext []byte, nonce [NonceLen]byte, err error) {
	aead, err := chacha20poly1305.New(s.sendKey[:])
	if err != nil {
		return nil, nonce, err
	}
	counter := s.nextSendCounter(pathID)
	nonce = buildNonce(pathID, counter)
	ciphertext = aead.Seal(nil, nonce[:], plaintext, ad)
	return ciphertext, nonce, nil
}

// Open authenticates and decrypts a packet received on the given path. Per PROTOCOL.md
// §3's "authenticate first, parse second" rule, callers must not inspect any header field
// from the plaintext until this returns successfully, and must silently drop on error
// without logging at a rate an attacker could use to fingerprint the drop reason.
func (s *Session) Open(pathID uint8, ciphertext, ad []byte, nonce [NonceLen]byte) ([]byte, error) {
	if nonce[0] != pathID {
		return nil, ErrAuthFailed
	}
	win := s.recvWindow(pathID)
	counter := nonceCounter(nonce)
	if !win.CheckOnly(counter) {
		return nil, ErrReplay
	}
	aead, err := chacha20poly1305.New(s.recvKey[:])
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nonce[:], ciphertext, ad)
	if err != nil {
		return nil, ErrAuthFailed
	}
	// Only mark the counter used once AEAD verification has actually succeeded — an
	// attacker replaying or corrupting a packet must never be able to burn a legitimate
	// future counter value out from under the real sender.
	win.MarkSeen(counter)
	return plaintext, nil
}

func (s *Session) nextSendCounter(pathID uint8) uint64 {
	s.mu.Lock()
	ctr, ok := s.sendCtrs[pathID]
	if !ok {
		var zero uint64
		ctr = &zero
		s.sendCtrs[pathID] = ctr
	}
	s.mu.Unlock()
	return atomic.AddUint64(ctr, 1) - 1
}

func (s *Session) recvWindow(pathID uint8) *replayWindow {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.recvWins[pathID]
	if !ok {
		w = newReplayWindow(replayWindowSize)
		s.recvWins[pathID] = w
	}
	return w
}
