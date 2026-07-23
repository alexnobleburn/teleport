package discovery

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestParseAnnounce_Valid(t *testing.T) {
	src := &net.UDPAddr{IP: net.ParseIP("192.168.1.42"), Port: 9877}
	peer, ok := parseAnnounce("TELEPORT|macbook|9878", src)
	if !ok {
		t.Fatal("should parse valid announce")
	}
	if peer.Name != "macbook" {
		t.Fatalf("name: got %q", peer.Name)
	}
	if peer.Addr != "192.168.1.42:9878" {
		t.Fatalf("addr: got %q", peer.Addr)
	}
}

func TestParseAnnounce_Invalid(t *testing.T) {
	src := &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 9877}
	cases := []string{
		"",
		"WRONG|name|9878",
		"TELEPORT",
		"TELEPORT|",
		"TELEPORT||9878",
		"TELEPORT|name|",
		"TELEPORT|name|0",
		"TELEPORT|name|99999",
		"TELEPORT|name|abc",
		"TELEPORT|name|-1",
		"garbage data",
	}
	for _, data := range cases {
		if _, ok := parseAnnounce(data, src); ok {
			t.Fatalf("should not parse %q", data)
		}
	}
}

func TestStatic_SinglePeer(t *testing.T) {
	disc := NewStatic("localhost:9878")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := disc.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}

	peer, ok := <-ch
	if !ok {
		t.Fatal("expected a peer")
	}
	if peer.Addr != "localhost:9878" {
		t.Fatalf("addr: got %q", peer.Addr)
	}

	// Cancel context to stop the discovery goroutine and close channel.
	cancel()

	// Drain any peer that was already queued before cancel took effect.
	for range ch {
	}
}

func TestStatic_ReEmit(t *testing.T) {
	disc := NewStatic("localhost:9878")
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	ch, err := disc.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// First peer — immediate.
	peer, ok := <-ch
	if !ok {
		t.Fatal("expected first peer")
	}
	if peer.Addr != "localhost:9878" {
		t.Fatalf("addr: got %q", peer.Addr)
	}

	// Second peer — re-emitted after retryInterval (5s).
	peer, ok = <-ch
	if !ok {
		t.Fatal("expected re-emitted peer")
	}
	if peer.Addr != "localhost:9878" {
		t.Fatalf("re-emitted addr: got %q", peer.Addr)
	}
}
