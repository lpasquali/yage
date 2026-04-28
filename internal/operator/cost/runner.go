// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package cost implements the operator's cost-monitoring runner.
// It is intentionally CRD-free for the Phase F spike: it emits
// Prometheus metrics only. CRD status will be added in Phase F step 3b.
package cost

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lpasquali/yage/internal/cluster/kindsync"
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/cost"
)

// Metrics returns the Prometheus collectors the operator exposes.
// Call once and pass to prometheus.Registry.MustRegister.
func Metrics() []prometheus.Collector {
	return []prometheus.Collector{monthlyUSD, fetchErrors}
}

var monthlyUSD = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: "yage",
	Name:      "cluster_monthly_usd",
	Help:      "Estimated monthly cost in USD for the configured cluster shape, by provider.",
}, []string{"provider"})

var fetchErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "yage",
	Name:      "pricing_fetch_errors_total",
	Help:      "Number of pricing API fetch errors, by provider.",
}, []string{"provider"})

// Runner polls pricing APIs on a fixed interval and updates the
// Prometheus gauges. No CRDs are required; the config is sourced
// either from a bootstrap-config Secret (when Client + SecretRef are
// set) or from the static Cfg field (for tests / env-var fallback).
type Runner struct {
	// Client reads the bootstrap-config Secret each poll cycle.
	// When nil, Cfg is used directly.
	Client    client.Client
	SecretRef types.NamespacedName

	Cfg      *config.Config
	Interval time.Duration
	Log      logr.Logger
}

// Start blocks until ctx is cancelled, running one poll immediately
// and then once per Interval.
func (r *Runner) Start(ctx context.Context) error {
	r.runOnce(ctx)
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.runOnce(ctx)
		}
	}
}

func (r *Runner) runOnce(ctx context.Context) {
	cfg := r.resolveConfig(ctx)
	r.Log.Info("polling pricing APIs", "interval", r.Interval)
	results := cost.CompareWithFilter(cfg, cost.ScopeAll, nil)
	ok := 0
	for _, row := range results {
		if row.Err != nil {
			r.Log.V(1).Info("pricing unavailable", "provider", row.ProviderName, "err", row.Err)
			fetchErrors.WithLabelValues(row.ProviderName).Inc()
			continue
		}
		monthlyUSD.WithLabelValues(row.ProviderName).Set(row.Estimate.TotalUSDMonthly)
		ok++
	}
	r.Log.Info("cost poll complete", "priced", ok, "total", len(results))
}

// resolveConfig returns the *config.Config to use for this poll.
// When a controller-runtime client and secret ref are configured it
// reads the bootstrap-config Secret; otherwise it falls back to r.Cfg.
func (r *Runner) resolveConfig(ctx context.Context) *config.Config {
	if r.Client == nil || r.SecretRef.Name == "" {
		return r.Cfg
	}
	var secret corev1.Secret
	if err := r.Client.Get(ctx, r.SecretRef, &secret); err != nil {
		r.Log.Error(err, "failed to read bootstrap-config Secret, falling back to static config",
			"secret", r.SecretRef)
		return r.Cfg
	}
	return kindsync.LoadConfigFromSecretData(secret.Data)
}
