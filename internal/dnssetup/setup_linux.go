//go:build linux

package dnssetup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	resolvConf    = "/etc/resolv.conf"
	resolvConfBak = "/etc/resolv.conf.relay-proxy.bak"
)

func setup(proxyAddr, iface string) error {
	// Try systemd-resolved first (modern Linux).
	if hasResolvectl() {
		target, err := resolveInterface(iface)
		if err != nil {
			return err
		}
		out, err := exec.Command("resolvectl", "dns", target, proxyAddr).CombinedOutput()
		if err != nil {
			return fmt.Errorf("resolvectl dns: %w: %s", err, out)
		}
		return nil
	}

	// Fall back to /etc/resolv.conf.
	return setupResolvConf(proxyAddr)
}

func remove(iface string) error {
	if hasResolvectl() {
		target, err := resolveInterface(iface)
		if err != nil {
			return err
		}
		// Passing no address reverts the interface to automatic (DHCP) DNS.
		out, err := exec.Command("resolvectl", "revert", target).CombinedOutput()
		if err != nil {
			return fmt.Errorf("resolvectl revert: %w: %s", err, out)
		}
		return nil
	}

	return restoreResolvConf()
}

func hasResolvectl() bool {
	_, err := exec.LookPath("resolvectl")
	return err == nil
}

func resolveInterface(iface string) (string, error) {
	if iface != "" {
		return iface, nil
	}
	return defaultInterface()
}

// defaultInterface reads /proc/net/route to find the interface carrying the
// default route (destination 00000000, mask 00000000).
func defaultInterface() (string, error) {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return "", fmt.Errorf("read /proc/net/route: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		if fields[1] == "00000000" && fields[7] == "00000000" {
			return fields[0], nil
		}
	}
	return "", errors.New("could not determine default network interface")
}

func setupResolvConf(proxyAddr string) error {
	// Backup original file.
	orig, err := os.ReadFile(resolvConf)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", resolvConf, err)
	}
	if err == nil {
		if writeErr := os.WriteFile(resolvConfBak, orig, 0o644); writeErr != nil {
			return fmt.Errorf("backup resolv.conf: %w", writeErr)
		}
	}

	content := fmt.Sprintf("# managed by relay-proxy\nnameserver %s\n", proxyAddr)
	if err := os.WriteFile(resolvConf, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", resolvConf, err)
	}
	return nil
}

func restoreResolvConf() error {
	bak, err := os.ReadFile(resolvConfBak)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("backup file %s not found; restore manually", resolvConfBak)
		}
		return fmt.Errorf("read backup: %w", err)
	}
	if err := os.WriteFile(resolvConf, bak, 0o644); err != nil {
		return fmt.Errorf("restore %s: %w", resolvConf, err)
	}
	return os.Remove(resolvConfBak)
}
