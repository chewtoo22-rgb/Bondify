package bond

import "testing"

func validHandshakeRespForDNSTest() HandshakeRespPayload {
	return HandshakeRespPayload{
		SessionIndex: 1,
		TunnelIP:     "10.77.0.2",
		Prefix:       24,
		GatewayIP:    "10.77.0.1",
		MTU:          1448,
		KeepaliveSec: 10,
	}
}

func TestValidateHandshakeRespAcceptsBoundedCanonicalDNS(t *testing.T) {
	cases := [][]string{
		nil,
		{"1.1.1.1"},
		{"1.1.1.1", "8.8.8.8", "9.9.9.9", "149.112.112.112"},
	}
	for _, dns := range cases {
		p := validHandshakeRespForDNSTest()
		p.DNS = dns
		if err := validateHandshakeResp(p); err != nil {
			t.Fatalf("DNS %v should be accepted: %v", dns, err)
		}
	}
}

func TestValidateHandshakeRespRejectsTooManyDNSServers(t *testing.T) {
	p := validHandshakeRespForDNSTest()
	p.DNS = []string{"1.1.1.1", "8.8.8.8", "9.9.9.9", "149.112.112.112", "208.67.222.222"}
	if err := validateHandshakeResp(p); err == nil {
		t.Fatal("expected oversized DNS list to be rejected")
	}
}

func TestValidateHandshakeRespRejectsNonCanonicalDNS(t *testing.T) {
	for _, dns := range []string{
		"not-an-ip",
		"2001:4860:4860::8888",
		"::ffff:1.1.1.1",
		" 1.1.1.1",
	} {
		p := validHandshakeRespForDNSTest()
		p.DNS = []string{dns}
		if err := validateHandshakeResp(p); err == nil {
			t.Fatalf("DNS %q should be rejected", dns)
		}
	}
}

func TestValidateHandshakeRespRejectsDuplicateDNS(t *testing.T) {
	p := validHandshakeRespForDNSTest()
	p.DNS = []string{"1.1.1.1", "1.1.1.1"}
	if err := validateHandshakeResp(p); err == nil {
		t.Fatal("expected duplicate DNS resolver to be rejected")
	}
}
