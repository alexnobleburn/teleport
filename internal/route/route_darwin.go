//go:build darwin

package route

import (
	"fmt"
	"os/exec"
)

func (r *DirectRoute) add() error {
	// sudo route add -host <peerIP> <gateway>
	cmd := exec.Command("route", "add", "-host", r.PeerIP, r.Gateway)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}

func (r *DirectRoute) remove() error {
	// sudo route delete -host <peerIP>
	cmd := exec.Command("route", "delete", "-host", r.PeerIP)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, string(out))
	}
	return nil
}
