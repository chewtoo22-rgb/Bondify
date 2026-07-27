// Command hydra-relay is the HYDRA relay: a single static binary, no database, no cloud
// dependency. Phase 1 scope: one UDP listener, Noise_IK handshake, one shared TUN device,
// kernel IP forwarding + MASQUERADE for internet egress. See ARCHITECTURE.md §4.3 and
// PROTOCOL.md §5 for the full design; multi-path, resource limits, and `status` land in
// later phases.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/chewtoo22-rgb/bondify/core/bond"
	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/proto"
	"github.com/chewtoo22-rgb/bondify/core/tun"
	"github.com/chewtoo22-rgb/bondify/relay/nat"
)

func main() {
	var (
		listen    = flag.String("listen", ":51820", "UDP listen address")
		poolCIDR  = flag.String("pool", "10.77.0.0/24", "tunnel IP pool (relay owns the first address)")
		tunName   = flag.String("tun", "hydra0", "TUN device name")
		mtu       = flag.Int("mtu", 1408, "tunnel MTU pushed to clients (payload MTU, not wire MTU)")
		keyFile   = flag.String("key-file", "/etc/hydra/relay.key", "path to relay private key (generated if absent)")
		dnsList   = flag.String("dns", "1.1.1.1,9.9.9.9", "comma-separated DNS servers pushed to clients")
		natIface  = flag.String("nat-iface", "", "if set, enable ip_forward + MASQUERADE(pool -> this interface) for internet egress")
		keepalive = flag.Int("keepalive", 15, "NAT keepalive interval seconds, pushed to clients")
	)
	flag.Parse()

	key, err := loadOrGenerateKey(*keyFile)
	if err != nil {
		log.Fatalf("relay: key: %v", err)
	}
	log.Printf("relay: public key: %s", crypto.EncodeKey(key.Public))

	dev, err := tun.Create(*tunName, *mtu)
	if err != nil {
		log.Fatalf("relay: create tun: %v", err)
	}

	pool, err := bond.NewIPPool(*poolCIDR)
	if err != nil {
		log.Fatalf("relay: ip pool: %v", err)
	}
	gwCIDR := fmt.Sprintf("%s/%d", pool.Gateway(), pool.Prefix())
	if err := tun.ConfigureLinux(*tunName, gwCIDR, nil); err != nil {
		log.Fatalf("relay: configure tun: %v", err)
	}
	log.Printf("relay: tun %s up, gateway %s, pool %s", *tunName, pool.Gateway(), *poolCIDR)

	if *natIface != "" {
		if err := nat.EnableForwarding(); err != nil {
			log.Fatalf("relay: enable forwarding: %v", err)
		}
		cleanup, err := nat.Masquerade(*poolCIDR, *natIface)
		if err != nil {
			log.Fatalf("relay: masquerade: %v", err)
		}
		defer cleanup()
		log.Printf("relay: NAT enabled, %s -> %s", *poolCIDR, *natIface)
	} else {
		log.Printf("relay: -nat-iface not set; relay will not provide internet egress (test/relay-behind-router mode)")
	}

	var dns []string
	for _, d := range strings.Split(*dnsList, ",") {
		if d = strings.TrimSpace(d); d != "" {
			dns = append(dns, d)
		}
	}

	r, err := bond.NewRelay(bond.RelayConfig{
		ListenAddr: *listen,
		RelayKey:   key,
		PoolCIDR:   *poolCIDR,
		DNS:        dns,
		MTU:        *mtu,
		KeepAlive:  *keepalive,
	}, dev)
	if err != nil {
		log.Fatalf("relay: init: %v", err)
	}

	errCh := make(chan error, 2)
	go func() { errCh <- r.ServeUDP() }()
	go func() { errCh <- r.ServeTUN() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("relay: listening on %s (protocol BOND/%d)", *listen, proto.Version)
	select {
	case err := <-errCh:
		log.Fatalf("relay: fatal: %v", err)
	case sig := <-sigCh:
		log.Printf("relay: received %s, shutting down", sig)
	}
}

func loadOrGenerateKey(path string) (crypto.Keypair, error) {
	if b, err := os.ReadFile(path); err == nil {
		priv, err := crypto.DecodeKey(strings.TrimSpace(string(b)))
		if err != nil {
			return crypto.Keypair{}, err
		}
		return crypto.Keypair{Private: priv, Public: crypto.DerivePublic(priv)}, nil
	}
	kp, err := crypto.GenerateKeypair()
	if err != nil {
		return crypto.Keypair{}, err
	}
	if dir := parentDir(path); dir != "" {
		_ = os.MkdirAll(dir, 0700)
	}
	if err := os.WriteFile(path, []byte(crypto.EncodeKey(kp.Private)+"\n"), 0600); err != nil {
		return crypto.Keypair{}, fmt.Errorf("relay: write key file: %w", err)
	}
	return kp, nil
}

func parentDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}
