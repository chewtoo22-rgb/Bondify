package pairbond

import (
	"context"
	"fmt"

	"github.com/chewtoo22-rgb/bondify/core/bond"
)

// AddPeerPath dials a paired peer's LAN data endpoint and attaches it to an
// already-established Bondify tunnel using the runtime AddPath API. From the
// tunnel's point of view this is just another connected UDP uplink; the peer
// forwards the already-encrypted Bondify packets to the relay.
func AddPeerPath(ctx context.Context, tunnel *bond.ClientTunnel, pathID uint8, peerAddr string) error {
	if tunnel == nil {
		return fmt.Errorf("pairbond: nil tunnel")
	}
	conn, err := DialPeerPath(ctx, peerAddr)
	if err != nil {
		return err
	}
	if err := tunnel.AddPath(ctx, pathID, bond.PathSpec{Conn: conn}); err != nil {
		_ = conn.Close()
		return fmt.Errorf("pairbond: add peer path %d: %w", pathID, err)
	}
	return nil
}

// DropPeerPath immediately removes a PairBond-contributed path from local
// scheduling and sends Bondify's authenticated PATH_DROP to the relay.
func DropPeerPath(tunnel *bond.ClientTunnel, pathID uint8, reason string) error {
	if tunnel == nil {
		return fmt.Errorf("pairbond: nil tunnel")
	}
	if reason == "" {
		reason = "pairbond revoke"
	}
	return tunnel.DropPath(pathID, reason)
}
