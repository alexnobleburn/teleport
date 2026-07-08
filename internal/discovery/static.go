package discovery

import (
	"context"
	"time"
)

const retryInterval = 5 * time.Second

// staticDiscovery is used with -peer host:port flag.
// Discover sends the peer immediately, then resends every retryInterval
// to allow reconnection if the first attempt fails.
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
	<-ctx.Done()
	return ctx.Err()
}

func (d *staticDiscovery) Discover(ctx context.Context) (<-chan Peer, error) {
	ch := make(chan Peer, 1)
	go func() {
		defer close(ch)

		// Send immediately
		select {
		case ch <- d.peer:
		case <-ctx.Done():
			return
		}

		// Resend periodically to allow reconnection
		ticker := time.NewTicker(retryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case ch <- d.peer:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}
