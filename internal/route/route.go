package route

import (
	"fmt"
	"log/slog"
	"net"
)

// DirectRoute represents a host route added to bypass VPN for a specific peer IP.
type DirectRoute struct {
	PeerIP  string
	Gateway string
	logger  *slog.Logger
}

// EnsureDirectRoute adds a host route for peerIP via the LAN gateway.
// It detects which local interface is on the same subnet as peerIP,
// infers the gateway (first IP in subnet, e.g. 192.168.0.1),
// and adds a host route. Requires admin/sudo on most systems.
// Returns the route (for cleanup) or error.
func EnsureDirectRoute(peerIP string, logger *slog.Logger) (*DirectRoute, error) {
	gateway, err := findGateway(peerIP)
	if err != nil {
		return nil, fmt.Errorf("detect gateway for %s: %w", peerIP, err)
	}

	r := &DirectRoute{
		PeerIP:  peerIP,
		Gateway: gateway,
		logger:  logger,
	}

	if err := r.add(); err != nil {
		return nil, fmt.Errorf("add route %s via %s: %w", peerIP, gateway, err)
	}

	logger.Info("direct route added (VPN bypass)", "peer", peerIP, "gateway", gateway)
	return r, nil
}

// Remove deletes the host route. Safe to call multiple times.
func (r *DirectRoute) Remove() {
	if err := r.remove(); err != nil {
		r.logger.Warn("failed to remove direct route", "peer", r.PeerIP, "error", err)
	} else {
		r.logger.Info("direct route removed", "peer", r.PeerIP)
	}
}

// findGateway detects the LAN gateway for a given peer IP.
// Looks at local interfaces, finds one on the same subnet,
// and returns the first usable IP in that subnet (typically .1).
func findGateway(peerIP string) (string, error) {
	peer := net.ParseIP(peerIP)
	if peer == nil {
		return "", fmt.Errorf("invalid peer IP: %s", peerIP)
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if ipNet.IP.To4() == nil {
				continue // skip IPv6
			}
			if ipNet.Contains(peer) {
				// Peer is on this subnet. Gateway = first IP in subnet (x.x.x.1)
				gw := make(net.IP, 4)
				copy(gw, ipNet.IP.Mask(ipNet.Mask))
				gw[3] = 1 // e.g. 192.168.0.0 → 192.168.0.1
				return gw.String(), nil
			}
		}
	}

	return "", fmt.Errorf("no local interface found on same subnet as %s", peerIP)
}
