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
	"flag"
	"os"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

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

	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	log := ctrl.Log.WithName("yage-operator")

	ctrlmetrics.Registry.MustRegister(opcost.Metrics()...)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Metrics:                server.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         false,
	})
	if err != nil {
		log.Error(err, "unable to create manager")
		os.Exit(1)
	}

	runner := &opcost.Runner{
		Cfg:      cfgFromEnv(),
		Interval: pollInterval,
		Log:      log.WithName("cost-runner"),
	}
	if err := mgr.Add(runner); err != nil {
		log.Error(err, "unable to register cost runner")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		log.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	log.Info("starting yage-operator", "metricsAddr", metricsAddr, "pollInterval", pollInterval)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited with error")
		os.Exit(1)
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
