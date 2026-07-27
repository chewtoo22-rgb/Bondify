// Command bondify is Bondify's Linux CLI client. Phase 2 scope: multi-path (one UDP socket
// per configured local address), Noise_IK handshake on path 0, PATH_ADD for the rest,
// round-robin scheduling, reordering, per-path probing. The desktop tray UI and Windows
// wintun support land in phase 6; this binary is also the foundation for the Linux path of
// that later work (see ARCHITECTURE.md §4 repo layout: desktop/cmd/bondify).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/bond"
	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/tun"
)

func main() {
	var (
		relayAddr   = flag.String("relay", "", "relay address, host:port (required)")
		relayPubKey = flag.String("relay-pubkey", "", "relay public key, base64 (required)")
		keyFile     = flag.String("key-file", "", "path to client private key (generated if absent)")
		tunName     = flag.String("tun", "bondify0", "TUN device name")
		mtu         = flag.Int("mtu", 1408, "expected tunnel MTU (overridden by relay's cfg_push once connected)")
		defRoute    = flag.Bool("default-route", false, "replace the default route with one via the tunnel")
		extraRoutes = flag.String("routes", "", "comma-separated extra CIDRs to route via the tunnel")
		localAddrs  = flag.String("local-addrs", "", "comma-separated local bind IPs, one per uplink/path; append @device to pin a path's egress interface (e.g. 10.60.0.1@wlan0,10.61.0.1@wwan0); omit for a single system-chosen-source path")
	)
	flag.Parse()

	if *relayAddr == "" || *relayPubKey == "" {
		fmt.Fprintln(os.Stderr, "usage: bondify -relay host:port -relay-pubkey <base64> [-tun bondify0] [-default-route] [-routes cidr,cidr] [-local-addrs ip[@device],...]")
		os.Exit(2)
	}
	relayPub, err := crypto.DecodeKey(*relayPubKey)
	if err != nil {
		log.Fatalf("client: bad -relay-pubkey: %v", err)
	}

	var clientKey crypto.Keypair
	if *keyFile != "" {
		clientKey, err = loadOrGenerateKey(*keyFile)
	} else {
		clientKey, err = crypto.GenerateKeypair()
	}
	if err != nil {
		log.Fatalf("client: key: %v", err)
	}
	log.Printf("client: public key: %s", crypto.EncodeKey(clientKey.Public))

	// Pin the relay's real address to its actual egress interface *before* any tunnel
	// route exists, so the UDP socket carrying the tunnel itself never gets routed back
	// into the tunnel it's building — the routing-loop bug in ARCHITECTURE.md §3.1.
	relayHost, _, err := net.SplitHostPort(*relayAddr)
	if err != nil {
		log.Fatalf("client: bad -relay address: %v", err)
	}
	relayIPs, err := net.LookupIP(relayHost)
	if err != nil || len(relayIPs) == 0 {
		log.Fatalf("client: resolve relay host: %v", err)
	}
	relayIP := relayIPs[0]
	if egressDev, err := tun.EgressDevice(relayIP); err != nil {
		log.Printf("client: warning: could not determine egress device for relay %s: %v", relayIP, err)
	} else if err := tun.AddHostRoute(relayIP, egressDev); err != nil {
		log.Printf("client: warning: could not pin relay route via %s: %v", egressDev, err)
	} else {
		log.Printf("client: pinned relay %s via %s (protect-loop guard)", relayIP, egressDev)
	}

	dev, err := tun.Create(*tunName, *mtu)
	if err != nil {
		log.Fatalf("client: create tun: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var paths []bond.PathSpec
	for _, a := range strings.Split(*localAddrs, ",") {
		if a = strings.TrimSpace(a); a != "" {
			// "ip" or "ip@device" -- @device pins the socket to that physical interface
			// (SO_BINDTODEVICE), required once more than one path reaches the same
			// relay address; see core/tun/linux.go's DialUDPViaDevice.
			spec := bond.PathSpec{}
			if idx := strings.IndexByte(a, '@'); idx >= 0 {
				spec.LocalAddr = a[:idx]
				spec.Device = a[idx+1:]
			} else {
				spec.LocalAddr = a
			}
			paths = append(paths, spec)
		}
	}

	t, cfg, err := bond.DialClient(ctx, bond.ClientConfig{
		RelayAddr:   *relayAddr,
		RelayPubKey: relayPub,
		ClientKey:   clientKey,
		Paths:       paths,
	}, dev, *mtu)
	if err != nil {
		log.Fatalf("client: handshake failed: %v", err)
	}
	log.Printf("client: session %08x established, tunnel ip %s/%d, gateway %s, mtu %d, paths=%d",
		cfg.SessionIndex, cfg.TunnelIP, cfg.Prefix, cfg.GatewayIP, cfg.MTU, len(t.Paths()))
	for _, perr := range t.PathErrors() {
		log.Printf("client: warning: %v", perr)
	}

	localCIDR := fmt.Sprintf("%s/%d", cfg.TunnelIP, cfg.Prefix)
	var routes []string
	if *defRoute {
		routes = append(routes, "0.0.0.0/0")
	}
	for _, r := range strings.Split(*extraRoutes, ",") {
		if r = strings.TrimSpace(r); r != "" {
			routes = append(routes, r)
		}
	}
	if err := tun.ConfigureLinux(*tunName, localCIDR, routes); err != nil {
		log.Fatalf("client: configure tun: %v", err)
	}
	log.Printf("client: tun %s up at %s, routes=%v", *tunName, localCIDR, routes)

	go statsLoop(ctx, t)

	if err := t.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("client: tunnel error: %v", err)
	}
	log.Println("client: shut down")
}

func statsLoop(ctx context.Context, t *bond.ClientTunnel) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			log.Printf("client: tx=%dpkt/%dB rx=%dpkt/%dB rxerr=%d",
				t.Stats.TxPackets, t.Stats.TxBytes, t.Stats.RxPackets, t.Stats.RxBytes, t.Stats.RxErrors)
			for _, p := range t.Paths() {
				log.Printf("client:   path %d state=%s tx=%dpkt/%dB rx=%dpkt/%dB rtt_min=%s loss=%.1f%%",
					p.ID(), p.State(), p.Stats.TxPackets, p.Stats.TxBytes, p.Stats.RxPackets, p.Stats.RxBytes,
					p.RTTMin(), p.Loss()*100)
			}
		}
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
	if err := os.WriteFile(path, []byte(crypto.EncodeKey(kp.Private)+"\n"), 0600); err != nil {
		return crypto.Keypair{}, fmt.Errorf("client: write key file: %w", err)
	}
	return kp, nil
}
