// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Hand-written DeepCopy implementations.  Replace this file with the
// controller-gen output once `make crds` is run in an environment where
// controller-gen is available (see Makefile).

package v1alpha1

import (
	runtime "k8s.io/apimachinery/pkg/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// -----------------------------------------------------------------------
// ProviderCost
// -----------------------------------------------------------------------

func (in *ProviderCost) DeepCopyInto(out *ProviderCost) {
	*out = *in
}

func (in *ProviderCost) DeepCopy() *ProviderCost {
	if in == nil {
		return nil
	}
	out := new(ProviderCost)
	in.DeepCopyInto(out)
	return out
}

// -----------------------------------------------------------------------
// CostEstimateSpec
// -----------------------------------------------------------------------

func (in *CostEstimateSpec) DeepCopyInto(out *CostEstimateSpec) {
	*out = *in
	// LocalObjectReference is a plain struct with only a string field.
	out.ClusterRef = in.ClusterRef
}

func (in *CostEstimateSpec) DeepCopy() *CostEstimateSpec {
	if in == nil {
		return nil
	}
	out := new(CostEstimateSpec)
	in.DeepCopyInto(out)
	return out
}

// -----------------------------------------------------------------------
// CostEstimateStatus
// -----------------------------------------------------------------------

func (in *CostEstimateStatus) DeepCopyInto(out *CostEstimateStatus) {
	*out = *in

	in.LastUpdated.DeepCopyInto(&out.LastUpdated)

	if in.ByProvider != nil {
		in, out := &in.ByProvider, &out.ByProvider
		*out = make([]ProviderCost, len(*in))
		copy(*out, *in)
	}

	if in.Conditions != nil {
		in, out := &in.Conditions, &out.Conditions
		*out = make([]metav1.Condition, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *CostEstimateStatus) DeepCopy() *CostEstimateStatus {
	if in == nil {
		return nil
	}
	out := new(CostEstimateStatus)
	in.DeepCopyInto(out)
	return out
}

// -----------------------------------------------------------------------
// CostEstimate
// -----------------------------------------------------------------------

func (in *CostEstimate) DeepCopyInto(out *CostEstimate) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	in.Spec.DeepCopyInto(&out.Spec)
	in.Status.DeepCopyInto(&out.Status)
}

func (in *CostEstimate) DeepCopy() *CostEstimate {
	if in == nil {
		return nil
	}
	out := new(CostEstimate)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *CostEstimate) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

// -----------------------------------------------------------------------
// CostEstimateList
// -----------------------------------------------------------------------

func (in *CostEstimateList) DeepCopyInto(out *CostEstimateList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		in, out := &in.Items, &out.Items
		*out = make([]CostEstimate, len(*in))
		for i := range *in {
			(*in)[i].DeepCopyInto(&(*out)[i])
		}
	}
}

func (in *CostEstimateList) DeepCopy() *CostEstimateList {
	if in == nil {
		return nil
	}
	out := new(CostEstimateList)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyObject implements runtime.Object.
func (in *CostEstimateList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}
