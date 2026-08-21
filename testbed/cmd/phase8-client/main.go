//go:build linux

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/chewtoo22-rgb/bondify/core/bond"
	"github.com/chewtoo22-rgb/bondify/core/crypto"
	"github.com/chewtoo22-rgb/bondify/core/pairbond"
	"github.com/chewtoo22-rgb/bondify/core/tun"
)

func main() {
	relay := flag.String("relay", "", "relay host:port")
	relayPubText := flag.String("relay-pubkey", "", "relay public key")
	peer := flag.String("peer", "127.0.0.1:51821", "PairBond peer address")
	directLocal := flag.String("direct-local", "10.60.0.1", "direct uplink source IP")
	tunName := flag.String("tun", "bondify0", "TUN name")
	mtu := flag.Int("mtu", 1408, "TUN MTU")
	flag.Parse()

	if *relay == "" || *relayPubText == "" {
		log.Fatal("phase8-client: -relay and -relay-pubkey are required")
	}
	relayPub, err := crypto.DecodeKey(*relayPubText)
	if err != nil {
		log.Fatalf("phase8-client: relay key: %v", err)
	}
	clientKey, err := crypto.GenerateKeypair()
	if err != nil {
		log.Fatalf("phase8-client: client key: %v", err)
	}
	peerConn, err := pairbond.DialPeerPath(context.Background(), *peer)
	if err != nil {
		log.Fatalf("phase8-client: peer path: %v", err)
	}

	dev, err := tun.Create(*tunName, *mtu)
	if err != nil {
		log.Fatalf("phase8-client: create tun: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	t, cfg, err := bond.DialClient(ctx, bond.ClientConfig{
		RelayAddr:   *relay,
		RelayPubKey: relayPub,
		ClientKey:   clientKey,
		Scheduler:   "round-robin",
		Paths: []bond.PathSpec{
			{LocalAddr: *directLocal},
			{Conn: peerConn},
		},
	}, dev, *mtu)
	if err != nil {
		log.Fatalf("phase8-client: handshake: %v", err)
	}

	localCIDR := fmt.Sprintf("%s/%d", cfg.TunnelIP, cfg.Prefix)
	if err := tun.Configure(*tunName, localCIDR, []string{"0.0.0.0/0"}); err != nil {
		log.Fatalf("phase8-client: configure tun: %v", err)
	}
	log.Printf("phase8-client: session %08x established, paths=%d tunnel=%s", cfg.SessionIndex, len(t.Paths()), localCIDR)

	revokeCh := make(chan os.Signal, 1)
	signal.Notify(revokeCh, syscall.SIGUSR1)
	defer signal.Stop(revokeCh)
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-revokeCh:
			if err := pairbond.DropPeerPath(t, 1, "phase8 explicit revoke"); err != nil {
				log.Printf("phase8-client: revoke error: %v", err)
				return
			}
			log.Printf("phase8-client: peer revoked, paths=%d", len(t.Paths()))
		}
	}()

	if err := t.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("phase8-client: tunnel: %v", err)
	}
}
