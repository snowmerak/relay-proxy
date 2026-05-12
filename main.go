package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/snowmerak/relay-proxy/internal/balancer"
	"github.com/snowmerak/relay-proxy/internal/circuitbreaker"
	"github.com/snowmerak/relay-proxy/internal/config"
	"github.com/snowmerak/relay-proxy/internal/discovery"
	"github.com/snowmerak/relay-proxy/internal/dnsserver"
	"github.com/snowmerak/relay-proxy/internal/dnssetup"
	"github.com/snowmerak/relay-proxy/internal/httpclient"
	"github.com/snowmerak/relay-proxy/internal/proxy"
	"github.com/snowmerak/relay-proxy/internal/registry"
)

func main() {
	cfgPath := flag.String("config", "config.toml", "path to config file")
	setupDNS := flag.Bool("setup-dns", false, "configure system DNS to point to this proxy (requires admin/root)")
	removeDNS := flag.Bool("remove-dns", false, "restore system DNS settings (requires admin/root)")
	dnsIface := flag.String("dns-interface", "", "network interface for DNS setup (empty = all active interfaces)")
	dnsAddr := flag.String("dns-addr", "127.0.0.1", "DNS server address to write into system settings")
	flag.Parse()

	if *setupDNS {
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			cfg = config.Default()
		}
		if !cfg.DNS.Enabled {
			slog.Warn("dns.enabled is false in config — system DNS will be set but the DNS server won't start; enable dns in config before running the proxy")
		}
		if err := dnssetup.Setup(*dnsAddr, *dnsIface); err != nil {
			slog.Error("DNS setup failed", "err", err)
			os.Exit(1)
		}
		slog.Info("system DNS configured", "addr", *dnsAddr)
		return
	}

	if *removeDNS {
		if err := dnssetup.Remove(*dnsIface); err != nil {
			slog.Error("DNS removal failed", "err", err)
			os.Exit(1)
		}
		slog.Info("system DNS restored")
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Warn("could not load config file, using defaults", "path", *cfgPath, "err", err)
		cfg = config.Default()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Build a DNS-bypass transport so relay-proxy's own outbound requests
	// (registry fetch, relay probe, health check) never go through the OS
	// resolver — which may be pointed at ourselves via -setup-dns.
	bypassTransport := httpclient.NewBypassTransport(cfg.DNS.Upstreams)

	// 1. Registry fetcher.
	fetcher := registry.NewFetcher(cfg.Registry.URL, cfg.Registry.RefreshInterval, cfg.Registry.HTTPTimeout, bypassTransport)

	// 2. Balancer.
	bal := balancer.NewRoundRobin()

	// 3. Circuit breaker registry.
	cbSettings := circuitbreaker.Settings{
		FailureThreshold: cfg.CircuitBreaker.FailureThreshold,
		SuccessThreshold: cfg.CircuitBreaker.SuccessThreshold,
		OpenTimeout:      cfg.CircuitBreaker.OpenTimeout,
	}
	var mgr *discovery.Manager
	cbReg := circuitbreaker.NewRegistry(
		cbSettings,
		cfg.CircuitBreaker.HealthCheckInterval,
		cfg.CircuitBreaker.HealthCheckTimeout,
		func(relayID string) {
			if mgr != nil {
				mgr.InvalidateRelay(relayID)
			}
		},
		bypassTransport,
	)

	// 4. DNS server (optional).
	var dnsSrv *dnsserver.Server
	if cfg.DNS.Enabled {
		dnsSrv, err = dnsserver.New(cfg.DNS.Addr, cfg.DNS.Upstreams, cfg.DNS.SelfIP)
		if err != nil {
			slog.Error("dns server init failed", "err", err)
			os.Exit(1)
		}
		go func() {
			if srvErr := dnsSrv.Run(ctx); srvErr != nil {
				slog.Error("dns server error", "err", srvErr)
			}
		}()
	}

	// 5. Wire fetcher → circuit breaker + balancer + DNS registration.
	fetcher.Subscribe(func(added, removed []*registry.Relay) {
		for _, r := range added {
			cbReg.Add(ctx, r)
			bal.Register(r)
			slog.Info("relay added", "id", r.ID)
		}
		for _, r := range removed {
			cbReg.Remove(r.ID)
			bal.Deregister(r.ID)
			slog.Info("relay removed", "id", r.ID)
		}
		if dnsSrv != nil {
			all := fetcher.Relays()
			domains := make([]string, len(all))
			for i, r := range all {
				domains[i] = r.ID
			}
			dnsSrv.SetDomains(domains)
		}
	})

	// 6. Discovery manager.
	prober := discovery.NewProber(cfg.Discovery.ProbeTimeout, cfg.Discovery.ProbeConcurrent, bypassTransport)
	mgr = discovery.NewManager(cfg.Discovery.ProbeTTL, prober, fetcher.Relays, cbReg.IsHealthy)

	// 7. HTTP handler.
	handler := proxy.NewHandler(fetcher, cbReg, mgr, bal)
	mux := http.NewServeMux()
	mux.Handle("/_relay/health", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	mux.Handle("/", handler)

	srv := &http.Server{
		Addr:    cfg.Server.Addr,
		Handler: mux,
	}

	// 8. Start registry fetch loop.
	go fetcher.Run(ctx)

	// 9. Start HTTP server.
	go func() {
		slog.Info("relay-proxy starting", "addr", cfg.Server.Addr)
		var srvErr error
		if cfg.Server.TLSCert != "" && cfg.Server.TLSKey != "" {
			srvErr = srv.ListenAndServeTLS(cfg.Server.TLSCert, cfg.Server.TLSKey)
		} else {
			srvErr = srv.ListenAndServe()
		}
		if srvErr != nil && srvErr != http.ErrServerClosed {
			slog.Error("server error", "err", srvErr)
			cancel()
		}
	}()

	<-ctx.Done()
	slog.Info("shutting down")
	_ = srv.Shutdown(context.Background())
}
