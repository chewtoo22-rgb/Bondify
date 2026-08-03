// Command rttprobe is a tiny persistent TCP echo probe for Phase 7's mixed-traffic gate.
// The server binds port 22 in the relay namespace so Bondify's real SSH fallback
// classification marks both request and response as INTERACTIVE; the client reports robust
// RTT percentiles while a concurrent reverse iperf3 flow supplies BULK traffic.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sort"
	"time"
)

type result struct {
	Count    int
	MinMS    float64
	MedianMS float64
	P95MS    float64
	MaxMS    float64
}

func main() {
	var (
		mode     = flag.String("mode", "client", "client or server")
		addr     = flag.String("addr", "127.0.0.1:22", "server listen address or client destination")
		count    = flag.Int("count", 100, "number of request/echo RTT samples in client mode")
		interval = flag.Duration("interval", 25*time.Millisecond, "delay between samples")
		timeout  = flag.Duration("timeout", 2*time.Second, "per-operation client deadline")
		payload  = flag.Int("payload", 32, "bytes per echo sample")
	)
	flag.Parse()

	switch *mode {
	case "server":
		if err := serve(*addr); err != nil {
			log.Fatalf("rttprobe server: %v", err)
		}
	case "client":
		if *count < 1 || *payload < 1 || *timeout <= 0 || *interval < 0 {
			fmt.Fprintln(os.Stderr, "rttprobe: count/payload/timeout must be positive and interval non-negative")
			os.Exit(2)
		}
		samples, err := probe(*addr, *count, *payload, *interval, *timeout)
		if err != nil {
			log.Fatalf("rttprobe client: %v", err)
		}
		r := summarize(samples)
		output := map[string]any{
			"count":     r.Count,
			"min_ms":    r.MinMS,
			"median_ms": r.MedianMS,
			"p95_ms":    r.P95MS,
			"max_ms":    r.MaxMS,
		}
		if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
			log.Fatalf("rttprobe encode: %v", err)
		}
	default:
		fmt.Fprintf(os.Stderr, "rttprobe: unknown -mode %q\n", *mode)
		os.Exit(2)
	}
}

func serve(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer func() { _ = conn.Close() }()
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetNoDelay(true)
			}
			_, _ = io.Copy(conn, conn)
		}()
	}
}

func probe(addr string, count, payloadBytes int, interval, timeout time.Duration) ([]time.Duration, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetNoDelay(true)
	}
	payload := make([]byte, payloadBytes)
	echo := make([]byte, payloadBytes)
	samples := make([]time.Duration, 0, count)
	for i := 0; i < count; i++ {
		if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
			return nil, err
		}
		start := time.Now()
		if _, err := conn.Write(payload); err != nil {
			return nil, fmt.Errorf("sample %d write: %w", i, err)
		}
		if _, err := io.ReadFull(conn, echo); err != nil {
			return nil, fmt.Errorf("sample %d read: %w", i, err)
		}
		samples = append(samples, time.Since(start))
		if i+1 < count && interval > 0 {
			time.Sleep(interval)
		}
	}
	return samples, nil
}

func summarize(samples []time.Duration) result {
	if len(samples) == 0 {
		return result{}
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	toMS := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	return result{
		Count:    len(sorted),
		MinMS:    toMS(sorted[0]),
		MedianMS: toMS(percentile(sorted, 50)),
		P95MS:    toMS(percentile(sorted, 95)),
		MaxMS:    toMS(sorted[len(sorted)-1]),
	}
}

func percentile(sorted []time.Duration, percent int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	// Nearest-rank percentile: ceil(percent*N/100)-1.
	index := (percent*len(sorted) + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(sorted) {
		index = len(sorted)
	}
	return sorted[index-1]
}
