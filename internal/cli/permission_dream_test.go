package cli

import (
	"net"
	"testing"
)

func TestDreamingRefusesInheritedTCPListener(t *testing.T) {
	if dreamListenerAllowed(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}) {
		t.Fatal("TCP accepted without scheduler")
	}
	if !dreamListenerAllowed(&net.UnixAddr{Name: "/private/visor.sock", Net: "unix"}) {
		t.Fatal("Unix refused")
	}
}
