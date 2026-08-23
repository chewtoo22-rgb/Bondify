package bond

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/crypto"
)

// mustRelay starts a real *Relay listening on loopback with a live UDP control plane
// (ServeUDP running), but no TUN device -- AddPath/DropPath only exercise the handshake and
// control-plane paths (handlePathAdd/handlePathDrop), never actual data forwarding, so a nil
// tun.Device is enough for these tests to run against the genuine relay code, not a hand-
// rolled stand-in.
func mustRelay(t *testing.T) (*Relay, crypto.Keypair) {
	t.Helper()
	relayKP, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("relay keypair: %v", err)
	}
	r, err := NewRelay(RelayConfig{
		ListenAddr: "127.0.0.1:0",
		RelayKey:   relayKP,
		PoolCIDR:   "10.77.0.0/24",
		MTU:        1280,
	}, nil)
	if err != nil {
		t.Fatalf("new relay: %v", err)
	}
	go func() { _ = r.ServeUDP() }()
	t.Cleanup(r.Close)
	return r, relayKP
}

// mustClientTunnel completes a real Noise_IK handshake with r's genuine handshake handler
// and returns the resulting *ClientTunnel, not attached to any TUN device.
func mustClientTunnel(t *testing.T, r *Relay, relayKP crypto.Keypair) (*ClientTunnel, uint32) {
	t.Helper()
	clientKP, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatalf("client keypair: %v", err)
	}
	tun, resp, err := DialHandshake(context.Background(), ClientConfig{
		RelayAddr:    r.conn.LocalAddr().(*net.UDPAddr).String(),
		RelayPubKey:  relayKP.Public,
		ClientKey:    clientKP,
		HandshakeTO:  time.Second,
		HandshakeTry: 3,
	})
	if err != nil {
		t.Fatalf("dial handshake: %v", err)
	}
	t.Cleanup(func() {
		for _, p := range tun.Paths() {
			_ = p.conn.Close()
		}
	})
	return tun, resp.SessionIndex
}

func relaySessionFor(r *Relay, sessionIndex uint32) *relaySession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byIndex[sessionIndex]
}

func TestClientTunnelAddPathBeforeRunDoesNotSpawnLoopsYet(t *testing.T) {
	r, relayKP := mustRelay(t)
	tun, _ := mustClientTunnel(t, r, relayKP)

	if err := tun.AddPath(context.Background(), 1, PathSpec{}); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	p1 := tun.pathByID(1)
	if p1 == nil {
		t.Fatal("path 1 not registered")
	}
	t.Cleanup(func() { _ = p1.conn.Close() })
	if p1.loopsStarted.Load() {
		t.Fatal("AddPath started read/probe loops before Run ever started")
	}
	if got := len(tun.schedPaths()); got != 2 {
		t.Fatalf("schedPaths() = %d, want 2", got)
	}
}

