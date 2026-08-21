package main

import (
	"context"
	"flag"
	"log"
	"net"
	"os/signal"
	"syscall"

	"github.com/chewtoo22-rgb/bondify/core/pairbond"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:51821", "LAN UDP listen address")
	relay := flag.String("relay", "", "Bondify relay address")
	allowed := flag.String("allowed-host", "127.0.0.1", "paired host LAN IP")
	wanLocal := flag.String("wan-local", "", "optional peer WAN source IP")
	flag.Parse()

	if *relay == "" {
		log.Fatal("pairbond-proxy: -relay is required")
	}
	hostIP := net.ParseIP(*allowed)
	if hostIP == nil {
		log.Fatalf("pairbond-proxy: invalid -allowed-host %q", *allowed)
	}

	proxy, err := pairbond.NewPeerProxy(pairbond.ProxyConfig{
		ListenAddr:    *listen,
		RelayAddr:     *relay,
		AllowedHostIP: hostIP,
		WANLocalAddr:  *wanLocal,
	})
	if err != nil {
		log.Fatalf("pairbond-proxy: create: %v", err)
	}
	defer func() { _ = proxy.Close() }()
	log.Printf("pairbond-proxy: listening=%s relay=%s wan-local=%s", proxy.LocalAddr(), *relay, *wanLocal)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := proxy.Serve(ctx); err != nil && ctx.Err() == nil && err != pairbond.ErrRevoked {
		log.Fatalf("pairbond-proxy: serve: %v", err)
	}
}
