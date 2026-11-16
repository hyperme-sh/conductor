package cli

import (
	"errors"
	"net"
)

// Provisioning names the mesh interface. Do not infer it from a route or accept
// wildcard/public listener addresses. This does not create or configure peers.
func validatePhoneInterface(listen, name string) error {
	refused := errors.New("phone mesh interface unavailable")
	host, _, err := net.SplitHostPort(listen)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsPrivate() || name == "" {
		return refused
	}
	iface, err := net.InterfaceByName(name)
	if err != nil || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
		return refused
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return refused
	}
	for _, address := range addresses {
		candidate, _, err := net.ParseCIDR(address.String())
		if err == nil && candidate.Equal(ip) {
			return nil
		}
	}
	return refused
}
