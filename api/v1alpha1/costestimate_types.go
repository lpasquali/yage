// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	SchemeBuilder.Register(&CostEstimate{}, &CostEstimateList{})
}

// CostEstimateSpec defines the desired state of CostEstimate.
type CostEstimateSpec struct {
	// ClusterRef names the CAPI Cluster this estimate is for.
	ClusterRef corev1.LocalObjectReference `json:"clusterRef"`

	// ProviderName is the infrastructure provider (e.g. proxmox, aws, gcp).
	ProviderName string `json:"providerName"`

	// PollIntervalSeconds controls how often the controller refreshes the estimate.
	// +kubebuilder:default=86400
	PollIntervalSeconds int32 `json:"pollIntervalSeconds,omitempty"`
}

// ProviderCost holds the cost breakdown for a single provider in a
// multi-provider setup.
type ProviderCost struct {
	// ProviderName identifies the infrastructure provider.
	ProviderName string `json:"providerName"`

	// MonthlyUSD is the estimated monthly cost in USD for this provider.
	MonthlyUSD float64 `json:"monthlyUSD"`

	// Note carries any human-readable caveat from the pricing API
	// (e.g. "spot pricing used", "estimate only").
	Note string `json:"note,omitempty"`
}

// CostEstimateStatus defines the observed state of CostEstimate.
type CostEstimateStatus struct {
	// MonthlyUSD is the total estimated monthly cost in USD across all providers.
	MonthlyUSD float64 `json:"monthlyUSD,omitempty"`

	// ByProvider breaks the cost down per provider when multiple providers are in use.
	ByProvider []ProviderCost `json:"byProvider,omitempty"`

	// LastUpdated is the time the estimate was last successfully refreshed.
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`

	// Unavailable is true when the pricing API was unreachable during the last poll.
	Unavailable bool `json:"unavailable,omitempty"`

	// UnavailableReason explains why Unavailable is true.
	UnavailableReason string `json:"unavailableReason,omitempty"`

	// Conditions follows the standard Kubernetes condition pattern.
	// The Ready condition is set by the controller after each reconcile.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="PROVIDER",type=string,JSONPath=".spec.providerName"
// +kubebuilder:printcolumn:name="MONTHLY-USD",type=number,JSONPath=".status.monthlyUSD"
// +kubebuilder:printcolumn:name="LAST-UPDATED",type=date,JSONPath=".status.lastUpdated"
// +kubebuilder:printcolumn:name="AGE",type=date,JSONPath=".metadata.creationTimestamp"

// CostEstimate exposes pricing-API cost data for a CAPI cluster as a
// cluster-scoped Kubernetes custom resource.
type CostEstimate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CostEstimateSpec   `json:"spec,omitempty"`
	Status CostEstimateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CostEstimateList contains a list of CostEstimate.
type CostEstimateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []CostEstimate `json:"items"`
}
