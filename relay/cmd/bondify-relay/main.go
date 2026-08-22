// Command bondify-relay is the Bondify relay: a single static binary, no database, no cloud
// dependency. Phase 1 scope: one UDP listener, Noise_IK handshake, one shared TUN device,
// kernel IP forwarding + MASQUERADE for internet egress. See ARCHITECTURE.md §4.3 and
// PROTOCOL.md §5 for the full design; multi-path, resource limits, and `status` land in
// later phases.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/chewtoo22-rgb/bondify/core/bond"
	"github.com/chewtoo22-rgb/bondify/core/budget"
	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/diag"
	"github.com/chewtoo22-rgb/bondify/core/proto"
	"github.com/chewtoo22-rgb/bondify/core/tun"
	"github.com/chewtoo22-rgb/bondify/relay/nat"
)

func main() {
	var (
		listen      = flag.String("listen", ":51820", "UDP listen address")
		poolCIDR    = flag.String("pool", "10.77.0.0/24", "tunnel IP pool (relay owns the first address)")
		tunName     = flag.String("tun", "bondify0", "TUN device name")
		mtu         = flag.Int("mtu", 1408, "tunnel MTU pushed to clients (payload MTU, not wire MTU)")
		keyFile     = flag.String("key-file", "/etc/bondify/relay.key", "path to relay private key (generated if absent)")
		dnsList     = flag.String("dns", "1.1.1.1,9.9.9.9", "comma-separated DNS servers pushed to clients")
		natIface    = flag.String("nat-iface", "", "if set, enable ip_forward + MASQUERADE(pool -> this interface) for internet egress")
		keepalive   = flag.Int("keepalive", 15, "NAT keepalive interval seconds, pushed to clients")
		sessionIdle = flag.Duration("session-idle-timeout", bond.DefaultSessionIdleTimeout, "reclaim a fully-dead relay session and its tunnel IP after this much inactivity; 0 disables reclamation")
		diagAddr    = flag.String("diag-addr", "127.0.0.1:9091", "localhost address to serve live JSON diagnostics on, one entry per connected session (GET /api/v1/diagnostics); empty disables it")
		scheduler   = flag.String("scheduler", "round-robin", "scheduling tier for the relay's own return traffic: round-robin, weighted-goodput, min-rtt-cwnd, hol-aware")
		mode        = flag.String("mode", "speed", "sending mode for the relay's own return traffic: speed or redundant")
		fec         = flag.Bool("fec", false, "adaptive Reed-Solomon FEC on the relay's own return traffic; opt-in since it copies every packet into a generation buffer even at zero loss")
		classify    = flag.Bool("classify", false, "Tier 5 traffic-class routing on the relay's own return traffic (see the client's -classify flag for the per-class routing); ignored in -mode redundant")
		bulkLimit   = flag.Int64("bulk-limit-bps", 0, "optional return-traffic BULK pacing ceiling in bits per second; enables -classify, 0 is unlimited")
		bulkQueue   = flag.Int("bulk-queue-packets", bond.DefaultBulkQueuePackets, "maximum copied BULK packets waiting for pacing/headroom; queue-full drops are reported in diagnostics")
	)
	flag.Parse()

	if *sessionIdle < 0 {
		log.Fatalf("relay: -session-idle-timeout must be >= 0")
	}

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
	if err := tun.Configure(*tunName, gwCIDR, nil); err != nil {
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

	sendMode, err := bond.ModeFromString(*mode)
	if err != nil {
		log.Fatalf("relay: bad -mode: %v", err)
	}
	if *fec && sendMode == bond.ModeRedundant {
		// See the matching check in desktop/cmd/bondify/main.go: sendRedundant never
		// stamps FlagFECProtected or records into fecSend, so FEC would sit permanently
		// allocated and inert for the relay's own return traffic too.
		log.Printf("relay: warning: -fec has no effect in -mode redundant; disabling it")
		*fec = false
	}
	if *classify && sendMode == bond.ModeRedundant {
		log.Printf("relay: warning: -classify has no effect in -mode redundant; disabling it")
		*classify = false
	}
	bulkBudget, err := budget.FromBitsPerSecond(*bulkLimit)
	if err != nil {
		log.Fatalf("relay: bad -bulk-limit-bps: %v", err)
	}
	if *bulkQueue < 1 {
		log.Fatalf("relay: -bulk-queue-packets must be >= 1")
	}
	if *bulkLimit > 0 {
		if sendMode == bond.ModeRedundant {
			log.Fatalf("relay: -bulk-limit-bps requires -mode speed")
		}
		if !*classify {
			log.Printf("relay: enabling -classify because -bulk-limit-bps is set")
			*classify = true
		}
	}

	r, err := bond.NewRelay(bond.RelayConfig{
		ListenAddr:       *listen,
		RelayKey:         key,
		PoolCIDR:         *poolCIDR,
		DNS:              dns,
		MTU:              *mtu,
		KeepAlive:        *keepalive,
		Scheduler:        *scheduler,
		Mode:             sendMode,
		FEC:              *fec,
		Classify:         *classify,
		BulkBudget:       bulkBudget,
		BulkQueuePackets: *bulkQueue,
	}, dev)
	if err != nil {
		log.Fatalf("relay: init: %v", err)
	}
	defer r.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 2)
	go func() { errCh <- r.ServeUDP() }()
	go func() { errCh <- r.ServeTUN() }()
	go r.ServeManagedReorder(ctx)
	go r.FECMaintenanceLoop(ctx)
	if *sessionIdle > 0 {
		go r.RunSessionReaper(ctx, *sessionIdle)
		log.Printf("relay: idle session reclamation enabled (%s)", sessionIdle.Round(time.Second))
	} else {
		log.Printf("relay: idle session reclamation disabled")
	}

	if *diagAddr != "" {
		srv, err := diag.NewServer(*diagAddr, func() any { return r.Diagnostics() })
		if err != nil {
			log.Printf("relay: warning: diagnostics endpoint disabled: %v", err)
		} else {
			log.Printf("relay: diagnostics endpoint listening on http://%s/api/v1/diagnostics", srv.Addr())
			go func() {
				if err := srv.Serve(); err != nil {
					log.Printf("relay: diagnostics endpoint error: %v", err)
				}
			}()
			go func() {
				<-ctx.Done()
				_ = srv.Close()
			}()
		}
	}

	log.Printf("relay: listening on %s (protocol BOND/%d)", *listen, proto.Version)
	select {
	case err := <-errCh:
		log.Fatalf("relay: fatal: %v", err)
	case <-ctx.Done():
		log.Printf("relay: shutting down")
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
