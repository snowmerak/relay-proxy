//go:build windows

package dnssetup

import (
	"fmt"
	"os/exec"
	"strings"
)

func setup(proxyAddr, iface string) error {
	ifaces, err := resolveInterfaces(iface)
	if err != nil {
		return err
	}
	var errs []string
	for _, i := range ifaces {
		if err := netshSetDNS(i, proxyAddr); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", i, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("DNS setup errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func remove(iface string) error {
	ifaces, err := resolveInterfaces(iface)
	if err != nil {
		return err
	}
	var errs []string
	for _, i := range ifaces {
		if err := netshResetDNS(i); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", i, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("DNS removal errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func resolveInterfaces(iface string) ([]string, error) {
	if iface != "" {
		return []string{iface}, nil
	}
	return activeWindowsInterfaces()
}

// activeWindowsInterfaces returns the names of connected network interfaces
// by querying via PowerShell (more reliable than netsh text parsing).
func activeWindowsInterfaces() ([]string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-Command",
		`Get-NetAdapter | Where-Object {$_.Status -eq 'Up'} | Select-Object -ExpandProperty Name`)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("Get-NetAdapter: %w", err)
	}
	var ifaces []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			ifaces = append(ifaces, line)
		}
	}
	if len(ifaces) == 0 {
		return nil, fmt.Errorf("no active network interfaces found")
	}
	return ifaces, nil
}

func netshSetDNS(iface, addr string) error {
	// Set primary DNS server.
	out, err := exec.Command("netsh", "interface", "ip", "set", "dns",
		"name="+iface, "static", addr, "primary").CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh set dns: %w: %s", err, out)
	}
	return nil
}

func netshResetDNS(iface string) error {
	// Revert to DHCP-assigned DNS.
	out, err := exec.Command("netsh", "interface", "ip", "set", "dns",
		"name="+iface, "dhcp").CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh reset dns: %w: %s", err, out)
	}
	return nil
}
