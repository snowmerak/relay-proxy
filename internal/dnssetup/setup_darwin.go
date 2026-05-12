//go:build darwin

package dnssetup

import (
	"fmt"
	"os/exec"
	"strings"
)

func setup(proxyAddr, iface string) error {
	services, err := resolveServices(iface)
	if err != nil {
		return err
	}
	var errs []string
	for _, svc := range services {
		out, err := exec.Command("networksetup", "-setdnsservers", svc, proxyAddr).CombinedOutput()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v: %s", svc, err, out))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("DNS setup errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func remove(iface string) error {
	services, err := resolveServices(iface)
	if err != nil {
		return err
	}
	var errs []string
	for _, svc := range services {
		// "Empty" is the networksetup keyword to clear manual DNS entries.
		out, err := exec.Command("networksetup", "-setdnsservers", svc, "Empty").CombinedOutput()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v: %s", svc, err, out))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("DNS removal errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

func resolveServices(iface string) ([]string, error) {
	if iface != "" {
		return []string{iface}, nil
	}
	return activeNetworkServices()
}

// activeNetworkServices returns all enabled network services via networksetup.
func activeNetworkServices() ([]string, error) {
	out, err := exec.Command("networksetup", "-listallnetworkservices").Output()
	if err != nil {
		return nil, fmt.Errorf("networksetup -listallnetworkservices: %w", err)
	}
	var services []string
	for i, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i == 0 {
			// Skip the header: "An asterisk (*) denotes that a network service is disabled."
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "*") {
			continue
		}
		services = append(services, line)
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("no active network services found")
	}
	return services, nil
}