func TestClientTunnelAddPathAfterRunStartsServingImmediately(t *testing.T) {
	r, relayKP := mustRelay(t)
	tun, sessionIndex := mustClientTunnel(t, r, relayKP)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Simulate Run() having already started, without needing a real TUN device (Run's own
	// packet-pump goroutines are irrelevant to AddPath/DropPath's control-plane behavior).
	tun.runCtx.Store(&ctx)

	if err := tun.AddPath(context.Background(), 1, PathSpec{}); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	p1 := tun.pathByID(1)
	if p1 == nil {
		t.Fatal("path 1 not registered")
	}
	if !p1.loopsStarted.Load() {
		t.Fatal("AddPath did not start path 1's read/probe loops on an already-running tunnel")
	}

	sess := relaySessionFor(r, sessionIndex)
	if sess == nil {
		t.Fatal("relay never created a session for this handshake")
	}
	if sess.pathByID(1) == nil {
		t.Fatal("relay never registered path 1 via PATH_ADD")
	}
}

func TestClientTunnelAddPathRejectsDuplicateID(t *testing.T) {
	r, relayKP := mustRelay(t)
	tun, _ := mustClientTunnel(t, r, relayKP)

	if err := tun.AddPath(context.Background(), 0, PathSpec{}); err == nil {
		t.Fatal("expected an error re-registering path 0, got nil")
	}
}

func TestClientTunnelConcurrentAddPathSameIDIsRejectedWhileFirstIsInFlight(t *testing.T) {
	r, relayKP := mustRelay(t)
	tun, _ := mustClientTunnel(t, r, relayKP)

	// Point the first registration at a UDP sink that receives PATH_ADD but never ACKs it.
	// Once the sink sees that packet, the first call is deterministically inside addPath's
	// handshake window, which is exactly where the old read-check-then-append race lived.
	sink, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("listen sink: %v", err)
	}
	defer func() { _ = sink.Close() }()
	blockedConn, err := net.DialUDP("udp", nil, sink.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("dial sink: %v", err)
	}
	defer func() { _ = blockedConn.Close() }()

	tun.handshakeTO = 400 * time.Millisecond
	tun.handshakeTry = 1
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- tun.AddPath(context.Background(), 7, PathSpec{Conn: blockedConn})
	}()

	buf := make([]byte, 2048)
	if err := sink.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set sink deadline: %v", err)
	}
	if _, _, err := sink.ReadFromUDP(buf); err != nil {
		t.Fatalf("first AddPath never entered PATH_ADD exchange: %v", err)
	}

	secondDone := make(chan error, 1)
	go func() {
		secondDone <- tun.AddPath(context.Background(), 7, PathSpec{})
	}()
	select {
	case err := <-secondDone:
		if err == nil || !strings.Contains(err.Error(), "already registered or being registered") {
			t.Fatalf("concurrent same-ID AddPath error = %v, want reservation rejection", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("concurrent same-ID AddPath did not fail immediately; duplicate registration entered the handshake path")
	}

	if err := <-firstDone; err == nil {
		t.Fatal("blocked first AddPath unexpectedly succeeded without a PATH_ADD ACK")
	}

	// A failed registration must release the reservation. Reusing the same ID against the
	// real relay should succeed, proving the fix does not permanently consume path IDs.
	if err := tun.AddPath(context.Background(), 7, PathSpec{}); err != nil {
		t.Fatalf("AddPath after failed reserved registration: %v", err)
	}
}

func TestClientTunnelDropPathIsResilientAndTellsTheRelay(t *testing.T) {
	r, relayKP := mustRelay(t)
	tun, sessionIndex := mustClientTunnel(t, r, relayKP)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tun.runCtx.Store(&ctx)

	if err := tun.AddPath(context.Background(), 1, PathSpec{}); err != nil {
		t.Fatalf("AddPath: %v", err)
	}
	if got := len(tun.Paths()); got != 2 {
		t.Fatalf("paths before drop = %d, want 2", got)
	}

	if err := tun.DropPath(1, "test teardown"); err != nil {
		t.Fatalf("DropPath: %v", err)
	}
	if got := len(tun.Paths()); got != 1 {
		t.Fatalf("paths after drop = %d, want 1", got)
	}
	if got := len(tun.schedPaths()); got != 1 {
		t.Fatalf("schedPaths() after drop = %d, want 1 (dropped path must stop being scheduled)", got)
	}

	sess := relaySessionFor(r, sessionIndex)
	if sess == nil {
		t.Fatal("relay never created a session for this handshake")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sess.pathByID(1) != nil {
		time.Sleep(10 * time.Millisecond)
	}
	if sess.pathByID(1) != nil {
		t.Fatal("relay still has path 1 registered after PATH_DROP -- it will keep scheduling return traffic onto a dead path until PathDeadTimeout")
	}

	// Dropping one path must not have killed the session: a fresh AddPath still works.
	if err := tun.AddPath(context.Background(), 2, PathSpec{}); err != nil {
		t.Fatalf("AddPath after drop: %v", err)
	}
}

func TestClientTunnelDropPathUnknownIDErrors(t *testing.T) {
	r, relayKP := mustRelay(t)
	tun, _ := mustClientTunnel(t, r, relayKP)

	if err := tun.DropPath(99, "x"); err == nil {
		t.Fatal("expected an error dropping an unregistered path, got nil")
	}
}

func TestPathLoopsStartedCASIsOneShot(t *testing.T) {
	p := NewPath(1, nil)
	if !p.loopsStarted.CompareAndSwap(false, true) {
		t.Fatal("first CompareAndSwap(false, true) should succeed")
	}
	if p.loopsStarted.CompareAndSwap(false, true) {
		t.Fatal("second CompareAndSwap(false, true) should fail -- loops must only start once")
	}
}
