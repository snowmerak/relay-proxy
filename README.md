# relay-proxy

A lightweight transparent proxy for the [portal-tunnel](https://github.com/gosuda/portal-tunnel) ecosystem.

It sets itself as the system DNS resolver so that `{app}.{relay-domain}` hostnames resolve to `127.0.0.1`, then handles both HTTP and HTTPS traffic locally and forwards them to the correct relay backend.

## How It Works

```
Browser ──► gopher.portal.thumbgo.kr
               │
               ▼ DNS (127.0.0.1:53 — relay-proxy)
           *.portal.thumbgo.kr  →  127.0.0.1
               │
     ┌─────────┴──────────┐
     │ HTTP :80            │ HTTPS :443
     │ Reverse Proxy       │ TCP Passthrough (SNI)
     │ Host header intact  │ bytes forwarded as-is
     └─────────┬───────────┘
               │
               ▼ TCP → portal.thumbgo.kr (real relay IP)
           Relay Server
```

| Port | Protocol | Behaviour |
|------|----------|-----------|
| 53   | UDP DNS  | Returns `127.0.0.1` for relay wildcard subdomains; forwards all other queries to upstream resolvers |
| 80   | HTTP     | Reverse-proxied to the relay; `Host` header set to `{app}.{relay}` |
| 443  | HTTPS    | Raw TCP passthrough using SNI; relay's TLS certificate is presented directly to the browser — relay-proxy never decrypts |

All other DNS queries (non-relay domains) are forwarded to upstream resolvers (`8.8.8.8`, `1.1.1.1` by default).

The relay list is fetched from the [portal-tunnel registry](https://raw.githubusercontent.com/gosuda/portal-tunnel/main/registry.json) and refreshed every 5 minutes.

## Requirements

- Go 1.21+
- **Administrator / root privileges** — ports 53, 80, 443 require elevated access on all OSes
  - Windows: run PowerShell as Administrator
  - macOS/Linux: prefix commands with `sudo`

## Installation

```sh
go install github.com/snowmerak/relay-proxy@latest
```

## Quick Start

```sh
# 1. Point system DNS at relay-proxy (run as admin/root)
relay-proxy -setup-dns

# 2. Run the proxy (run as admin/root — different terminal is fine)
relay-proxy

# 3. When done, restore original DNS
relay-proxy -remove-dns
```

## CLI Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-config` | `config.toml` | Path to config file |
| `-setup-dns` | — | Configure system DNS to `127.0.0.1` and exit |
| `-remove-dns` | — | Restore original DNS settings and exit |
| `-dns-addr` | `127.0.0.1` | DNS address written into system settings |
| `-dns-interface` | *(all active)* | Network interface for DNS setup |

## Configuration

`relay-proxy` looks for `config.toml` in the current working directory. All fields are optional — the values below are the defaults.

```toml
[server]
addr           = ":80"   # HTTP reverse-proxy listener
tls_tunnel_addr = ":443"  # HTTPS TCP-passthrough listener (empty = disabled)

[dns]
enabled   = true
addr      = ":53"
upstreams = ["8.8.8.8:53", "1.1.1.1:53"]  # fallback resolvers for non-relay queries
self_ip   = "127.0.0.1"  # IP returned for relay wildcard A-record queries

[registry]
url              = "https://raw.githubusercontent.com/gosuda/portal-tunnel/main/registry.json"
refresh_interval = "5m"
http_timeout     = "10s"

[circuit_breaker]
failure_threshold     = 5
success_threshold     = 2
open_timeout          = "30s"
health_check_interval = "10s"
health_check_timeout  = "5s"
```

### Deploying as a Shared Proxy

To use relay-proxy on a machine other than localhost (e.g. a home server that multiple devices share):

1. Set `dns.self_ip` to the machine's LAN IP (e.g. `192.168.1.10`).
2. Point each device's DNS server to that IP.
3. Run relay-proxy on that machine with ports 80 and 443 open on the LAN.

## Notes

- **TLS passthrough** means relay-proxy never reads HTTPS content. The relay server must have a valid certificate covering the app subdomain (e.g. a wildcard cert `*.portal.thumbgo.kr`).
- relay-proxy's own outbound HTTP requests (registry fetch, health checks) use a DNS bypass that queries upstream resolvers directly, avoiding the bootstrap loop that would occur if relay-proxy asked itself for relay domain resolutions before the relay list was loaded.
- Circuit breakers open automatically after 5 consecutive failures and recover after a successful health check.
