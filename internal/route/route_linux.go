//go:build linux

package route

import (
	"fmt"
	"os/exec"
)

func (r *DirectRoute) add() error {
	// ip route add <peerIP>/32 via <gateway> dev <iface>
	// First delete any existing route
	exec.Command("ip", "route", "del", r.PeerIP+"/32").Run()

	cmd := exec.Command("ip", "route", "add", r.PeerIP+"/32", "via", r.Gateway)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

func (r *DirectRoute) remove() error {
	cmd := exec.Command("ip", "route", "del", r.PeerIP+"/32")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}
