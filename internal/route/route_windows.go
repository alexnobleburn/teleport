//go:build windows

package route

import (
	"fmt"
	"os/exec"
	"strconv"
)

func (r *DirectRoute) add() error {
	// First, delete any existing routes to the peer IP (e.g. VPN-injected routes)
	exec.Command("route", "delete", r.PeerIP).Run() // ignore error — route may not exist

	// route add <peerIP> mask 255.255.255.255 <gateway> METRIC 1 IF <interfaceIndex>
	ifIdx := strconv.Itoa(r.IfaceIndex)
	cmd := exec.Command("route", "add", r.PeerIP, "mask", "255.255.255.255", r.Gateway, "METRIC", "1", "IF", ifIdx)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

func (r *DirectRoute) remove() error {
	cmd := exec.Command("route", "delete", r.PeerIP)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}
