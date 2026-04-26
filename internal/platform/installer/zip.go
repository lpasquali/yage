// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package installer

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
)

// extractZipMemberImpl is the zip-backed implementation referenced from
// extractZipMember. Split out to keep installer.go focused on the bash port.
func extractZipMemberImpl(zipPath, name, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		defer src.Close()
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, src); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}
	return fmt.Errorf("member %q not found in %s", name, zipPath)
}