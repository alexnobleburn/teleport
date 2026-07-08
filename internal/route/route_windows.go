//go:build windows

package route

import (
	"fmt"
	"os/exec"
)

func (r *DirectRoute) add() error {
	// route add <peerIP> mask 255.255.255.255 <gateway>
	cmd := exec.Command("route", "add", r.PeerIP, "mask", "255.255.255.255", r.Gateway)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

func (r *DirectRoute) remove() error {
	// route delete <peerIP>
	cmd := exec.Command("route", "delete", r.PeerIP)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}
