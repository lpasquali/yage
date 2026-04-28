// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

//go:build !linux

package sysinfo

import "time"

// Stats is a point-in-time sample of the yage process + network.
type Stats struct {
	CPUPercent  float64
	MemRSSBytes uint64
	NetRxDelta  uint64
	NetTxDelta  uint64
	DeltaDur    time.Duration
}

// Sampler is a no-op stub on non-Linux platforms.
type Sampler struct{}

func NewSampler() *Sampler { return &Sampler{} }

func (s *Sampler) Sample() Stats { return Stats{} }
