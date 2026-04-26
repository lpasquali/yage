// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package pivot implements the standard CAPI "bootstrap-and-pivot" pattern:
// kind boots, clusterctl init runs on kind, then this package provisions a
// management cluster on Proxmox via CAPI, runs `clusterctl move` from the
// kind context to the new management cluster, and finally tears the kind
// cluster down once parity is verified.
//
// File layout:
//   - pivot.go     — main entry points (EnsureManagementCluster, VerifyParity, TeardownKind)
//   - manifest.go  — render the management-cluster CAPI manifest
//   - move.go      — clusterctl init + clusterctl move wrappers
//   - wait.go      — wait helpers (CAPI Cluster Available, Deployment Available, etc.)
//
// This file holds the wait helpers. The CAPI-Cluster-Available check is a
// straight copy of internal/orchestrator.waitClusterAvailable; copying instead
// of importing avoids an internal/bootstrap → internal/pivot cycle (the
// orchestrator drives pivot from orchestrator.Run).
package pivot

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/lpasquali/yage/internal/platform/k8sclient"
)

// capiClusterGVR is the v1beta1 CAPI Cluster resource.
var capiClusterGVR = schema.GroupVersionResource{
	Group:    "cluster.x-k8s.io",
	Version:  "v1beta2",
	Resource: "clusters",
}

// waitClusterAvailable polls a CAPI Cluster's Available condition (=True).
// Mirrors internal/orchestrator.waitClusterAvailable verbatim — copied to keep
// internal/pivot decoupled from internal/orchestrator.
func waitClusterAvailable(cli *k8sclient.Client, bg context.Context, ns, name string, timeout time.Duration) error {
	return k8sclient.PollUntil(bg, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		u, err := cli.Dynamic.Resource(capiClusterGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		conds, _, _ := unstructuredSlice(u.Object, "status", "conditions")
		for _, c := range conds {
			cm, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			t, _ := cm["type"].(string)
			s, _ := cm["status"].(string)
			if t == "Available" && s == "True" {
				return true, nil
			}
		}
		return false, nil
	})
}

// waitDeploymentReady polls a Deployment's Available condition (=True).
// Same logic as internal/orchestrator.waitDeploymentReady; copied to avoid the
// import cycle.
func waitDeploymentReady(cli *k8sclient.Client, ns, name string, timeout time.Duration) error {
	bg := context.Background()
	return k8sclient.PollUntil(bg, 3*time.Second, timeout, func(ctx context.Context) (bool, error) {
		d, err := cli.Typed.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		for _, c := range d.Status.Conditions {
			if string(c.Type) == "Available" && c.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}

// waitNamespacePresent polls until the named Namespace exists or the timeout
// elapses. Used as a coarse readiness check during VerifyParity.
func waitNamespacePresent(cli *k8sclient.Client, name string, timeout time.Duration) error {
	bg := context.Background()
	return k8sclient.PollUntil(bg, 3*time.Second, timeout, func(ctx context.Context) (bool, error) {
		_, err := cli.Typed.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

// waitSecretPresent polls until the named Secret exists or the timeout
// elapses.
func waitSecretPresent(cli *k8sclient.Client, ns, name string, timeout time.Duration) error {
	bg := context.Background()
	return k8sclient.PollUntil(bg, 3*time.Second, timeout, func(ctx context.Context) (bool, error) {
		_, err := cli.Typed.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		return true, nil
	})
}

// unstructuredSlice fetches a []interface{} at path; returns nil on miss.
// Copied from internal/bootstrap/purge.go for the same import-cycle reason.
func unstructuredSlice(obj map[string]interface{}, path ...string) ([]interface{}, bool, error) {
	cur := obj
	for i, p := range path {
		v, ok := cur[p]
		if !ok || v == nil {
			return nil, false, nil
		}
		if i == len(path)-1 {
			s, ok := v.([]interface{})
			return s, ok, nil
		}
		next, ok := v.(map[string]interface{})
		if !ok {
			return nil, false, nil
		}
		cur = next
	}
	return nil, false, nil
}

// parseDuration parses a Go-style duration string with a fallback.
func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}