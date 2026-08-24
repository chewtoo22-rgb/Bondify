package bond

import "testing"

func TestValidateHandshakeRespRejectsReservedIPv4Endpoints(t *testing.T) {
	base := HandshakeRespPayload{
		SessionIndex: 1,
		TunnelIP:     "10.77.0.2",
		Prefix:       24,
		GatewayIP:    "10.77.0.1",
		MTU:          1280,
		KeepaliveSec: 15,
	}

	tests := []struct {
		name      string
		tunnelIP  string
		gatewayIP string
	}{
		{name: "tunnel network address", tunnelIP: "10.77.0.0", gatewayIP: "10.77.0.1"},
		{name: "tunnel broadcast address", tunnelIP: "10.77.0.255", gatewayIP: "10.77.0.1"},
		{name: "gateway network address", tunnelIP: "10.77.0.2", gatewayIP: "10.77.0.0"},
		{name: "gateway broadcast address", tunnelIP: "10.77.0.2", gatewayIP: "10.77.0.255"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.TunnelIP = tc.tunnelIP
			p.GatewayIP = tc.gatewayIP
			if err := validateHandshakeResp(p); err == nil {
				t.Fatalf("validateHandshakeResp(%+v) unexpectedly accepted reserved endpoint", p)
			}
		})
	}
}

func TestValidateHandshakeRespAllowsUsableSubnetEndpoints(t *testing.T) {
	tests := []HandshakeRespPayload{
		{
			SessionIndex: 1,
			TunnelIP:     "10.77.0.2",
			Prefix:       24,
			GatewayIP:    "10.77.0.1",
			MTU:          576,
		},
		{
			SessionIndex: 2,
			TunnelIP:     "192.0.2.0",
			Prefix:       31,
			GatewayIP:    "192.0.2.1",
			MTU:          65535,
			KeepaliveSec: 30,
		},
	}

	for _, p := range tests {
		if err := validateHandshakeResp(p); err != nil {
			t.Fatalf("validateHandshakeResp(%+v) rejected usable endpoints: %v", p, err)
		}
	}
}
