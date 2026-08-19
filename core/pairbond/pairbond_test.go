package pairbond

import (
	"errors"
	"testing"
	"time"
)

func TestCodeSingleUseAndExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	s := newCodeStore(func() time.Time { return now })

	code, err := s.Issue()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != CodeLen {
		t.Fatalf("code length = %d, want %d", len(code), CodeLen)
	}
	if err := s.Consume(code); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if err := s.Consume(code); !errors.Is(err, ErrCodeUsed) {
		t.Fatalf("second consume = %v, want ErrCodeUsed", err)
	}

	expiring, err := s.Issue()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(CodeTTL + time.Nanosecond)
	if err := s.Consume(expiring); !errors.Is(err, ErrBadCode) {
		t.Fatalf("expired consume = %v, want ErrBadCode", err)
	}
}

func TestBadCodeRejected(t *testing.T) {
	s := NewCodeStore()
	if err := s.Consume("SHORT"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("short code = %v", err)
	}
	if err := s.Consume("000000"); !errors.Is(err, ErrBadCode) {
		t.Fatalf("unknown code = %v", err)
	}
}

func TestHeaderRoundTrip(t *testing.T) {
	b := MarshalHeader(MsgData, 4096)
	typ, n, err := UnmarshalHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	if typ != MsgData || n != 4096 {
		t.Fatalf("got type=%x len=%d", typ, n)
	}
	if _, _, err := UnmarshalHeader([]byte{1, 2, 3}); err == nil {
		t.Fatal("short header accepted")
	}
}

func TestRegistryLifecycleAndLimit(t *testing.T) {
	r := NewRegistry()
	for i, id := range []string{"d", "c", "b", "a"} {
		if err := r.Add(Peer{ID: id, RemoteAddr: "192.0.2.1", AcceptedAt: time.Unix(int64(i), 0)}); err != nil {
			t.Fatalf("add %s: %v", id, err)
		}
	}
	if err := r.Add(Peer{ID: "e", RemoteAddr: "192.0.2.2"}); !errors.Is(err, ErrTooManyPeers) {
		t.Fatalf("fifth peer = %v, want ErrTooManyPeers", err)
	}
	if got := r.List(); len(got) != 4 || got[0].ID != "a" || got[3].ID != "d" {
		t.Fatalf("unexpected sorted list: %#v", got)
	}
	if err := r.Revoke("b"); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Get("b"); ok {
		t.Fatal("revoked peer still present")
	}
	if r.Count() != 3 {
		t.Fatalf("count = %d, want 3", r.Count())
	}
}

func TestRegistryRejectsInvalidAndDuplicatePeers(t *testing.T) {
	r := NewRegistry()
	if err := r.Add(Peer{}); !errors.Is(err, ErrInvalidPeer) {
		t.Fatalf("empty peer = %v", err)
	}
	p := Peer{ID: "phone", RemoteAddr: "192.0.2.3"}
	if err := r.Add(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(p); !errors.Is(err, ErrAlreadyPaired) {
		t.Fatalf("duplicate = %v", err)
	}
	if err := r.Revoke("missing"); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("missing revoke = %v", err)
	}
}
