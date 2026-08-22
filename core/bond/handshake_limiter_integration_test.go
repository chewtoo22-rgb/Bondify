package bond

import (
	"errors"
	"net"
	"testing"

	"github.com/chewtoo22-rgb/bondify/core/crypto"
)

func TestHandshakeLimiterRejectsBeforeResponderWork(t *testing.T) {
	called := 0
	r := &Relay{
		handshakeLimiter: newHandshakeLimiterWithGlobal(100, 1, 8, 100, 100),
		newResponder: func(crypto.Keypair) (*crypto.Responder, error) {
			called++
			return nil, errors.New("sentinel responder")
		},
	}
	src := &net.UDPAddr{IP: net.ParseIP("203.0.113.77"), Port: 4242}

	r.handleHandshakeInit(nil, src)
	if called != 1 {
		t.Fatalf("first allowed handshake created %d responders, want 1", called)
	}

	r.handleHandshakeInit(nil, src)
	if called != 1 {
		t.Fatalf("rate-limited handshake reached responder factory; calls=%d, want 1", called)
	}
}

func TestHandshakeLimiterRejectsNilSourceBeforeResponderWork(t *testing.T) {
	called := 0
	r := &Relay{
		handshakeLimiter: newHandshakeLimiterWithGlobal(100, 1, 8, 100, 100),
		newResponder: func(crypto.Keypair) (*crypto.Responder, error) {
			called++
			return nil, errors.New("sentinel responder")
		},
	}
	r.handleHandshakeInit(nil, nil)
	if called != 0 {
		t.Fatalf("nil source reached responder factory; calls=%d", called)
	}
}
