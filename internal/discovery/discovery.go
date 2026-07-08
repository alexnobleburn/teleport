package discovery

import "context"

// Peer represents a discovered device on the network.
type Peer struct {
	Name    string // device name (-name flag)
	Addr    string // IP:port for TCP connection
	SrcAddr string // sender IP (from UDP packet)
}

// Discovery provides peer discovery on the local network.
type Discovery interface {
	// Announce broadcasts presence to the network. Blocking, cancelled via ctx.
	Announce(ctx context.Context) error

	// Discover listens for peer announcements. Sends new/changed peers to channel.
	// Deduplication: same peer is not sent again unless its address changed.
	Discover(ctx context.Context) (<-chan Peer, error)
}
