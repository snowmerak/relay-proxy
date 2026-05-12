package config

import (
	"time"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Server         ServerConfig         `toml:"server"`
	DNS            DNSConfig            `toml:"dns"`
	Registry       RegistryConfig       `toml:"registry"`
	Discovery      DiscoveryConfig      `toml:"discovery"`
	CircuitBreaker CircuitBreakerConfig `toml:"circuit_breaker"`
	Balancer       BalancerConfig       `toml:"balancer"`
}

type ServerConfig struct {
	Addr    string `toml:"addr"`
	TLSCert string `toml:"tls_cert"`
	TLSKey  string `toml:"tls_key"`
}

type DNSConfig struct {
	Enabled bool   `toml:"enabled"`
	Addr    string `toml:"addr"`
	// Upstreams is the list of upstream DNS servers tried in order on failure.
	Upstreams []string `toml:"upstreams"`
	// SelfIP is the IP address returned in A records for relay wildcard domains.
	// Set this to the public IP of this proxy. Defaults to 127.0.0.1 (local use).
	SelfIP string `toml:"self_ip"`
}

type RegistryConfig struct {
	URL             string        `toml:"url"`
	RefreshInterval time.Duration `toml:"refresh_interval"`
	HTTPTimeout     time.Duration `toml:"http_timeout"`
}

type DiscoveryConfig struct {
	ProbeTTL        time.Duration `toml:"probe_ttl"`
	ProbeTimeout    time.Duration `toml:"probe_timeout"`
	ProbeConcurrent int           `toml:"probe_concurrent"`
}

type CircuitBreakerConfig struct {
	FailureThreshold    uint32        `toml:"failure_threshold"`
	SuccessThreshold    uint32        `toml:"success_threshold"`
	OpenTimeout         time.Duration `toml:"open_timeout"`
	HealthCheckInterval time.Duration `toml:"health_check_interval"`
	HealthCheckTimeout  time.Duration `toml:"health_check_timeout"`
}

type BalancerConfig struct {
	Algorithm string `toml:"algorithm"`
}

func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Addr: ":8080",
		},
		DNS: DNSConfig{
			Enabled:   false,
			Addr:      ":53",
			Upstreams: []string{"8.8.8.8:53", "1.1.1.1:53"},
			SelfIP:    "127.0.0.1",
		},
		Registry: RegistryConfig{
			URL:             "https://raw.githubusercontent.com/gosuda/portal-tunnel/main/registry.json",
			RefreshInterval: 5 * time.Minute,
			HTTPTimeout:     10 * time.Second,
		},
		Discovery: DiscoveryConfig{
			ProbeTTL:        30 * time.Second,
			ProbeTimeout:    3 * time.Second,
			ProbeConcurrent: 10,
		},
		CircuitBreaker: CircuitBreakerConfig{
			FailureThreshold:    5,
			SuccessThreshold:    2,
			OpenTimeout:         30 * time.Second,
			HealthCheckInterval: 10 * time.Second,
			HealthCheckTimeout:  5 * time.Second,
		},
		Balancer: BalancerConfig{
			Algorithm: "weighted-round-robin",
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := Default()
	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
