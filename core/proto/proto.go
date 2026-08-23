// Package proto implements the BOND/1 wire format defined in PROTOCOL.md.
//
// Two headers exist: the Outer datagram header (16 bytes, plaintext, precedes
// the AEAD ciphertext) and the inner DATA header (20 bytes, padded, encrypted
// inside the AEAD payload for DATA/FEC packets). All integers are big-endian.
package proto

import (
	"encoding/binary"
	"errors"
)

// Version is the current BOND/1 protocol major version.
const Version uint8 = 1

// Type identifies the outer packet type. See PROTOCOL.md §4.
type Type uint8

const (
	TypeHandshakeInit Type = 0x01
	TypeHandshakeResp Type = 0x02
	TypeData          Type = 0x10
	TypeFEC           Type = 0x11
	TypeProbe         Type = 0x20
	TypeProbeAck      Type = 0x21
	TypeAck           Type = 0x30
	TypePathAdd       Type = 0x40
	TypePathDrop      Type = 0x41
	TypeCtrl          Type = 0x50
	TypeKeepalive     Type = 0x60
)

func (t Type) String() string {
	switch t {
	case TypeHandshakeInit:
		return "HANDSHAKE_INIT"
	case TypeHandshakeResp:
		return "HANDSHAKE_RESP"
	case TypeData:
		return "DATA"
	case TypeFEC:
		return "FEC"
	case TypeProbe:
		return "PROBE"
	case TypeProbeAck:
		return "PROBE_ACK"
	case TypeAck:
		return "ACK"
	case TypePathAdd:
		return "PATH_ADD"
	case TypePathDrop:
		return "PATH_DROP"
	case TypeCtrl:
		return "CTRL"
	case TypeKeepalive:
		return "KEEPALIVE"
	default:
		return "UNKNOWN"
	}
}

// DATA flags. See PROTOCOL.md §4 "DATA flags".
const (
	FlagDUP          uint8 = 1 << 0
	FlagRTX          uint8 = 1 << 1
	FlagLATENCY      uint8 = 1 << 2
	FlagBULK         uint8 = 1 << 3
	FlagFECProtected uint8 = 1 << 4
	FlagPUSH         uint8 = 1 << 5
	// bits 6-7 reserved, must be zero.
	reservedFlagsMask uint8 = 0xC0
)

const (
	// OuterFixedLen is Type(1) + Version(1) + Reserved(2) + SessionIndex(4).
	OuterFixedLen = 8
	// NonceLen is the AEAD nonce size (96 bits / 12 bytes).
	//
	// Deviation from the source spec's arithmetic (documented in ARCHITECTURE.md §9):
	// the nonce is path_id(8 bits) || counter(88 bits), and path_id lives inside the
	// encrypted inner header — so it cannot be reconstructed before AEAD verification.
	// We therefore transmit the full 96-bit nonce in the clear outer prefix rather than
	// deriving it, which is the only way to decrypt without a chicken-and-egg dependency.
	// This makes the true outer prefix 20 bytes (OuterFixedLen+NonceLen), not the spec
	// prose's stated 16; HeaderOverhead below is computed self-consistently from that.
	NonceLen = 12
	// OuterPrefixLen is everything transmitted in the clear before the AEAD ciphertext.
	OuterPrefixLen = OuterFixedLen + NonceLen
	// AuthTagLen is the AEAD authentication tag size (128 bits).
	AuthTagLen = 16
	// InnerHeaderLen is the padded inner DATA header size (19 bytes, padded to 20).
	InnerHeaderLen = 20
	// HeaderOverhead is total fixed overhead per DATA packet: outer prefix(20) + inner(20) + tag(16).
	HeaderOverhead = OuterPrefixLen + InnerHeaderLen + AuthTagLen
	// PMTUFloor is the minimum plausible path MTU; below this something is badly broken.
	PMTUFloor = 1200
)

var (
	ErrShortBuffer  = errors.New("proto: buffer too short")
	ErrBadVersion   = errors.New("proto: unsupported version")
	ErrReserved     = errors.New("proto: reserved field must be zero")
	ErrPayloadTooLg = errors.New("proto: payload exceeds 16-bit length field")
)

// OuterHeader is the plaintext framing before the encrypted payload.
//
//	 0                   1                   2                   3
//	 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|  Type (8)     |  Version (8)  |         Reserved (16)         |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                     Session Index (32)                        |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                                                               |
//	+                        Nonce (96)                             +
//	|                                                               |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
type OuterHeader struct {
	Type         Type
	Version      uint8
	SessionIndex uint32
	Nonce        [NonceLen]byte
}

// MarshalOuter writes the OuterPrefixLen-byte clear-text prefix: Type, Version, Reserved,
// SessionIndex, then the 12-byte nonce. It does not touch ciphertext/tag; caller appends
// those after. dst must be at least OuterPrefixLen bytes.
func MarshalOuter(dst []byte, h OuterHeader) error {
	if len(dst) < OuterPrefixLen {
		return ErrShortBuffer
	}
	dst[0] = byte(h.Type)
	dst[1] = h.Version
	dst[2] = 0
	dst[3] = 0
	binary.BigEndian.PutUint32(dst[4:8], h.SessionIndex)
	copy(dst[8:8+NonceLen], h.Nonce[:])
	return nil
}

