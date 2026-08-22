package bond

import (
	"context"
	"strings"
	"testing"
)

func TestDialHandshakeRejectsMoreThan256PathsBeforeNetworkWork(t *testing.T) {
	cfg := ClientConfig{
		RelayAddr: "definitely-not-a-real-bondify-host.invalid:51820",
		Paths:     make([]PathSpec, 257),
	}

	tunnel, _, err := DialHandshake(context.Background(), cfg)
	if tunnel != nil {
		t.Fatal("DialHandshake returned a tunnel for an over-limit path configuration")
	}
	if err == nil {
		t.Fatal("DialHandshake unexpectedly accepted 257 paths")
	}
	if !strings.Contains(err.Error(), "too many paths") || !strings.Contains(err.Error(), "maximum 256") {
		t.Fatalf("DialHandshake error = %q; want path-limit validation error", err)
	}
	if strings.Contains(err.Error(), "resolve relay addr") {
		t.Fatalf("DialHandshake reached relay resolution before path validation: %v", err)
	}
}
