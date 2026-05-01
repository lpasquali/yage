// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package pivot

// Tests for the three VerifyParity helpers added per ADR 0011 §6:
//
//   - checkYageSystemNamespace          (§6.a)
//   - checkLabeledYageSystemSecrets     (§6.b — zero is a warning, not fatal)
//   - checkYageReposPVCBound            (§6.c)
//
// Each helper takes a kubernetes.Interface so a fake clientset is enough
// to drive the present/absent and bound/pending paths.

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func newFake(objs ...interface{}) kubernetes.Interface {
	cs := k8sfake.NewClientset()
	ctx := context.Background()
	for _, o := range objs {
		switch v := o.(type) {
		case *corev1.Namespace:
			_, _ = cs.CoreV1().Namespaces().Create(ctx, v, metav1.CreateOptions{})
		case *corev1.Secret:
			_, _ = cs.CoreV1().Secrets(v.Namespace).Create(ctx, v, metav1.CreateOptions{})
		case *corev1.PersistentVolumeClaim:
			_, _ = cs.CoreV1().PersistentVolumeClaims(v.Namespace).Create(ctx, v, metav1.CreateOptions{})
		}
	}
	return cs
}

func mkNS(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func mkLabeledSecret(ns, name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "yage"},
		},
		Type: corev1.SecretTypeOpaque,
	}
}

func mkPVC(ns, name string, phase corev1.PersistentVolumeClaimPhase) *corev1.PersistentVolumeClaim {
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status:     corev1.PersistentVolumeClaimStatus{Phase: phase},
	}
}

// --- checkYageSystemNamespace ------------------------------------------------

func TestCheckYageSystemNamespace_Present(t *testing.T) {
	cs := newFake(mkNS(yageSystemNamespace))
	if err := checkYageSystemNamespace(context.Background(), cs); err != nil {
		t.Errorf("present case returned err: %v", err)
	}
}

func TestCheckYageSystemNamespace_Missing(t *testing.T) {
	cs := newFake()
	err := checkYageSystemNamespace(context.Background(), cs)
	if err == nil {
		t.Fatalf("missing case returned nil err")
	}
	if !strings.Contains(err.Error(), yageSystemNamespace) {
		t.Errorf("error %q does not mention namespace name", err)
	}
}

// --- checkLabeledYageSystemSecrets ------------------------------------------

func TestCheckLabeledYageSystemSecrets_OneOrMore(t *testing.T) {
	cs := newFake(
		mkNS(yageSystemNamespace),
		mkLabeledSecret(yageSystemNamespace, "tfstate-default-proxmox"),
	)
	if err := checkLabeledYageSystemSecrets(context.Background(), cs); err != nil {
		t.Errorf("one-or-more case returned err: %v", err)
	}
}

func TestCheckLabeledYageSystemSecrets_ZeroIsWarning(t *testing.T) {
	// ADR 0011 §6.b: zero labeled Secrets is a warning, not a fatal
	// error — first-run with no tofu state yet. The helper must return
	// nil so the caller's poll loop converges.
	cs := newFake(mkNS(yageSystemNamespace))
	if err := checkLabeledYageSystemSecrets(context.Background(), cs); err != nil {
		t.Errorf("zero-secrets case returned err %v, want nil (ADR 0011 §6.b warn-don't-fail)", err)
	}
}

func TestCheckLabeledYageSystemSecrets_IgnoresUnlabeled(t *testing.T) {
	// Unlabeled Secrets must not satisfy the §6.b check on their own
	// — the label is what makes a Secret part of the yage-managed set.
	// (Steady-state still warns since we want zero matches → nil.)
	cs := newFake(mkNS(yageSystemNamespace),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: yageSystemNamespace},
			Type:       corev1.SecretTypeOpaque,
		},
	)
	if err := checkLabeledYageSystemSecrets(context.Background(), cs); err != nil {
		t.Errorf("unlabeled-only case returned err %v, want nil (warn path)", err)
	}
}

// --- checkYageReposPVCBound -------------------------------------------------

func TestCheckYageReposPVCBound_Bound(t *testing.T) {
	cs := newFake(
		mkNS(yageSystemNamespace),
		mkPVC(yageSystemNamespace, yageReposPVCName, corev1.ClaimBound),
	)
	if err := checkYageReposPVCBound(context.Background(), cs); err != nil {
		t.Errorf("Bound case returned err: %v", err)
	}
}

func TestCheckYageReposPVCBound_Pending(t *testing.T) {
	cs := newFake(
		mkNS(yageSystemNamespace),
		mkPVC(yageSystemNamespace, yageReposPVCName, corev1.ClaimPending),
	)
	err := checkYageReposPVCBound(context.Background(), cs)
	if err == nil {
		t.Fatalf("Pending case returned nil err")
	}
	if !strings.Contains(err.Error(), "Pending") {
		t.Errorf("error %q does not mention the actual phase", err)
	}
}

func TestCheckYageReposPVCBound_Missing(t *testing.T) {
	cs := newFake(mkNS(yageSystemNamespace))
	err := checkYageReposPVCBound(context.Background(), cs)
	if err == nil {
		t.Fatalf("missing case returned nil err")
	}
	if !strings.Contains(err.Error(), yageReposPVCName) {
		t.Errorf("error %q does not name the missing PVC", err)
	}
}
