//go:build !linux

package tray

// No L2 on this platform: neither Windows nor macOS offers an unprivileged
// "who owns this listening socket" query that x/sys exposes.
//
// Rather than fall back to the pid file alone — the claim-only path §5.2 of the
// tray spec removed, because a claim is not an identity — this leaves the
// default fail-closed. Stop Gateway is therefore refused here, and the menu
// points at the dashboard instead.
//
// §9 of the tray spec records the upgrade: a UDS / named-pipe control channel
// lets the kernel attest the peer, which solves all three platforms without
// unsafe code. It needs a gateway change and its own review.
