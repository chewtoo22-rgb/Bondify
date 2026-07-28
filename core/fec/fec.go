// Package fec implements ARCHITECTURE.md §2.3's adaptive FEC: systematic Reed-Solomon
// parity over a generation of data packets, redundancy scaled to observed loss. It knows
// nothing about BOND/1 framing or sessions -- callers hand it raw packet payloads for one
// generation and get parity shards (to send) or recovered payloads (after reconstruction)
// back, keeping this package independently testable with real Reed-Solomon math.
//
// Packets in a generation are rarely all the same length (they're tunnelled IP packets of
// varying size), but Reed-Solomon requires uniform shard width. Rather than padding every
// DATA packet on the wire (wasting bytes on the common case where nothing is lost), each
// shard fed into the actual erasure-coding math is self-describing: a 2-byte big-endian
// length prefix followed by the payload, zero-padded out to the generation's shard width.
// A reconstructed shard therefore carries its own true length back out, with no separate
// manifest needed on the wire.
package fec

import (
	"encoding/binary"
	"errors"
	"math"

	"github.com/klauspost/reedsolomon"
)

// Generation defaults, per PROTOCOL.md's Appendix.
const (
	K             = 10   // FEC_K: max data shards per generation
	MaxRedundancy = 0.25 // FEC_MAX_REDUNDANCY
	// LossScale is not itself part of PROTOCOL.md's spec (only K and MaxRedundancy are);
	// it's the multiplier this implementation uses to translate an observed mean loss rate
	// into a redundancy ratio. It's deliberately steep, not a 1:1 scale-up: a generation is
	// only K=10 packets, so at a "5% loss" mean the actual number of losses in any given
	// generation is binomially distributed, not a constant -- e.g. real measurement at
	// K=10, 5% iid loss found m=2 (the previous, gentler scale of 2.5) left ~1.16% of
	// generations losing 3+ packets and going unrecovered, above the <1% goodput-loss gate
	// (ARCHITECTURE.md §5). Saturating the MaxRedundancy cap by 5% mean loss instead of
	// 10% pushes that down near 0.11%, comfortably under the gate. See ARCHITECTURE.md §9.
	LossScale    = 5.0
	lengthPrefix = 2 // bytes reserved for each shard's self-describing length
)

var (
	// ErrTooFewShards means fewer than n of the n+m shards are present -- reconstruction
	// is mathematically impossible, not just difficult.
	ErrTooFewShards = errors.New("fec: fewer than n shards present, cannot reconstruct")
	// ErrEmptyGeneration means n == 0: there is nothing to encode or reconstruct.
	ErrEmptyGeneration = errors.New("fec: generation has no data shards")
)

// RedundancyFor returns m, the parity shard count for a generation of n data shards,
// given the current observed loss estimate (0.0-1.0, e.g. Path.Loss()). Scaled
// `clamp(loss * LossScale, 0, MaxRedundancy)` per ARCHITECTURE.md §2.3: at zero loss this
// is zero (FEC effectively off, no bytes wasted); at 5%+ loss it saturates at
// MaxRedundancy so a single generation is never more than 25% parity overhead regardless
// of how bad the path gets.
func RedundancyFor(loss float64, n int) int {
	if n <= 0 {
		return 0
	}
	ratio := loss * LossScale
	if ratio < 0 {
		ratio = 0
	}
	if ratio > MaxRedundancy {
		ratio = MaxRedundancy
	}
	m := int(math.Ceil(ratio * float64(n)))
	if m > n {
		m = n // sanity floor; MaxRedundancy already keeps this well under n in practice
	}
	return m
}

// ShardWidth returns the fixed per-shard byte width Reed-Solomon must operate over for a
// generation whose longest data-shard payload is maxPayloadLen bytes.
func ShardWidth(maxPayloadLen int) int { return lengthPrefix + maxPayloadLen }

