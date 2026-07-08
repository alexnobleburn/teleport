package route

import (
	"fmt"
	"log/slog"
	"net"
	"strings"
)

// DirectRoute represents a host route added to bypass VPN for a specific peer IP.
type DirectRoute struct {
	PeerIP     string
	Gateway    string
	LocalIP    string
	IfaceIndex int
	logger     *slog.Logger
}

// EnsureDirectRoute adds a host route for peerIP via the LAN gateway,
// bound to the correct local interface. Requires admin/sudo.
func EnsureDirectRoute(peerIP string, logger *slog.Logger) (*DirectRoute, error) {
	gateway, localIP, ifaceIndex, err := findGatewayAndLocal(peerIP)
	if err != nil {
		return nil, fmt.Errorf("detect gateway for %s: %w", peerIP, err)
	}

	r := &DirectRoute{
		PeerIP:     peerIP,
		Gateway:    gateway,
		LocalIP:    localIP,
		IfaceIndex: ifaceIndex,
		logger:     logger,
	}

	if err := r.add(); err != nil {
		return nil, fmt.Errorf("add route %s via %s (if %s, idx %d): %w", peerIP, gateway, localIP, ifaceIndex, err)
	}

	logger.Info("direct route added (VPN bypass)", "peer", peerIP, "gateway", gateway, "interface", localIP, "ifindex", ifaceIndex)
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

// virtualIfacePatterns matches known virtual/VPN/hypervisor interface names.
// These are skipped when looking for the real LAN interface.
var virtualIfacePatterns = []string{
	"tun", "tap",          // OpenVPN, WireGuard
	"vmware", "vmnet",     // VMware
	"vethernet", "veth",   // Hyper-V, WSL, Docker
	"wsl",                 // WSL
	"docker",              // Docker
	"virbr",               // libvirt
	"br-",                 // Docker bridge
	"utun",                // macOS VPN
	"ipsec",               // IPSec VPN
	"ppp",                 // PPP VPN
	"loopback",            // loopback
	"bluetooth",           // Bluetooth
	"isatap",              // ISATAP tunnel
	"teredo",              // Teredo tunnel
}

// isVirtualInterface checks if an interface name matches known virtual patterns.
func isVirtualInterface(name string) bool {
	lower := strings.ToLower(name)
	for _, pattern := range virtualIfacePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// candidate holds a matching interface for ranking.
type candidate struct {
	gateway    string
	localIP    string
	ifaceIndex int
	isVirtual  bool
}

// findGatewayAndLocal detects the LAN gateway, local IP, and interface index for a given peer IP.
// Prefers physical interfaces (Ethernet, Wi-Fi) over virtual ones (VPN, VMware, WSL).
func findGatewayAndLocal(peerIP string) (gateway, localIP string, ifaceIndex int, err error) {
	peer := net.ParseIP(peerIP)
	if peer == nil {
		return "", "", 0, fmt.Errorf("invalid peer IP: %s", peerIP)
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return "", "", 0, err
	}

	var candidates []candidate

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
			if !ok || ipNet.IP.To4() == nil {
				continue
			}
			if ipNet.Contains(peer) {
				gw := make(net.IP, 4)
				copy(gw, ipNet.IP.Mask(ipNet.Mask))
				gw[3] = 1
				candidates = append(candidates, candidate{
					gateway:    gw.String(),
					localIP:    ipNet.IP.String(),
					ifaceIndex: iface.Index,
					isVirtual:  isVirtualInterface(iface.Name),
				})
			}
		}
	}

	if len(candidates) == 0 {
		return "", "", 0, fmt.Errorf("no local interface found on same subnet as %s", peerIP)
	}

	// Prefer physical (non-virtual) interface
	for _, c := range candidates {
		if !c.isVirtual {
			return c.gateway, c.localIP, c.ifaceIndex, nil
		}
	}

	// Fallback to first candidate (all virtual)
	c := candidates[0]
	return c.gateway, c.localIP, c.ifaceIndex, nil
}
