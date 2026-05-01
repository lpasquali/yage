// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

// Tests for EnsureYageSystemOnCluster.
//
// We use k8s.io/client-go/kubernetes/fake.NewSimpleClientset() injected
// into a k8sclient.Client to exercise the function without a real cluster.
//
// Assertions: after the call, the SA / Role / RoleBinding all exist in
// yage-system with the expected names and managed-by label. On a second
// call (idempotency) no error is returned.

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/lpasquali/yage/internal/platform/k8sclient"
)

// fakeClient wraps a fake clientset inside a k8sclient.Client. The
// yage-system namespace is pre-created so EnsureNamespace's Get call
// succeeds without needing the REST mapper (which is nil in unit tests).
// Only the Typed field is exercised by EnsureYageSystemOnCluster.
func fakeClient(t *testing.T) *k8sclient.Client {
	t.Helper()
	cs := k8sfake.NewClientset()
	// Pre-create yage-system so EnsureNamespace takes the early-return path.
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: YageSystemNamespace},
	}
	if _, err := cs.CoreV1().Namespaces().Create(context.Background(), ns, metav1.CreateOptions{}); err != nil {
		t.Fatalf("pre-create yage-system namespace: %v", err)
	}
	return &k8sclient.Client{
		Typed: cs,
	}
}

func TestEnsureYageSystemOnCluster_CreatesResources(t *testing.T) {
	cli := fakeClient(t)
	ctx := context.Background()

	if err := EnsureYageSystemOnCluster(ctx, cli); err != nil {
		t.Fatalf("EnsureYageSystemOnCluster: unexpected error: %v", err)
	}

	ns := YageSystemNamespace

	// Verify ServiceAccount.
	sa, err := cli.Typed.CoreV1().ServiceAccounts(ns).Get(ctx, yageJobRunnerName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("ServiceAccount not found after EnsureYageSystemOnCluster: %v", err)
	}
	if sa.Labels["app.kubernetes.io/managed-by"] != "yage" {
		t.Errorf("ServiceAccount missing managed-by=yage label, got: %v", sa.Labels)
	}

	// Verify Role.
	role, err := cli.Typed.RbacV1().Roles(ns).Get(ctx, yageJobRunnerName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Role not found after EnsureYageSystemOnCluster: %v", err)
	}
	if role.Labels["app.kubernetes.io/managed-by"] != "yage" {
		t.Errorf("Role missing managed-by=yage label, got: %v", role.Labels)
	}
	assertRoleRules(t, role)

	// Verify RoleBinding.
	rb, err := cli.Typed.RbacV1().RoleBindings(ns).Get(ctx, yageJobRunnerName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("RoleBinding not found after EnsureYageSystemOnCluster: %v", err)
	}
	if rb.Labels["app.kubernetes.io/managed-by"] != "yage" {
		t.Errorf("RoleBinding missing managed-by=yage label, got: %v", rb.Labels)
	}
	if rb.RoleRef.Name != yageJobRunnerName {
		t.Errorf("RoleBinding.RoleRef.Name = %q, want %q", rb.RoleRef.Name, yageJobRunnerName)
	}
	if rb.RoleRef.Kind != "Role" {
		t.Errorf("RoleBinding.RoleRef.Kind = %q, want Role", rb.RoleRef.Kind)
	}
	if len(rb.Subjects) != 1 || rb.Subjects[0].Name != yageJobRunnerName {
		t.Errorf("RoleBinding.Subjects unexpected: %+v", rb.Subjects)
	}
	if rb.Subjects[0].Namespace != ns {
		t.Errorf("RoleBinding.Subjects[0].Namespace = %q, want %q", rb.Subjects[0].Namespace, ns)
	}
}

func TestEnsureYageSystemOnCluster_Idempotent(t *testing.T) {
	cli := fakeClient(t)
	ctx := context.Background()

	if err := EnsureYageSystemOnCluster(ctx, cli); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := EnsureYageSystemOnCluster(ctx, cli); err != nil {
		t.Fatalf("second call (idempotent): %v", err)
	}
}

// assertRoleRules verifies the four required PolicyRules are present.
func assertRoleRules(t *testing.T, role *rbacv1.Role) {
	t.Helper()

	type ruleCheck struct {
		group    string
		resource string
		verb     string
	}
	required := []ruleCheck{
		{"", "secrets", "create"},
		{"", "secrets", "delete"},
		{"", "persistentvolumeclaims", "create"},
		{"", "pods", "get"},
		{"", "pods/log", "watch"},
		{"batch", "jobs", "create"},
		{"batch", "jobs", "delete"},
	}

	for _, rc := range required {
		found := false
		for _, rule := range role.Rules {
			if !containsString(rule.APIGroups, rc.group) {
				continue
			}
			if !containsString(rule.Resources, rc.resource) {
				continue
			}
			if !containsString(rule.Verbs, rc.verb) {
				continue
			}
			found = true
			break
		}
		if !found {
			t.Errorf("Role missing rule: apiGroup=%q resource=%q verb=%q", rc.group, rc.resource, rc.verb)
		}
	}
}

// containsString reports whether slice contains s.
func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}
