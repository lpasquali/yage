// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// yage-operator is the optional in-cluster day-2 companion to the yage
// CLI (Phase F). This binary runs as a single Deployment in the
// management cluster post-pivot and currently implements one component:
//
//   - CostRunner: polls pricing APIs on a fixed interval and emits
//     yage_cluster_monthly_usd + yage_pricing_fetch_errors_total metrics.
//
// Upcoming (Phase F steps 3b-7): CostEstimate CRD, CapacityWebhook,
// DriftController, Helm chart, --install-operator CLI flag.
//
// Config:
//
//	--bootstrap-secret-ref   namespace/name of the bootstrap-config
//	                         Secret written by the yage CLI post-pivot
//	                         (default: yage-system/proxmox-bootstrap-config).
//	                         When the Secret is unreadable the runner
//	                         falls back to an empty config (all providers
//	                         priced at default shape).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

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
		metricsAddr    string
		probeAddr      string
		pollInterval   time.Duration
		secretRef      string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080",
		"Address to expose Prometheus metrics on.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"Address to expose /healthz and /readyz probes on.")
	flag.DurationVar(&pollInterval, "cost-poll-interval", 24*time.Hour,
		"How often to poll pricing APIs (e.g. 24h, 6h).")
	flag.StringVar(&secretRef, "bootstrap-secret-ref", "yage-system/proxmox-bootstrap-config",
		"namespace/name of the bootstrap-config Secret written by the yage CLI.")

	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	log := ctrl.Log.WithName("yage-operator")

	secretNN, err := parseNamespacedName(secretRef)
	if err != nil {
		log.Error(err, "invalid --bootstrap-secret-ref", "value", secretRef)
		os.Exit(1)
	}

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
		Client:    mgr.GetClient(),
		SecretRef: secretNN,
		Interval:  pollInterval,
		Log:       log.WithName("cost-runner"),
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

	log.Info("starting yage-operator",
		"metricsAddr", metricsAddr,
		"pollInterval", pollInterval,
		"bootstrapSecretRef", secretRef,
	)
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "manager exited with error")
		os.Exit(1)
	}
}

func parseNamespacedName(ref string) (types.NamespacedName, error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return types.NamespacedName{}, fmt.Errorf("expected namespace/name, got %q", ref)
	}
	return types.NamespacedName{Namespace: parts[0], Name: parts[1]}, nil
}
