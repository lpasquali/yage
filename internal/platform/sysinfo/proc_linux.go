// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

//go:build linux

package sysinfo

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

// Stats is a point-in-time sample of the yage process + network.
type Stats struct {
	CPUPercent  float64       // process CPU %age (0-100 per logical core)
	MemRSSBytes uint64        // process resident-set size in bytes
	NetRxDelta  uint64        // total received bytes since previous Sample()
	NetTxDelta  uint64        // total transmitted bytes since previous Sample()
	DeltaDur    time.Duration // elapsed time between the last two samples
}

// Sampler accumulates the previous tick's counters so deltas can be computed.
type Sampler struct {
	lastProcTicks  uint64
	lastTotalTicks uint64
	lastRxBytes    uint64
	lastTxBytes    uint64
	lastTime       time.Time
}

// NewSampler returns a ready-to-use Sampler.
func NewSampler() *Sampler { return &Sampler{} }

// Sample reads the current /proc values and returns deltas vs the previous call.
// The very first call always returns zero deltas (no prior reference point).
func (s *Sampler) Sample() Stats {
	now := time.Now()
	procTicks := readProcCPUTicks()
	totalTicks := readTotalCPUTicks()
	rss := readProcRSS()
	rx, tx := readNetBytes()

	var cpu float64
	var rxDelta, txDelta uint64
	dur := now.Sub(s.lastTime)

	if !s.lastTime.IsZero() {
		dProc := float64(procTicks - s.lastProcTicks)
		dTotal := float64(totalTicks - s.lastTotalTicks)
		if dTotal > 0 {
			cpu = (dProc / dTotal) * 100.0
		}
		if rx >= s.lastRxBytes {
			rxDelta = rx - s.lastRxBytes
		}
		if tx >= s.lastTxBytes {
			txDelta = tx - s.lastTxBytes
		}
	}

	s.lastProcTicks = procTicks
	s.lastTotalTicks = totalTicks
	s.lastRxBytes = rx
	s.lastTxBytes = tx
	s.lastTime = now

	return Stats{
		CPUPercent:  cpu,
		MemRSSBytes: rss,
		NetRxDelta:  rxDelta,
		NetTxDelta:  txDelta,
		DeltaDur:    dur,
	}
}

// readProcCPUTicks returns utime+stime for this process from /proc/self/stat.
func readProcCPUTicks() uint64 {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	// Format: pid (comm) state ppid ... utime stime ... (fields 14 and 15, 1-indexed)
	// comm can contain spaces and parentheses; skip past the closing ')'.
	s := string(raw)
	close := strings.LastIndex(s, ")")
	if close < 0 {
		return 0
	}
	fields := strings.Fields(s[close+1:])
	// After ')': state(0) ppid(1) pgrp(2) session(3) tty(4) tpgid(5)
	// flags(6) minflt(7) cminflt(8) majflt(9) cmajflt(10)
	// utime(11) stime(12)
	if len(fields) < 13 {
		return 0
	}
	u, _ := strconv.ParseUint(fields[11], 10, 64)
	sv, _ := strconv.ParseUint(fields[12], 10, 64)
	return u + sv
}

// readTotalCPUTicks returns the sum of all CPU-time fields from /proc/stat line 0.
func readTotalCPUTicks() uint64 {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		var total uint64
		for _, tok := range strings.Fields(line)[1:] {
			v, _ := strconv.ParseUint(tok, 10, 64)
			total += v
		}
		return total
	}
	return 0
}

// readProcRSS returns VmRSS (bytes) from /proc/self/status.
func readProcRSS() uint64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, _ := strconv.ParseUint(fields[1], 10, 64)
		return kb * 1024
	}
	return 0
}

// readNetBytes sums rx_bytes and tx_bytes across all non-loopback interfaces
// from /proc/net/dev. On a non-namespaced process this equals /proc/self/net/dev.
func readNetBytes() (rx, tx uint64) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	// Skip 2 header lines.
	for i := 0; i < 2; i++ {
		sc.Scan()
	}
	for sc.Scan() {
		line := sc.Text()
		// "  eth0:  12345 ... 67890 ..."  — colon separates iface from stats.
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:colon])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(fields[0], 10, 64)
		t, _ := strconv.ParseUint(fields[8], 10, 64)
		rx += r
		tx += t
	}
	return
}
