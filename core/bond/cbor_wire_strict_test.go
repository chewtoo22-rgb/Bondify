package bond

import "testing"

func TestWireCBORRejectsDuplicateControlKeys(t *testing.T) {
	// {"pid": 1, "pid": 2}. The default fxamacker decoder permits duplicate
	// map keys and may keep either value depending on destination type; BOND/1
	// control messages must have one unambiguous authenticated interpretation.
	payload := []byte{
		0xa2,
		0x63, 'p', 'i', 'd', 0x01,
		0x63, 'p', 'i', 'd', 0x02,
	}
	var got PathAddPayload
	if err := unmarshalCBOR(payload, &got); err == nil {
		t.Fatal("duplicate control key was accepted")
	}
}

func TestWireCBORRejectsDuplicateHandshakeKeys(t *testing.T) {
	// {"si": 1, "si": 2}. cfg_push is authenticated, but accepting two
	// representations for the same field still creates parser ambiguity and makes
	// protocol behavior depend on decoder implementation details.
	payload := []byte{
		0xa2,
		0x62, 's', 'i', 0x01,
		0x62, 's', 'i', 0x02,
	}
	if _, err := UnmarshalHandshakeResp(payload); err == nil {
		t.Fatal("duplicate cfg_push key was accepted")
	}
}

func TestWireCBORRejectsIndefiniteLengthMaps(t *testing.T) {
	// {_ "pid": 1} -- BOND/1 emits fixed, definite-length CBOR maps only.
	payload := []byte{
		0xbf,
		0x63, 'p', 'i', 'd', 0x01,
		0xff,
	}
	var got PathAddPayload
	if err := unmarshalCBOR(payload, &got); err == nil {
		t.Fatal("indefinite-length control map was accepted")
	}
}

func TestWireCBORRejectsSemanticTags(t *testing.T) {
	// Tag 24 wrapped around a normal {"pid": 1} map. BOND/1 does not define any
	// tagged control/config values, so tags must not silently alter interpretation.
	payload := []byte{
		0xd8, 0x18,
		0xa1,
		0x63, 'p', 'i', 'd', 0x01,
	}
	var got PathAddPayload
	if err := unmarshalCBOR(payload, &got); err == nil {
		t.Fatal("tagged control payload was accepted")
	}
}

func TestWireCBORRejectsTruncatedControlMap(t *testing.T) {
	// Declares one key/value pair but omits the value. A truncated authenticated
	// datagram must never be interpreted as a zero-valued control request.
	payload := []byte{
		0xa1,
		0x63, 'p', 'i', 'd',
	}
	var got PathAddPayload
	if err := unmarshalCBOR(payload, &got); err == nil {
		t.Fatal("truncated control map was accepted")
	}
}

func TestWireCBORRejectsOutOfRangePathID(t *testing.T) {
	// {"pid": 256}. Path IDs are uint8 on the wire and in the AEAD nonce. Values
	// outside that domain must fail instead of wrapping or truncating to path zero.
	payload := []byte{
		0xa1,
		0x63, 'p', 'i', 'd',
		0x19, 0x01, 0x00,
	}
	var got PathAddPayload
	if err := unmarshalCBOR(payload, &got); err == nil {
		t.Fatal("out-of-range path id was accepted")
	}
}

func TestWireCBORRejectsWrongPathIDType(t *testing.T) {
	// {"pid": "7"}. Control fields are strongly typed; accepting textual aliases
	// would create alternate authenticated encodings with implementation-specific
	// coercion behavior.
	payload := []byte{
		0xa1,
		0x63, 'p', 'i', 'd',
		0x61, '7',
	}
	var got PathAddPayload
	if err := unmarshalCBOR(payload, &got); err == nil {
		t.Fatal("text path id was accepted")
	}
}

func TestWireCBORRejectsNonMapControlPayload(t *testing.T) {
	// A scalar is never a valid BOND/1 control object.
	var got PathAddPayload
	if err := unmarshalCBOR([]byte{0x07}, &got); err == nil {
		t.Fatal("scalar control payload was accepted")
	}
}

func TestWireCBORStillAcceptsCanonicalPayloads(t *testing.T) {
	want := PathAddPayload{PathID: 7}
	payload, err := marshalCBOR(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got PathAddPayload
	if err := unmarshalCBOR(payload, &got); err != nil {
		t.Fatalf("strict decoder rejected canonical payload: %v", err)
	}
	if got != want {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, want)
	}
}
