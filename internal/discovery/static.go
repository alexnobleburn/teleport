package discovery

import "context"

// staticDiscovery is used with -peer host:port flag.
// Discover immediately returns one Peer. Announce is a no-op.
type staticDiscovery struct {
	peer Peer
}

// NewStatic creates a Discovery that returns a single fixed peer.
func NewStatic(addr string) Discovery {
	return &staticDiscovery{
		peer: Peer{
			Name: "peer",
			Addr: addr,
		},
	}
}

func (d *staticDiscovery) Announce(ctx context.Context) error {
	// No-op: direct connection, no announcement needed.
	<-ctx.Done()
	return ctx.Err()
}

func (d *staticDiscovery) Discover(ctx context.Context) (<-chan Peer, error) {
	ch := make(chan Peer, 1)
	ch <- d.peer
	close(ch)
	return ch, nil
}
