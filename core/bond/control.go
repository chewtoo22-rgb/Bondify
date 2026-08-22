package bond

import (
	"fmt"

	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/proto"
	"github.com/fxamacker/cbor/v2"
)

// Control-plane packets (PROBE, PROBE_ACK, ACK, PATH_ADD, PATH_DROP, CTRL) are sealed directly
// with crypto.Session.Seal, skipping proto.InnerDataHeader entirely. That header exists to
// carry GSN (data-stream reassembly order) and PSN/Generation (loss attribution and FEC)
// for the tunnelled IP packet stream specifically -- control messages need neither: replay
// protection for every packet type already comes for free from crypto.Session's per-path
// AEAD nonce counter and replay window (see core/crypto/session.go, core/crypto/replay.go),
// which is independent of GSN. Reusing the DATA-shaped header here would conflate two
// different counters (data-stream sequence vs. anti-replay) that phase 1 deliberately kept
// separate.

func sealControl(sess *crypto.Session, typ proto.Type, sessionIndex uint32, pathID uint8, payload []byte) ([]byte, error) {
	adBuf := make([]byte, proto.OuterFixedLen)
	adBuf[0] = byte(typ)
	adBuf[1] = proto.Version
	be32(adBuf[4:8], sessionIndex)

	ciphertext, nonce, err := sess.Seal(pathID, payload, adBuf)
	if err != nil {
		return nil, fmt.Errorf("bond: seal control: %w", err)
	}
	out := make([]byte, proto.OuterPrefixLen+len(ciphertext))
	if err := proto.MarshalOuter(out, proto.OuterHeader{
		Type: typ, Version: proto.Version, SessionIndex: sessionIndex, Nonce: nonce,
	}); err != nil {
		return nil, err
	}
	copy(out[proto.OuterPrefixLen:], ciphertext)
	return out, nil
}

func openControl(sess *crypto.Session, oh proto.OuterHeader, pathID uint8, ciphertext []byte) ([]byte, error) {
	adBuf := make([]byte, proto.OuterFixedLen)
	adBuf[0] = byte(oh.Type)
	adBuf[1] = oh.Version
	be32(adBuf[4:8], oh.SessionIndex)
	payload, err := sess.Open(pathID, ciphertext, adBuf, oh.Nonce)
	if err != nil {
		return nil, err
	}
	// PATH_ADD is authenticated under the path ID encoded into the AEAD nonce. The
	// decrypted CBOR must name that same path; otherwise an authenticated client could
	// bind one cryptographic identity while registering another control-plane identity.
	if oh.Type == proto.TypePathAdd {
		var req PathAddPayload
		if err := unmarshalCBOR(payload, &req); err != nil {
			return nil, err
		}
		if req.PathID != pathID {
			return nil, fmt.Errorf("bond: path add payload id %d does not match authenticated path id %d", req.PathID, pathID)
		}
	}
	return payload, nil
}

// ProbePayload measures RTT and loss for one path (PROTOCOL.md §6). SentPSN is the path's
// current DATA-stream PSN at the moment of probing: echoing it back in ProbeAckPayload
// lets the sender compute the delta of PSN issued vs. the receiver's delta of PSN actually
// received since the last probe, i.e. real per-path DATA loss -- not merely probe-packet
// loss, which at 5 probes/sec would be a far noisier signal than the data stream itself.
type ProbePayload struct {
	SentAtUnixNano int64  `cbor:"ts"`
	SentPSN        uint32 `cbor:"psn"`
}

type ProbeAckPayload struct {
	SentAtUnixNano int64  `cbor:"ts"`  // echoed verbatim from the PROBE
	SentPSN        uint32 `cbor:"psn"` // echoed verbatim from the PROBE
	RecvPSN        uint32 `cbor:"rpsn"`
}

// PathAddPayload registers path PathID within the already-established session.
type PathAddPayload struct {
	PathID uint8 `cbor:"pid"`
}

// PathDropPayload retires a path. Reason is informational (logs/diagnostics only).
type PathDropPayload struct {
	PathID uint8  `cbor:"pid"`
	Reason string `cbor:"reason"`
}

// CtrlPathAddAck is sent as a CTRL packet in response to PATH_ADD -- see PROTOCOL.md §5's
// establishment diagram ("PATH_ADD ... --> | <-- CTRL ack --"). Kind mirrors PROTOCOL.md
// §9's CTRL kind discriminator convention (cfg_push, mode_set, ...); only path_add_ack is
// implemented in phase 2, more kinds arrive as later phases need them (mode_set in phase 4,
// path_role/fec_set in phase 7, stats streaming wherever the diagnostics UI lands).
type CtrlPathAddAck struct {
	Kind   string `cbor:"kind"`
	PathID uint8  `cbor:"pid"`
}

func marshalCBOR(v interface{}) ([]byte, error) {
	b, err := cbor.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("bond: marshal: %w", err)
	}
	return b, nil
}

func unmarshalCBOR(b []byte, v interface{}) error {
	if err := cbor.Unmarshal(b, v); err != nil {
		return fmt.Errorf("bond: unmarshal: %w", err)
	}
	return nil
}
