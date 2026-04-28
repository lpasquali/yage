// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package cost_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lpasquali/yage/internal/config"
	opcost "github.com/lpasquali/yage/internal/operator/cost"

	// capd self-registers so CompareWithFilter has at least one provider.
	_ "github.com/lpasquali/yage/internal/provider/capd"
)

// TestRunnerStartsAndStops verifies the runner completes without error
// when its context is cancelled after the first poll. It does not assert
// on specific metric values because pricing APIs require live credentials.
func TestRunnerStartsAndStops(t *testing.T) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(opcost.Metrics()...)

	cfg := &config.Config{}
	r := &opcost.Runner{
		Cfg:      cfg,
		Interval: time.Hour,
		Log:      logr.Discard(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	if err := r.Start(ctx); err != nil {
		t.Fatalf("runner returned unexpected error: %v", err)
	}
}

// TestMetricsRegistered verifies that Metrics() returns collectors that
// can be registered without a duplicate-registration panic.
func TestMetricsRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	cols := opcost.Metrics()
	if len(cols) == 0 {
		t.Fatal("Metrics() returned no collectors")
	}
	for _, c := range cols {
		if err := reg.Register(c); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
}
