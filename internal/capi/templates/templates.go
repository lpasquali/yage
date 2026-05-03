// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package templates defines the six wrapper structs that
// internal/platform/manifests.Fetcher passes to yage-manifests
// templates (ADR 0012 §3).
//
// Templates do not receive *config.Config directly. Each template group
// receives a purpose-built wrapper carrying the entire workload Config
// alongside the per-call-site fields the renderer needs (release name,
// sync wave, pre-rendered values blob, etc.). Field shapes are pinned
// here so #137–#141 migrations land mechanically against a fixed
// contract; reviewers reject ad-hoc per-renderer structs.
//
// Drift between this package and the templates is caught at render time
// by Fetcher's Option("missingkey=error") policy (ADR 0012 §4): any
// template that references a field absent from these structs fails the
// orchestrator phase loudly rather than rendering "<no value>" into an
// apiserver-acceptable manifest.
package templates

import "github.com/lpasquali/yage/internal/config"

// HelmValuesData is passed to addons/<addon>/values.yaml.tmpl,
// csi/<driver>/values.yaml.tmpl, and addons/<addon>/argocd-cr.yaml.tmpl.
type HelmValuesData struct {
	Cfg *config.Config
}

// ArgoApplicationData is passed to addons/<addon>/application.yaml.tmpl.
//
// PostSyncs is a slice rather than a single block: HelmOCI's proxmox-csi
// smoke variant attaches two PostSync hooks together, single-hook flows
// use one entry, and the no-hook case is nil/empty.
type ArgoApplicationData struct {
	Cfg         *config.Config
	Name        string
	DestNS      string
	RepoURL     string
	Chart       string
	Path        string
	Ref         string
	SyncWave    string
	ReleaseName string
	ValuesYAML  string
	Annotations map[string]string
	PostSyncs   []PostSyncBlock
}

// PostSyncBlock is the second-source git+kustomize fields for an Argo
// Application. Zero value means "single-source Application; no hook".
type PostSyncBlock struct {
	URL              string
	Path             string
	Ref              string
	KustomizePartial string
}

// HelmChartProxyData is passed to addons/<addon>/helmchartproxy.yaml.tmpl.
type HelmChartProxyData struct {
	Cfg             *config.Config
	Name            string
	Namespace       string
	ClusterSelector map[string]string
	ChartName       string
	RepoURL         string
	Version         string
	ChartNamespace  string
	ValuesTemplate  string
}

// PostSyncData is passed to postsync/<hookname>.yaml.tmpl.
type PostSyncData struct {
	Cfg           *config.Config
	Namespace     string
	StorageClass  string
	KubectlImage  string
	K8sVersionTag string
}

// KustomizePartialData is passed to postsync/_partials/<name>.kustomize.tmpl.
type KustomizePartialData struct {
	Cfg          *config.Config
	Namespace    string
	JobName      string
	KubectlImage string
	Extra        map[string]string
}
