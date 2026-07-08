package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	MulticastAddr    = "239.255.77.55:9877"
	AnnounceInterval = 3 * time.Second
	maxPacketSize    = 256
)

type multicastDiscovery struct {
	name    string
	tcpPort int
	logger  *slog.Logger
	known   map[string]Peer
	mu      sync.Mutex
}

// NewMulticast creates a Discovery that uses UDP multicast for peer discovery.
func NewMulticast(name string, tcpPort int, logger *slog.Logger) Discovery {
	return &multicastDiscovery{
		name:    name,
		tcpPort: tcpPort,
		logger:  logger,
		known:   make(map[string]Peer),
	}
}

func (d *multicastDiscovery) Announce(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp4", MulticastAddr)
	if err != nil {
		return fmt.Errorf("resolve multicast addr: %w", err)
	}

	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return fmt.Errorf("dial multicast: %w", err)
	}
	defer conn.Close()

	packet := fmt.Sprintf("TELEPORT|%s|%d", d.name, d.tcpPort)

	ticker := time.NewTicker(AnnounceInterval)
	defer ticker.Stop()

	// Send immediately, then on interval.
	// Multicast TTL defaults to 1 (single LAN segment), which is sufficient for our use case.
	if _, err := conn.Write([]byte(packet)); err != nil {
		d.logger.Debug("initial announce failed", "error", err)
	}
	d.logger.Debug("announce sent", "packet", packet)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := conn.Write([]byte(packet)); err != nil {
				d.logger.Debug("announce write failed", "error", err)
			}
		}
	}
}

func (d *multicastDiscovery) Discover(ctx context.Context) (<-chan Peer, error) {
	addr, err := net.ResolveUDPAddr("udp4", MulticastAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve multicast addr: %w", err)
	}

	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("listen multicast: %w", err)
	}
	conn.SetReadBuffer(maxPacketSize * 16)

	ch := make(chan Peer, 4)
	go func() {
		defer conn.Close()
		defer close(ch)

		buf := make([]byte, maxPacketSize)
		for {
			if ctx.Err() != nil {
				return
			}
			conn.SetReadDeadline(time.Now().Add(1 * time.Second))
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				// Timeout is expected, just retry
				continue
			}

			peer, ok := parseAnnounce(string(buf[:n]), src)
			if !ok {
				continue
			}

			// Skip self
			if peer.Name == d.name {
				continue
			}

			// Dedup: only send if new or changed
			d.mu.Lock()
			existing, exists := d.known[peer.Name]
			if exists && existing.Addr == peer.Addr {
				d.mu.Unlock()
				continue
			}
			d.known[peer.Name] = peer
			d.mu.Unlock()

			d.logger.Info("peer discovered", "name", peer.Name, "addr", peer.Addr)
			select {
			case ch <- peer:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

// parseAnnounce parses "TELEPORT|<name>|<tcp-port>" from a UDP packet.
func parseAnnounce(data string, src *net.UDPAddr) (Peer, bool) {
	parts := strings.SplitN(data, "|", 3)
	if len(parts) != 3 || parts[0] != "TELEPORT" {
		return Peer{}, false
	}
	name := parts[1]
	port := parts[2]
	if name == "" || port == "" {
		return Peer{}, false
	}
	return Peer{
		Name:    name,
		Addr:    net.JoinHostPort(src.IP.String(), port),
		SrcAddr: src.String(),
	}, true
}
