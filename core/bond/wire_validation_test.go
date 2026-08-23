package bond

import (
	"strings"
	"testing"
)

func validHandshakeRespForTest() HandshakeRespPayload {
	return HandshakeRespPayload{
		SessionIndex: 1,
		TunnelIP:     "10.77.0.2",
		Prefix:       24,
		GatewayIP:    "10.77.0.1",
		DNS:          []string{"1.1.1.1", "2606:4700:4700::1111"},
		MTU:          1408,
		KeepaliveSec: 15,
	}
}

func TestHandshakeRespValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*HandshakeRespPayload)
		wantErr string
	}{
		{"valid", func(*HandshakeRespPayload) {}, ""},
		{"legacy zero mtu", func(p *HandshakeRespPayload) { p.MTU = 0 }, ""},
		{"zero session", func(p *HandshakeRespPayload) { p.SessionIndex = 0 }, "session index"},
		{"bad tunnel ip", func(p *HandshakeRespPayload) { p.TunnelIP = "not-an-ip" }, "tunnel ip"},
		{"ipv6 tunnel ip", func(p *HandshakeRespPayload) { p.TunnelIP = "2001:db8::2" }, "tunnel ip"},
		{"bad gateway", func(p *HandshakeRespPayload) { p.GatewayIP = "nope" }, "gateway ip"},
		{"same gateway", func(p *HandshakeRespPayload) { p.GatewayIP = p.TunnelIP }, "must differ"},
		{"prefix too small", func(p *HandshakeRespPayload) { p.Prefix = 0 }, "prefix"},
		{"prefix too large", func(p *HandshakeRespPayload) { p.Prefix = 31 }, "prefix"},
		{"different subnet", func(p *HandshakeRespPayload) { p.GatewayIP = "10.78.0.1" }, "same subnet"},
		{"mtu too small", func(p *HandshakeRespPayload) { p.MTU = 575 }, "mtu"},
		{"mtu too large", func(p *HandshakeRespPayload) { p.MTU = 65536 }, "mtu"},
		{"negative keepalive", func(p *HandshakeRespPayload) { p.KeepaliveSec = -1 }, "keepalive"},
		{"bad dns", func(p *HandshakeRespPayload) { p.DNS = []string{"dns.example"} }, "dns server"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validHandshakeRespForTest()
			tt.mutate(&p)
			err := p.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestUnmarshalHandshakeRespValidatesDecodedConfig(t *testing.T) {
	p := validHandshakeRespForTest()
	p.GatewayIP = "192.0.2.1"
	b, err := p.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if _, err := UnmarshalHandshakeResp(b); err == nil || !strings.Contains(err.Error(), "same subnet") {
		t.Fatalf("UnmarshalHandshakeResp error = %v, want same-subnet validation failure", err)
	}
}
