// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package cost implements the operator's cost-monitoring runner.
// It is intentionally CRD-free for the Phase F spike: it emits
// Prometheus metrics only. CRD status will be added in Phase F step 3.
package cost

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"

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
// Prometheus gauges. No CRDs are required; the config is supplied at
// construction time from env vars.
//
// TODO(phase-f-step3): replace Cfg with a client.Client that reads the
// bootstrap-config Secret directly, so the operator discovers the
// provider and cluster shape without needing env vars.
type Runner struct {
	Cfg      *config.Config
	Interval time.Duration
	Log      logr.Logger
}

// Start blocks until ctx is cancelled, running one poll immediately
// and then once per Interval.
func (r *Runner) Start(ctx context.Context) error {
	r.runOnce()
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.runOnce()
		}
	}
}

func (r *Runner) runOnce() {
	r.Log.Info("polling pricing APIs", "interval", r.Interval)
	results := cost.CompareWithFilter(r.Cfg, cost.ScopeAll, nil)
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
