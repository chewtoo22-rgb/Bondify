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