// encodeShard produces the length-prefixed, zero-padded w-byte Reed-Solomon input for one
// data packet's raw payload. Deterministic: the receiver reproduces this exact transform
// for every data shard it actually received, so its bytes agree with what the sender's
// encoder computed.
func encodeShard(payload []byte, w int) ([]byte, error) {
	if len(payload)+lengthPrefix > w {
		return nil, errors.New("fec: payload exceeds shard width")
	}
	buf := make([]byte, w)
	binary.BigEndian.PutUint16(buf[0:lengthPrefix], uint16(len(payload)))
	copy(buf[lengthPrefix:], payload)
	return buf, nil
}

// decodeShard strips the self-describing length prefix back off a w-byte Reed-Solomon
// shard, returning the original (or reconstructed) payload at its true length.
func decodeShard(buf []byte) ([]byte, error) {
	if len(buf) < lengthPrefix {
		return nil, errors.New("fec: shard shorter than length prefix")
	}
	n := int(binary.BigEndian.Uint16(buf[0:lengthPrefix]))
	if lengthPrefix+n > len(buf) {
		return nil, errors.New("fec: shard length prefix exceeds shard width")
	}
	return buf[lengthPrefix : lengthPrefix+n], nil
}

// EncodeParity computes m parity shards from dataShards (one generation's worth of raw,
// variable-length packet payloads, indices 0..n-1), each padded to width w. w must be at
// least ShardWidth(len(longest dataShards[i])). Returns m parity shards, each exactly w
// bytes, ready to send on the wire as-is.
func EncodeParity(dataShards [][]byte, w, m int) ([][]byte, error) {
	n := len(dataShards)
	if n == 0 {
		return nil, ErrEmptyGeneration
	}
	if m == 0 {
		return nil, nil
	}

	enc, err := reedsolomon.New(n, m)
	if err != nil {
		return nil, err
	}

	shards := make([][]byte, n+m)
	for i, payload := range dataShards {
		s, err := encodeShard(payload, w)
		if err != nil {
			return nil, err
		}
		shards[i] = s
	}
	for i := n; i < n+m; i++ {
		shards[i] = make([]byte, w)
	}

	if err := enc.Encode(shards); err != nil {
		return nil, err
	}
	return shards[n:], nil
}

// Reconstruct recovers missing data shards for a generation of n data + m parity shards,
// each of width w. present[i] must be true, and shards[i] populated, for every shard
// actually received: data shards (indices 0..n-1) at their true received length, exactly
// as they arrived off the wire; parity shards (indices n..n+m-1) at exactly w bytes,
// exactly as they arrived. Returns a map from data-shard index to its recovered original
// payload, for every data index that was missing (present[i] == false, i < n) but could be
// recovered. Returns ErrTooFewShards if fewer than n total shards (data+parity combined)
// are present -- Reed-Solomon's fundamental floor, not a bug to retry around.
func Reconstruct(shards [][]byte, present []bool, n, m, w int) (map[int][]byte, error) {
	if n <= 0 {
		return nil, ErrEmptyGeneration
	}
	if len(shards) != n+m || len(present) != n+m {
		return nil, errors.New("fec: shards/present length must equal n+m")
	}

	haveCount := 0
	rsShards := make([][]byte, n+m)
	for i := 0; i < n+m; i++ {
		if !present[i] {
			continue
		}
		haveCount++
		if i < n {
			s, err := encodeShard(shards[i], w)
			if err != nil {
				return nil, err
			}
			rsShards[i] = s
		} else {
			if len(shards[i]) != w {
				return nil, errors.New("fec: parity shard has wrong width")
			}
			rsShards[i] = shards[i]
		}
	}
	if haveCount < n {
		return nil, ErrTooFewShards
	}

	enc, err := reedsolomon.New(n, m)
	if err != nil {
		return nil, err
	}
	if err := enc.Reconstruct(rsShards); err != nil {
		return nil, err
	}

	recovered := make(map[int][]byte)
	for i := 0; i < n; i++ {
		if present[i] {
			continue
		}
		payload, err := decodeShard(rsShards[i])
		if err != nil {
			return nil, err
		}
		recovered[i] = payload
	}
	return recovered, nil
}
