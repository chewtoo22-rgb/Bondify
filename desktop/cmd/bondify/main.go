// Command bondify is Bondify's desktop CLI client, for Linux and Windows.
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
	"github.com/chewtoo22-rgb/bondify/core/budget"
	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/diag"
	"github.com/chewtoo22-rgb/bondify/core/split"
	"github.com/chewtoo22-rgb/bondify/core/tun"
)

func main() {
	var (
		relayAddr    = flag.String("relay", "", "relay address, host:port (required)")
		relayPubKey  = flag.String("relay-pubkey", "", "relay public key, base64 (required)")
		keyFile      = flag.String("key-file", "", "path to client private key (generated if absent)")
		tunName      = flag.String("tun", "bondify0", "TUN device name")
		mtu          = flag.Int("mtu", 1408, "fallback tunnel MTU for legacy peers that do not push one")
		defRoute     = flag.Bool("default-route", false, "replace the default route with one via the tunnel")
		extraRoutes  = flag.String("routes", "", "comma-separated extra CIDRs to route via the tunnel")
		splitTunnel  = flag.Bool("split-tunnel", true, "with -default-route, bypass curated local/private/link-local/CGNAT CIDRs on the original physical route")
		bypassRoutes = flag.String("bypass-routes", "", "comma-separated additional IPv4 CIDRs to bypass on the original physical route; requires -default-route")
		localAddrs   = flag.String("local-addrs", "", "comma-separated local bind IPs, one per uplink/path; append @device to pin a path's egress interface (e.g. 10.60.0.1@wlan0,10.61.0.1@wwan0); omit for a single system-chosen-source path")
		diagAddr     = flag.String("diag-addr", "127.0.0.1:9090", "localhost address to serve live JSON diagnostics on (GET /api/v1/diagnostics); empty disables it")
		scheduler    = flag.String("scheduler", "round-robin", "scheduling tier: round-robin, weighted-goodput, min-rtt-cwnd, hol-aware")
		mode         = flag.String("mode", "speed", "sending mode: speed (scheduler-picked single path per packet) or redundant (duplicate onto 2 paths)")
		fec          = flag.Bool("fec", false, "adaptive Reed-Solomon FEC on speed-mode traffic")
		classifyFl   = flag.Bool("classify", false, "Tier 5 traffic-class routing; ignored in -mode redundant")
		bulkLimit    = flag.Int64("bulk-limit-bps", 0, "optional BULK pacing ceiling in bits per second; enables -classify, 0 is unlimited")
		bulkQueue    = flag.Int("bulk-queue-packets", bond.DefaultBulkQueuePackets, "maximum copied BULK packets waiting for pacing/headroom; queue-full drops are reported in diagnostics")
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

	// Pin the relay's real address before any tunnel route exists, so the transport socket
	// can never be routed back into the tunnel it is building.
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var paths []bond.PathSpec
	for _, a := range strings.Split(*localAddrs, ",") {
		if a = strings.TrimSpace(a); a != "" {
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

	sendMode, err := bond.ModeFromString(*mode)
	if err != nil {
		log.Fatalf("client: bad -mode: %v", err)
	}
	if *fec && sendMode == bond.ModeRedundant {
		log.Printf("client: warning: -fec has no effect in -mode redundant; disabling it")
		*fec = false
	}
	if *classifyFl && sendMode == bond.ModeRedundant {
		log.Printf("client: warning: -classify has no effect in -mode redundant; disabling it")
		*classifyFl = false
	}
	bulkBudget, err := budget.FromBitsPerSecond(*bulkLimit)
	if err != nil {
		log.Fatalf("client: bad -bulk-limit-bps: %v", err)
	}
	if *bulkQueue < 1 {
		log.Fatalf("client: -bulk-queue-packets must be >= 1")
	}
	if *bulkLimit > 0 {
		if sendMode == bond.ModeRedundant {
			log.Fatalf("client: -bulk-limit-bps requires -mode speed")
		}
		if !*classifyFl {
			log.Printf("client: enabling -classify because -bulk-limit-bps is set")
			*classifyFl = true
		}
	}

	// Desktop now follows the same ordering as Android: authenticate cfg_push first, then
	// create the platform TUN with the relay-selected MTU. This makes the relay's MTU an
	// actual transport invariant rather than a value that is merely logged after startup.
	t, cfg, err := bond.DialHandshake(ctx, bond.ClientConfig{
		RelayAddr:        *relayAddr,
		RelayPubKey:      relayPub,
		ClientKey:        clientKey,
		Paths:            paths,
		Scheduler:        *scheduler,
		Mode:             sendMode,
		FEC:              *fec,
		Classify:         *classifyFl,
		BulkBudget:       bulkBudget,
		BulkQueuePackets: *bulkQueue,
	})
	if err != nil {
		log.Fatalf("client: handshake failed: %v", err)
	}
	effectiveMTU, err := effectiveTunnelMTU(cfg, *mtu)
	if err != nil {
		log.Fatalf("client: invalid relay cfg_push: %v", err)
	}
	dev, err := tun.Create(*tunName, effectiveMTU)
	if err != nil {
		log.Fatalf("client: create tun: %v", err)
	}
	t.AttachTUN(dev, effectiveMTU)

	log.Printf("client: session %08x established, tunnel ip %s/%d, gateway %s, mtu %d, paths=%d",
		cfg.SessionIndex, cfg.TunnelIP, cfg.Prefix, cfg.GatewayIP, effectiveMTU, len(t.Paths()))
	for _, perr := range t.PathErrors() {
		log.Printf("client: warning: %v", perr)
	}

	localCIDR := fmt.Sprintf("%s/%d", cfg.TunnelIP, cfg.Prefix)
	var installedBypass []string
	if *defRoute {
		customBypass, err := split.Resolve(false, *bypassRoutes)
		if err != nil {
			log.Fatalf("client: bad -bypass-routes: %v", err)
		}
		customSet := make(map[string]struct{}, len(customBypass))
		for _, prefix := range customBypass {
			customSet[prefix.String()] = struct{}{}
		}
		bypass, err := split.Resolve(*splitTunnel, *bypassRoutes)
		if err != nil {
			log.Fatalf("client: split tunnel plan: %v", err)
		}
		for _, prefix := range bypass {
			cidr := prefix.String()
			_, required := customSet[cidr]
			probe, err := split.ProbeAddress(prefix)
			if err == nil {
				err = installBypass(cidr, net.IP(probe.AsSlice()), &installedBypass)
			}
			if err == nil {
				continue
			}
			if required {
				removeInstalledBypass(installedBypass)
				log.Fatalf("client: install requested bypass %s: %v", cidr, err)
			}
			log.Printf("client: split tunnel: skipping unreachable default bypass %s: %v", cidr, err)
		}
		if len(installedBypass) > 0 {
			defer removeInstalledBypass(installedBypass)
		}
	}
	if *bypassRoutes != "" && !*defRoute {
		log.Printf("client: -bypass-routes has no effect without -default-route (non-tunnel destinations already bypass)")
	}
	var routes []string
	if *defRoute {
		routes = append(routes, "0.0.0.0/0")
	}
	for _, r := range strings.Split(*extraRoutes, ",") {
		if r = strings.TrimSpace(r); r != "" {
			routes = append(routes, r)
		}
	}
	if err := tun.Configure(*tunName, localCIDR, routes); err != nil {
		removeInstalledBypass(installedBypass)
		log.Fatalf("client: configure tun: %v", err)
	}
	log.Printf("client: tun %s up at %s mtu=%d routes=%v", *tunName, localCIDR, effectiveMTU, routes)

	go statsLoop(ctx, t)

	var diagURL string
	if *diagAddr != "" {
		srv, err := diag.NewServer(*diagAddr, func() any { return t.Diagnostics() })
		if err != nil {
			log.Printf("client: warning: diagnostics endpoint disabled: %v", err)
		} else {
			diagURL = fmt.Sprintf("http://%s/api/v1/diagnostics", srv.Addr())
			log.Printf("client: diagnostics endpoint listening on %s", diagURL)
			go func() {
				if err := srv.Serve(); err != nil {
					log.Printf("client: diagnostics endpoint error: %v", err)
				}
			}()
			go func() {
				<-ctx.Done()
				_ = srv.Close()
			}()
		}
	}

	go startTray(stop, diagURL)

	if err := t.Run(ctx); err != nil && ctx.Err() == nil {
		log.Printf("client: tunnel error: %v", err)
		return
	}
	log.Println("client: shut down")
}

func installBypass(cidr string, probe net.IP, installed *[]string) error {
	route, err := tun.RouteFor(probe)
	if err != nil {
		return err
	}
	added, err := tun.AddBypassRoute(cidr, route)
	if err != nil {
		return err
	}
	if added {
		*installed = append(*installed, cidr)
		log.Printf("client: split tunnel: bypassing %s via %s", cidr, route.Device)
	}
	return nil
}

func removeInstalledBypass(routes []string) {
	for i := len(routes) - 1; i >= 0; i-- {
		if err := tun.DelRoute(routes[i]); err != nil {
			log.Printf("client: warning: remove bypass route %s: %v", routes[i], err)
		}
	}
}

func statsLoop(ctx context.Context, t *bond.ClientTunnel) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d := t.Diagnostics()
			agg := d.Aggregate
			log.Printf("client: tx=%dpkt/%dB rx=%dpkt/%dB rxerr=%d ack_tx=%d ack_rx=%d rtx=%d",
				agg.TxPackets, agg.TxBytes, agg.RxPackets, agg.RxBytes, agg.RxErrors,
				agg.TxAcks, agg.RxAcks, agg.TxRetries)
			for _, p := range d.Paths {
				log.Printf("client:   path %d state=%s tx=%dpkt/%dB rx=%dpkt/%dB rtt_min=%s loss=%.1f%%",
					p.ID, p.State, p.TxPackets, p.TxBytes, p.RxPackets, p.RxBytes,
					time.Duration(p.RTTMinMS*float64(time.Millisecond)), p.LossPct)
			}
			if pacing := agg.BulkPacing; pacing != nil {
				log.Printf("client:   bulk queue=%d/%d drops=%d/%dB paced=%dpkt/%dB scheduler_waits=%d rate=%dB/s",
					pacing.QueueDepth, pacing.QueueCapacity, pacing.QueueDrops, pacing.QueueDropBytes,
					pacing.SentPackets, pacing.SentBytes, pacing.SchedulerWaits, pacing.Limiter.BytesPerSecond)
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
