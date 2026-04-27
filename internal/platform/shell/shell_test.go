// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package shell

import (
	"reflect"
	"testing"
)

func TestInjectKindImage(t *testing.T) {
	t.Cleanup(func() { SetKindNodeImage("") })

	base := []string{"kind", "create", "cluster", "--name", "mgmt"}
	if got := injectKindImage(append([]string(nil), base...)); !reflect.DeepEqual(got, base) {
		t.Fatalf("no override: got %v want %v", got, base)
	}

	SetKindNodeImage("registry.local/kindest/node:v1.2.3")
	want := []string{"kind", "create", "cluster", "--name", "mgmt", "--image", "registry.local/kindest/node:v1.2.3"}
	if got := injectKindImage(append([]string(nil), base...)); !reflect.DeepEqual(got, want) {
		t.Fatalf("with override: got %v want %v", got, want)
	}

	already := []string{"kind", "create", "cluster", "--image", "pinned:tag", "--name", "mgmt"}
	if got := injectKindImage(append([]string(nil), already...)); !reflect.DeepEqual(got, already) {
		t.Fatalf("existing --image must be respected: got %v want %v", got, already)
	}

	other := []string{"kind", "delete", "cluster", "--name", "mgmt"}
	if got := injectKindImage(append([]string(nil), other...)); !reflect.DeepEqual(got, other) {
		t.Fatalf("non-create argv unchanged: got %v", got)
	}

	SetKindNodeImage("")
	short := []string{"kind", "create"}
	if got := injectKindImage(append([]string(nil), short...)); !reflect.DeepEqual(got, short) {
		t.Fatalf("argv too short: got %v", got)
	}
}
