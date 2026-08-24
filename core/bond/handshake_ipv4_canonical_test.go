package bond

import "testing"

func TestHandshakeRespRejectsIPv4MappedIPv6Endpoints(t *testing.T) {
	tests := []struct {
		name      string
		tunnelIP  string
		gatewayIP string
	}{
		{
			name:      "mapped tunnel",
			tunnelIP:  "::ffff:10.77.0.2",
			gatewayIP: "10.77.0.1",
		},
		{
			name:      "mapped gateway",
			tunnelIP:  "10.77.0.2",
			gatewayIP: "::ffff:10.77.0.1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := HandshakeRespPayload{
				SessionIndex: 1,
				TunnelIP:     tt.tunnelIP,
				Prefix:       24,
				GatewayIP:    tt.gatewayIP,
				MTU:          1500,
				KeepaliveSec: 10,
			}
			encoded, err := payload.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := UnmarshalHandshakeResp(encoded); err == nil {
				t.Fatalf("accepted ambiguous IPv4-mapped IPv6 cfg_push: tunnel=%q gateway=%q", tt.tunnelIP, tt.gatewayIP)
			}
		})
	}
}

func TestHandshakeRespAcceptsCanonicalIPv4Endpoints(t *testing.T) {
	payload := HandshakeRespPayload{
		SessionIndex: 7,
		TunnelIP:     "10.77.0.2",
		Prefix:       24,
		GatewayIP:    "10.77.0.1",
		DNS:          []string{"1.1.1.1"},
		MTU:          1500,
		KeepaliveSec: 10,
	}
	encoded, err := payload.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnmarshalHandshakeResp(encoded)
	if err != nil {
		t.Fatalf("canonical IPv4 cfg_push rejected: %v", err)
	}
	if got.TunnelIP != payload.TunnelIP || got.GatewayIP != payload.GatewayIP {
		t.Fatalf("cfg_push endpoints changed: tunnel=%q gateway=%q", got.TunnelIP, got.GatewayIP)
	}
}
