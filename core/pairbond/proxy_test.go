package pairbond

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestPeerProxyForwardsOpaqueDatagramsAndRevokes(t *testing.T) {
	relay, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("relay listen: %v", err)
	}
	defer relay.Close()

	proxy, err := NewPeerProxy(ProxyConfig{
		ListenAddr:    "127.0.0.1:0",
		RelayAddr:     relay.LocalAddr().String(),
		AllowedHostIP: net.ParseIP("127.0.0.1"),
	})
	if err != nil {
		t.Fatalf("new proxy: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- proxy.Serve(ctx) }()

	host, err := DialPeerPath(context.Background(), proxy.LocalAddr().String())
	if err != nil {
		t.Fatalf("dial host path: %v", err)
	}
	defer host.Close()

	wantUp := []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x01}
	if _, err := host.Write(wantUp); err != nil {
		t.Fatalf("host write: %v", err)
	}

	if err := relay.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	n, peerWAN, err := relay.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("relay read: %v", err)
	}
	if got := string(buf[:n]); got != string(wantUp) {
		t.Fatalf("relay got %x, want %x", buf[:n], wantUp)
	}

	wantDown := []byte{0xca, 0xfe, 0xba, 0xbe, 0x02}
	if _, err := relay.WriteToUDP(wantDown, peerWAN); err != nil {
		t.Fatalf("relay reply: %v", err)
	}
	if err := host.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err = host.Read(buf)
	if err != nil {
		t.Fatalf("host read: %v", err)
	}
	if got := string(buf[:n]); got != string(wantDown) {
		t.Fatalf("host got %x, want %x", buf[:n], wantDown)
	}

	if err := proxy.Revoke(); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	select {
	case err := <-serveErr:
		if !errors.Is(err, ErrRevoked) {
			t.Fatalf("Serve after revoke = %v, want ErrRevoked", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop immediately after revoke")
	}

	_ = relay.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, _ = host.Write([]byte("must-not-forward"))
	if _, _, err := relay.ReadFromUDP(buf); err == nil {
		t.Fatal("relay received data after proxy revoke")
	}
}

func TestPeerProxyRequiresPairedHostIP(t *testing.T) {
	_, err := NewPeerProxy(ProxyConfig{ListenAddr: "127.0.0.1:0", RelayAddr: "127.0.0.1:9"})
	if err == nil {
		t.Fatal("expected missing AllowedHostIP to fail closed")
	}
}

func TestAddPeerPathRejectsNilTunnel(t *testing.T) {
	if err := AddPeerPath(context.Background(), nil, 7, "127.0.0.1:1"); err == nil {
		t.Fatal("expected nil tunnel error")
	}
}

func TestDropPeerPathRejectsNilTunnel(t *testing.T) {
	if err := DropPeerPath(nil, 7, "test"); err == nil {
		t.Fatal("expected nil tunnel error")
	}
}
