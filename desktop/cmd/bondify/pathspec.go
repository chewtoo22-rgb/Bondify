package main

import (
	"fmt"
	"net"
	"strings"
	"unicode"

	"github.com/chewtoo22-rgb/bondify/core/bond"
)

const maxDesktopPathSpecs = 256

func parseLocalPathSpecs(raw string) ([]bond.PathSpec, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ",")
	if len(parts) > maxDesktopPathSpecs {
		return nil, fmt.Errorf("too many local paths: %d (maximum %d)", len(parts), maxDesktopPathSpecs)
	}

	paths := make([]bond.PathSpec, 0, len(parts))
	seenPairs := make(map[string]struct{}, len(parts))
	seenAddrs := make(map[string]struct{}, len(parts))
	seenDevices := make(map[string]struct{}, len(parts))

	for i, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			return nil, fmt.Errorf("local path %d is empty", i+1)
		}
		if strings.Count(token, "@") > 1 {
			return nil, fmt.Errorf("local path %d contains more than one @ separator", i+1)
		}

		addrText, device := token, ""
		if idx := strings.IndexByte(token, '@'); idx >= 0 {
			addrText = strings.TrimSpace(token[:idx])
			device = strings.TrimSpace(token[idx+1:])
			if device == "" {
				return nil, fmt.Errorf("local path %d has an empty device", i+1)
			}
			if len(device) > 128 {
				return nil, fmt.Errorf("local path %d device name exceeds 128 bytes", i+1)
			}
			if strings.IndexFunc(device, unicode.IsControl) >= 0 {
				return nil, fmt.Errorf("local path %d device name contains control characters", i+1)
			}
		}

		if addrText == "" {
			return nil, fmt.Errorf("local path %d has an empty bind address", i+1)
		}
		ip := net.ParseIP(addrText)
		if ip == nil {
			return nil, fmt.Errorf("local path %d has invalid bind IP %q", i+1, addrText)
		}
		addr := ip.String()

		pairKey := addr + "\x00" + device
		if _, exists := seenPairs[pairKey]; exists {
			return nil, fmt.Errorf("local path %d duplicates bind address/device pair %s@%s", i+1, addr, device)
		}
		seenPairs[pairKey] = struct{}{}

		if len(parts) > 1 {
			if _, exists := seenAddrs[addr]; exists {
				return nil, fmt.Errorf("local path %d reuses bind address %s", i+1, addr)
			}
			seenAddrs[addr] = struct{}{}
			if device != "" {
				if _, exists := seenDevices[device]; exists {
					return nil, fmt.Errorf("local path %d reuses pinned device %q", i+1, device)
				}
				seenDevices[device] = struct{}{}
			}
		}

		paths = append(paths, bond.PathSpec{LocalAddr: addr, Device: device})
	}

	return paths, nil
}
