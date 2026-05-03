// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pricing

import (
	"context"
	"errors"
	"testing"
)

func TestFetcherFrom_DefaultsToLiveFetcherWhenAbsent(t *testing.T) {
	got := FetcherFrom(context.Background())
	if got == nil {
		t.Fatal("FetcherFrom returned nil for empty context")
	}
	if _, ok := got.(liveFetcher); !ok {
		t.Fatalf("FetcherFrom on empty context returned %T, want liveFetcher", got)
	}
}

func TestFetcherFrom_NilContextReturnsDefault(t *testing.T) {
	//nolint:staticcheck // intentionally passing nil to exercise the guard.
	got := FetcherFrom(nil)
	if _, ok := got.(liveFetcher); !ok {
		t.Fatalf("FetcherFrom(nil) returned %T, want liveFetcher", got)
	}
}

func TestWithFetcher_RoundTrip(t *testing.T) {
	stub := StaticFetcher{"aws/us-east-1/t3.medium": 0.0416}
	ctx := WithFetcher(context.Background(), stub)
	got := FetcherFrom(ctx)
	if got == nil {
		t.Fatal("FetcherFrom returned nil after WithFetcher")
	}
	// Verify it routes to the stub by checking USDPerHour.
	rate, err := got.USDPerHour(ctx, "aws", "us-east-1", "t3.medium")
	if err != nil {
		t.Fatalf("USDPerHour: %v", err)
	}
	if rate != 0.0416 {
		t.Fatalf("USDPerHour = %v, want 0.0416", rate)
	}
}

func TestWithFetcher_NilFetcherIsNoOp(t *testing.T) {
	parent := context.Background()
	got := WithFetcher(parent, nil)
	if got != parent {
		t.Fatal("WithFetcher(ctx, nil) should return ctx unchanged")
	}
}

func TestStaticFetcher_FetchSynthesizesUSDPerMonth(t *testing.T) {
	s := StaticFetcher{"linode/us-east/g6-standard-2": 0.03}
	it, err := s.Fetch(context.Background(), "linode", "g6-standard-2", "us-east")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if it.USDPerHour != 0.03 {
		t.Errorf("USDPerHour = %v, want 0.03", it.USDPerHour)
	}
	wantMonth := 0.03 * MonthlyHours
	if it.USDPerMonth != wantMonth {
		t.Errorf("USDPerMonth = %v, want %v", it.USDPerMonth, wantMonth)
	}
	if it.Vendor != "linode" || it.SKU != "g6-standard-2" || it.Region != "us-east" {
		t.Errorf("metadata mismatch: %+v", it)
	}
}

func TestStaticFetcher_MissingEntryReturnsErrUnavailable(t *testing.T) {
	s := StaticFetcher{}
	_, err := s.Fetch(context.Background(), "aws", "ghost", "nowhere")
	if err == nil {
		t.Fatal("Fetch on missing key returned nil error")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want wrap of ErrUnavailable", err)
	}
	_, err = s.USDPerHour(context.Background(), "aws", "nowhere", "ghost")
	if err == nil {
		t.Fatal("USDPerHour on missing key returned nil error")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want wrap of ErrUnavailable", err)
	}
}

// TestParallelFetcherIsolation verifies that two contexts carrying
// different StaticFetchers do not see each other's data — the
// parallel-test safety property called out in issue #197.
func TestParallelFetcherIsolation(t *testing.T) {
	t.Parallel()
	a := StaticFetcher{"aws/us-east-1/t3.medium": 0.05}
	b := StaticFetcher{"aws/us-east-1/t3.medium": 0.99}

	ctxA := WithFetcher(context.Background(), a)
	ctxB := WithFetcher(context.Background(), b)

	rateA, err := FetcherFrom(ctxA).USDPerHour(ctxA, "aws", "us-east-1", "t3.medium")
	if err != nil {
		t.Fatalf("ctxA: %v", err)
	}
	rateB, err := FetcherFrom(ctxB).USDPerHour(ctxB, "aws", "us-east-1", "t3.medium")
	if err != nil {
		t.Fatalf("ctxB: %v", err)
	}
	if rateA != 0.05 {
		t.Errorf("ctxA rate = %v, want 0.05", rateA)
	}
	if rateB != 0.99 {
		t.Errorf("ctxB rate = %v, want 0.99", rateB)
	}
}

// TestDefaultFetcher_IsSingleton verifies the sync.Once guard
// returns the same instance on repeated calls (zero-allocation
// guarantee for the no-override hot path).
func TestDefaultFetcher_IsSingleton(t *testing.T) {
	a := DefaultFetcher()
	b := DefaultFetcher()
	if a != b {
		t.Fatalf("DefaultFetcher returned different instances: %p vs %p", a, b)
	}
}
