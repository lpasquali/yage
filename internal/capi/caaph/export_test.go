// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package caaph

import (
	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/manifests"
)

// BuildArgoCDCRForTest exposes the private buildArgoCDCR function for golden
// tests in the external caaph_test package.
func BuildArgoCDCRForTest(cfg *config.Config, f *manifests.Fetcher) (string, error) {
	return buildArgoCDCR(cfg, f)
}
