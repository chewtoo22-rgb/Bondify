package diag

import "testing"

func TestServerRejectsNonLoopbackBind(t *testing.T) {
	if _, err := NewServer("0.0.0.0:0", func() any { return nil }); err == nil {
		t.Fatal("expected non-loopback bind rejection")
	}
}

func TestServerRejectsNilSnapshot(t *testing.T) {
	if _, err := NewServer("127.0.0.1:0", nil); err == nil {
		t.Fatal("expected nil snapshot rejection")
	}
}
