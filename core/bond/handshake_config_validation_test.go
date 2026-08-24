package bond

import "testing"

func validHandshakeRespForTest() HandshakeRespPayload {
	return HandshakeRespPayload{
		SessionIndex: 7,
		TunnelIP:     "10.77.0.2",
		Prefix:       24,
		GatewayIP:    "10.77.0.1",
		DNS:          []string{"1.1.1.1"},
		MTU:          1400,
		KeepaliveSec: 15,
	}
}

func decodeHandshakeRespForTest(t *testing.T, p HandshakeRespPayload) error {
	t.Helper()
	b, err := p.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = UnmarshalHandshakeResp(b)
	return err
}

func TestHandshakeRespAcceptsValidNetworkConfig(t *testing.T) {
	p := validHandshakeRespForTest()
	if err := decodeHandshakeRespForTest(t, p); err != nil {
		t.Fatalf("valid cfg_push rejected: %v", err)
	}

	for _, mtu := range []int{576, 65535} {
		p.MTU = mtu
		if err := decodeHandshakeRespForTest(t, p); err != nil {
			t.Fatalf("boundary MTU %d rejected: %v", mtu, err)
		}
	}
}

func TestHandshakeRespRejectsInvalidNetworkConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*HandshakeRespPayload)
	}{
		{"zero session index", func(p *HandshakeRespPayload) { p.SessionIndex = 0 }},
		{"zero prefix", func(p *HandshakeRespPayload) { p.Prefix = 0 }},
		{"oversized prefix", func(p *HandshakeRespPayload) { p.Prefix = 33 }},
		{"malformed tunnel ip", func(p *HandshakeRespPayload) { p.TunnelIP = "not-an-ip" }},
		{"ipv6 tunnel ip", func(p *HandshakeRespPayload) { p.TunnelIP = "2001:db8::2" }},
		{"malformed gateway", func(p *HandshakeRespPayload) { p.GatewayIP = "not-an-ip" }},
		{"ipv6 gateway", func(p *HandshakeRespPayload) { p.GatewayIP = "2001:db8::1" }},
		{"same tunnel and gateway", func(p *HandshakeRespPayload) { p.TunnelIP = p.GatewayIP }},
		{"different subnets", func(p *HandshakeRespPayload) { p.TunnelIP = "10.78.0.2" }},
		{"mtu below ipv4 floor", func(p *HandshakeRespPayload) { p.MTU = 575 }},
		{"mtu above ipv4 maximum", func(p *HandshakeRespPayload) { p.MTU = 65536 }},
		{"negative keepalive", func(p *HandshakeRespPayload) { p.KeepaliveSec = -1 }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := validHandshakeRespForTest()
			tc.mutate(&p)
			if err := decodeHandshakeRespForTest(t, p); err == nil {
				t.Fatalf("invalid cfg_push accepted: %+v", p)
			}
		})
	}
}
