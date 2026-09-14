package discovery

import (
	"fmt"
	"net"
	"strings"
)

// Virtual and overlay interface prefixes to ignore in auto-private mode.
var virtualInterfacePrefixes = []string{
	"lo",
	"docker",
	"br-",
	"veth",
	"tun",
	"tap",
	"wg",
	"tailscale",
	"zt",
	"dummy",
	"cni",
	"flannel",
	"calico",
	"kube-ipvs",
	"utun",
	"bridge",
	"awdl", // Apple Wireless Direct Link (not useful for LAN ONVIF)
	"llw",
}

// NetworkScope pairs an active network interface with its primary private IPv4 address.
type NetworkScope struct {
	Interface net.Interface
	IPv4      net.IP
}

// SelectInterfaces discovers and filters local network interfaces for WS-Discovery scanning.
// If explicitInterfaces is non-empty, only the requested interface names are returned (fail-closed if missing).
// Otherwise, auto-private mode selects all active, non-virtual interfaces bearing a private IPv4 address.
func SelectInterfaces(explicitInterfaces []string) ([]NetworkScope, error) {
	allIfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("discovery: failed to list network interfaces: %w", err)
	}

	if len(explicitInterfaces) > 0 {
		return selectExplicitInterfaces(allIfaces, explicitInterfaces)
	}

	return selectAutoPrivateInterfaces(allIfaces)
}

func selectExplicitInterfaces(allIfaces []net.Interface, explicitNames []string) ([]NetworkScope, error) {
	byName := make(map[string]net.Interface, len(allIfaces))
	for _, iface := range allIfaces {
		byName[iface.Name] = iface
	}

	var scopes []NetworkScope
	for _, name := range explicitNames {
		cleanName := strings.TrimSpace(name)
		if cleanName == "" {
			continue
		}
		iface, ok := byName[cleanName]
		if !ok {
			return nil, fmt.Errorf("discovery: interface %q not found", cleanName)
		}
		ipv4, err := findPrivateIPv4(iface)
		if err != nil {
			return nil, fmt.Errorf("discovery: interface %q has no valid private IPv4: %w", cleanName, err)
		}
		scopes = append(scopes, NetworkScope{
			Interface: iface,
			IPv4:      ipv4,
		})
	}

	if len(scopes) == 0 {
		return nil, fmt.Errorf("discovery: no valid explicit interfaces provided")
	}
	return scopes, nil
}

func selectAutoPrivateInterfaces(allIfaces []net.Interface) ([]NetworkScope, error) {
	var scopes []NetworkScope

	for _, iface := range allIfaces {
		// Must be UP and support MULTICAST
		if (iface.Flags&net.FlagUp) == 0 || (iface.Flags&net.FlagMulticast) == 0 {
			continue
		}
		// Skip loopback
		if (iface.Flags & net.FlagLoopback) != 0 {
			continue
		}

		// Skip virtual / overlay interfaces
		if isVirtualInterface(iface.Name) {
			continue
		}

		ipv4, err := findPrivateIPv4(iface)
		if err != nil || ipv4 == nil {
			continue
		}

		scopes = append(scopes, NetworkScope{
			Interface: iface,
			IPv4:      ipv4,
		})
	}

	return scopes, nil
}

func isVirtualInterface(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range virtualInterfacePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func findPrivateIPv4(iface net.Interface) (net.IP, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}

	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}

		if ip == nil {
			continue
		}
		ipv4 := ip.To4()
		if ipv4 == nil || ipv4.IsLoopback() {
			continue
		}

		for _, netw := range allowedPrivateNets {
			if netw.Contains(ipv4) {
				return ipv4, nil
			}
		}
	}

	return nil, fmt.Errorf("no private IPv4 address on interface %s", iface.Name)
}
