// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// yage-operator is the optional in-cluster day-2 companion to the yage
// CLI (Phase F). This binary runs as a single Deployment in the
// management cluster post-pivot and currently implements one component:
//
//   - CostRunner: polls pricing APIs on a fixed interval and emits
//     yage_cluster_monthly_usd + yage_pricing_fetch_errors_total metrics.
//
// Upcoming (Phase F steps 3-7): CostEstimate CRD, CapacityWebhook,
// DriftController, Helm chart, --install-operator CLI flag.
//
// Note: controller-runtime will be added when a version compatible with
// k8s.io/client-go v0.36 is released. The spike uses stdlib directly.
//
// Config (env vars for the spike — to be replaced by Secret-backed
// config in step 3):
//
//	YAGE_INFRA_PROVIDER   provider name (e.g. proxmox, aws). When
//	                      empty all registered providers are priced.
//	YAGE_SKIP_PROVIDERS   comma-separated list of providers to exclude.
//	YAGE_AIRGAPPED        set to "1" to restrict to airgap-compatible
//	                      providers only.
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"log"

	"github.com/lpasquali/yage/internal/config"
	opcost "github.com/lpasquali/yage/internal/operator/cost"

	// Provider self-registration — every provider that calls
	// provider.Register in its init() becomes available for pricing.
	_ "github.com/lpasquali/yage/internal/provider/aws"
	_ "github.com/lpasquali/yage/internal/provider/azure"
	_ "github.com/lpasquali/yage/internal/provider/capd"
	_ "github.com/lpasquali/yage/internal/provider/digitalocean"
	_ "github.com/lpasquali/yage/internal/provider/gcp"
	_ "github.com/lpasquali/yage/internal/provider/hetzner"
	_ "github.com/lpasquali/yage/internal/provider/ibmcloud"
	_ "github.com/lpasquali/yage/internal/provider/linode"
	_ "github.com/lpasquali/yage/internal/provider/oci"
	_ "github.com/lpasquali/yage/internal/provider/openstack"
	_ "github.com/lpasquali/yage/internal/provider/proxmox"
	_ "github.com/lpasquali/yage/internal/provider/vsphere"
)

func main() {
	var (
		metricsAddr  string
		probeAddr    string
		pollInterval time.Duration
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080",
		"Address to expose Prometheus metrics on.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"Address to expose /healthz and /readyz probes on.")
	flag.DurationVar(&pollInterval, "cost-poll-interval", 24*time.Hour,
		"How often to poll pricing APIs (e.g. 24h, 6h).")
	flag.Parse()

	logger := stdr.New(log.New(os.Stdout, "", log.LstdFlags))
	logger = logger.WithName("yage-operator")

	reg := prometheus.NewRegistry()
	reg.MustRegister(opcost.Metrics()...)
	reg.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	go runMetricsServer(ctx, metricsAddr, reg, logger)
	go runProbeServer(ctx, probeAddr, logger)

	cfg := cfgFromEnv()
	runner := &opcost.Runner{
		Cfg:      cfg,
		Interval: pollInterval,
		Log:      logger.WithName("cost-runner"),
	}
	runner.Start(ctx) //nolint:errcheck
	logger.Info("shutdown complete")
}

func runMetricsServer(ctx context.Context, addr string, reg *prometheus.Registry, log logr.Logger) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	srv := &http.Server{Addr: addr, Handler: mux}
	log.Info("metrics server listening", "addr", addr)
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error(err, "metrics server error")
	}
}

func runProbeServer(ctx context.Context, addr string, log logr.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	srv := &http.Server{Addr: addr, Handler: mux}
	log.Info("probe server listening", "addr", addr)
	go func() {
		<-ctx.Done()
		srv.Close()
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error(err, "probe server error")
	}
}

// cfgFromEnv builds a minimal *config.Config from the env vars
// documented in the package comment. Full Secret-backed config
// comes in Phase F step 3.
func cfgFromEnv() *config.Config {
	cfg := &config.Config{}
	cfg.InfraProvider = os.Getenv("YAGE_INFRA_PROVIDER")
	cfg.InfraProviderDefaulted = cfg.InfraProvider == ""
	cfg.SkipProviders = os.Getenv("YAGE_SKIP_PROVIDERS")
	cfg.Airgapped = os.Getenv("YAGE_AIRGAPPED") == "1"
	return cfg
}
