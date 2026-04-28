// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package v1alpha1 contains API types for the yage.lpasquali.dev API group.
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	GroupVersion = schema.GroupVersion{Group: "yage.lpasquali.dev", Version: "v1alpha1"}

	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	AddToScheme = SchemeBuilder.AddToScheme
)
