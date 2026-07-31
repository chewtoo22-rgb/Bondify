//go:build !windows

package main

import "context"

// startTray is a no-op on platforms without a Windows-style tray icon (Linux runs as a
// plain foreground/background process today; see PROTOCOL.md/ARCHITECTURE.md's phase plan
// for any future Linux desktop-integration scope). See tray_windows.go for the real
// implementation this stands in for.
func startTray(cancel context.CancelFunc, diagURL string) {
	_ = cancel
	_ = diagURL
}
