// Package dnssetup provides OS-specific helpers to point the system DNS
// resolver at relay-proxy's built-in DNS server.
package dnssetup

// Setup sets the system DNS server to proxyAddr on the specified network
// interface. If iface is empty, all active interfaces are configured.
// Requires administrator / root privileges.
func Setup(proxyAddr, iface string) error {
	return setup(proxyAddr, iface)
}

// Remove restores the system DNS settings on the specified interface.
// If iface is empty, all active interfaces are reset.
// Requires administrator / root privileges.
func Remove(iface string) error {
	return remove(iface)
}
