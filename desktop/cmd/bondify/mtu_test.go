package main

import "testing"

func TestSelectTunnelMTU(t *testing.T) {
	tests := []struct {
		name      string
		relayMTU  int
		fallback  int
		want      int
		wantError bool
	}{
		{"relay wins", 1200, 1408, 1200, false},
		{"legacy fallback", 0, 1408, 1408, false},
		{"minimum", 576, 1408, 576, false},
		{"maximum", 65535, 1408, 65535, false},
		{"bad relay low", 575, 1408, 0, true},
		{"bad relay high", 65536, 1408, 0, true},
		{"bad fallback", 0, 575, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectTunnelMTU(tt.relayMTU, tt.fallback)
			if tt.wantError {
				if err == nil {
					t.Fatalf("selectTunnelMTU(%d, %d) = %d, want error", tt.relayMTU, tt.fallback, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectTunnelMTU(%d, %d): %v", tt.relayMTU, tt.fallback, err)
			}
			if got != tt.want {
				t.Fatalf("selectTunnelMTU(%d, %d) = %d, want %d", tt.relayMTU, tt.fallback, got, tt.want)
			}
		})
	}
}
