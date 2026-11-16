package cli

import "testing"

func TestPhoneInterfaceRefusesUnprovisionedAddresses(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8443", "[::]:8443", "127.0.0.1:8443", "8.8.8.8:8443", "example.org:8443", "10.2.3.4:8443"} {
		if validatePhoneInterface(address, "") == nil {
			t.Fatalf("accepted %s without provisioned interface", address)
		}
		if validatePhoneInterface(address, "visor-no-such-interface") == nil {
			t.Fatalf("accepted missing interface for %s", address)
		}
	}
}