// UnmarshalOuter parses the outer prefix (header + nonce) from src. Returns the number of
// bytes consumed (OuterPrefixLen) so the caller can slice the remaining ciphertext.
func UnmarshalOuter(src []byte) (OuterHeader, int, error) {
	var h OuterHeader
	if len(src) < OuterPrefixLen {
		return h, 0, ErrShortBuffer
	}
	h.Type = Type(src[0])
	h.Version = src[1]
	if h.Version != Version {
		return h, 0, ErrBadVersion
	}
	if src[2] != 0 || src[3] != 0 {
		return h, 0, ErrReserved
	}
	h.SessionIndex = binary.BigEndian.Uint32(src[4:8])
	copy(h.Nonce[:], src[8:8+NonceLen])
	return h, OuterPrefixLen, nil
}

// InnerDataHeader is the header inside the AEAD payload for DATA and FEC packets.
//
//	 0                   1                   2                   3
//	 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                   Global Sequence Number (64)                 +
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                 Path Sequence Number (32)                     |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|   Path ID (8) |   Flags (8)   |      Payload Length (16)      |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|                    Generation ID (16)         | Gen Index (8) |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
type InnerDataHeader struct {
	GSN          uint64
	PSN          uint32
	PathID       uint8
	Flags        uint8
	PayloadLen   uint16
	GenerationID uint16
	GenIndex     uint8
}

// MarshalInner writes the inner DATA header (19 bytes) padded to InnerHeaderLen(20).
func MarshalInner(dst []byte, h InnerDataHeader) error {
	if len(dst) < InnerHeaderLen {
		return ErrShortBuffer
	}
	if h.Flags&reservedFlagsMask != 0 {
		return ErrReserved
	}
	binary.BigEndian.PutUint64(dst[0:8], h.GSN)
	binary.BigEndian.PutUint32(dst[8:12], h.PSN)
	dst[12] = h.PathID
	dst[13] = h.Flags
	binary.BigEndian.PutUint16(dst[14:16], h.PayloadLen)
	binary.BigEndian.PutUint16(dst[16:18], h.GenerationID)
	dst[18] = h.GenIndex
	dst[19] = 0 // pad
	return nil
}

// UnmarshalInner parses the inner DATA header from src (must be authenticated already).
func UnmarshalInner(src []byte) (InnerDataHeader, int, error) {
	var h InnerDataHeader
	if len(src) < InnerHeaderLen {
		return h, 0, ErrShortBuffer
	}
	if src[13]&reservedFlagsMask != 0 || src[19] != 0 {
		return h, 0, ErrReserved
	}
	h.GSN = binary.BigEndian.Uint64(src[0:8])
	h.PSN = binary.BigEndian.Uint32(src[8:12])
	h.PathID = src[12]
	h.Flags = src[13]
	h.PayloadLen = binary.BigEndian.Uint16(src[14:16])
	h.GenerationID = binary.BigEndian.Uint16(src[16:18])
	h.GenIndex = src[18]
	return h, InnerHeaderLen, nil
}

// HasFlag reports whether flag bit f is set in flags.
func HasFlag(flags, f uint8) bool { return flags&f != 0 }

// FECHeaderLen is the fixed byte length of FECHeader.
const FECHeaderLen = 4

// FECHeader precedes the parity shard payload inside an FEC packet's AEAD-encrypted
// content, immediately after the InnerDataHeader ("Inner IP packet" in the DATA diagram's
// slot is a parity shard here instead). It carries the two facts a receiver needs to
// reconstruct a generation that core/fec's Reconstruct doesn't get from the InnerDataHeader
// already present on every DATA/FEC packet (GenerationID, GenIndex): how many total data
// shards (N) and parity shards (M) the generation has, and the shard width (W) core/fec's
// Reed-Solomon math must pad every data shard out to. A receiver that never loses a data
// shard never needs to parse this at all -- reconstruction, and therefore this header, only
// matters on the loss path.
//
//	 0                   1                   2                   3
//	 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|      N (8)    |      M (8)    |             W (16)           |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
type FECHeader struct {
	N uint8  // total data shards in this generation
	M uint8  // total parity shards in this generation
	W uint16 // Reed-Solomon shard width, bytes (core/fec.ShardWidth's return value)
}

// MarshalFECHeader writes the 4-byte FECHeader. dst must be at least FECHeaderLen bytes.
func MarshalFECHeader(dst []byte, h FECHeader) error {
	if len(dst) < FECHeaderLen {
		return ErrShortBuffer
	}
	dst[0] = h.N
	dst[1] = h.M
	binary.BigEndian.PutUint16(dst[2:4], h.W)
	return nil
}

// UnmarshalFECHeader parses FECHeader from src, returning bytes consumed (FECHeaderLen).
func UnmarshalFECHeader(src []byte) (FECHeader, int, error) {
	var h FECHeader
	if len(src) < FECHeaderLen {
		return h, 0, ErrShortBuffer
	}
	h.N = src[0]
	h.M = src[1]
	h.W = binary.BigEndian.Uint16(src[2:4])
	return h, FECHeaderLen, nil
}
