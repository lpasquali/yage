// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

// Tests for the label-scoped second pass of HandOffBootstrapSecretsToManagement
// (ADR 0011 §2): copyYageSystemSecrets discovers every Secret in yage-system
// carrying app.kubernetes.io/managed-by=yage on the source (kind) side and
// server-side-applies it to the destination (mgmt). The named pass already
// applied some Secrets — those must NOT be re-applied (per-key skip set).

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/lpasquali/yage/internal/platform/k8sclient"
)

// makeKindsyncClient builds a Client backed by a fake clientset and
// pre-creates the yage-system namespace so EnsureNamespace short-circuits.
func makeKindsyncClient(t *testing.T, objs ...interface{}) *k8sclient.Client {
	t.Helper()
	cs := k8sfake.NewClientset()
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(),
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: YageSystemNamespace}},
		metav1.CreateOptions{}); err != nil {
		t.Fatalf("pre-create %s: %v", YageSystemNamespace, err)
	}
	for _, o := range objs {
		switch v := o.(type) {
		case *corev1.Secret:
			if _, err := cs.CoreV1().Secrets(v.Namespace).Create(context.Background(), v, metav1.CreateOptions{}); err != nil {
				t.Fatalf("seed Secret %s/%s: %v", v.Namespace, v.Name, err)
			}
		default:
			t.Fatalf("unsupported seed type %T", o)
		}
	}
	return &k8sclient.Client{Typed: cs}
}

func labeledSecret(name string, extra map[string]string) *corev1.Secret {
	labels := map[string]string{"app.kubernetes.io/managed-by": "yage"}
	for k, v := range extra {
		labels[k] = v
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: YageSystemNamespace,
			Labels:    labels,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"k": []byte("v-" + name)},
	}
}

func TestCopyYageSystemSecrets_AppliesAllLabeled(t *testing.T) {
	src := makeKindsyncClient(t,
		labeledSecret("tfstate-default-proxmox", nil),
		labeledSecret("tfstate-default-aws", nil),
	)
	// Add an unlabeled Secret that must be ignored.
	if _, err := src.Typed.CoreV1().Secrets(YageSystemNamespace).Create(context.Background(),
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: YageSystemNamespace},
			Type:       corev1.SecretTypeOpaque,
		}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed unrelated: %v", err)
	}

	dst := makeKindsyncClient(t)

	n, err := copyYageSystemSecrets(context.Background(), src, dst, YageSystemNamespace, nil, "kind-test")
	if err != nil {
		t.Fatalf("copyYageSystemSecrets: %v", err)
	}
	if n != 2 {
		t.Errorf("copied count = %d, want 2", n)
	}
	for _, name := range []string{"tfstate-default-proxmox", "tfstate-default-aws"} {
		if _, err := dst.Typed.CoreV1().Secrets(YageSystemNamespace).Get(context.Background(), name, metav1.GetOptions{}); err != nil {
			t.Errorf("destination missing %s: %v", name, err)
		}
	}
	if _, err := dst.Typed.CoreV1().Secrets(YageSystemNamespace).Get(context.Background(), "unrelated", metav1.GetOptions{}); err == nil {
		t.Errorf("unlabeled Secret leaked to destination — label selector ignored")
	}
}

func TestCopyYageSystemSecrets_RespectsSkipSet(t *testing.T) {
	src := makeKindsyncClient(t,
		labeledSecret("already-handled", nil),
		labeledSecret("new-one", nil),
	)
	dst := makeKindsyncClient(t)

	skip := map[string]bool{
		YageSystemNamespace + "/already-handled": true,
	}
	n, err := copyYageSystemSecrets(context.Background(), src, dst, YageSystemNamespace, skip, "kind-test")
	if err != nil {
		t.Fatalf("copyYageSystemSecrets: %v", err)
	}
	if n != 1 {
		t.Errorf("copied count = %d, want 1 (skip set should suppress duplicate)", n)
	}
	if _, err := dst.Typed.CoreV1().Secrets(YageSystemNamespace).Get(context.Background(), "already-handled", metav1.GetOptions{}); err == nil {
		t.Errorf("already-handled Secret was re-applied despite skip set")
	}
	if _, err := dst.Typed.CoreV1().Secrets(YageSystemNamespace).Get(context.Background(), "new-one", metav1.GetOptions{}); err != nil {
		t.Errorf("new-one Secret missing on destination: %v", err)
	}
}

func TestCopyYageSystemSecrets_EmptyListReturnsZero(t *testing.T) {
	src := makeKindsyncClient(t)
	dst := makeKindsyncClient(t)

	n, err := copyYageSystemSecrets(context.Background(), src, dst, YageSystemNamespace, nil, "kind-test")
	if err != nil {
		t.Errorf("expected nil error on empty list, got: %v", err)
	}
	if n != 0 {
		t.Errorf("copied count = %d, want 0", n)
	}
}

func TestHandoffResult_TotalSumsCounts(t *testing.T) {
	cases := []struct {
		name  string
		named int
		label int
		want  int
	}{
		{"both zero", 0, 0, 0},
		{"only named", 4, 0, 4},
		{"only label", 0, 7, 7},
		{"mixed", 3, 5, 8},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := HandoffResult{NamedCopied: tc.named, LabelCopied: tc.label}
			if got := r.Total(); got != tc.want {
				t.Errorf("Total() = %d, want %d", got, tc.want)
			}
		})
	}
}
