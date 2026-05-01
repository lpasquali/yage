// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package kindsync

// yagesystem.go — idempotent RBAC bootstrap for the yage-job-runner
// ServiceAccount on any Kubernetes cluster (kind or management).
//
// All objects are created via server-side apply (Force=true) using hardcoded
// Go structs ("from spec"), so no template files are needed and re-runs are
// always safe.
//
// Call order: EnsureNamespace → ServiceAccount → Role → RoleBinding.
// Each step returns on the first error — this is bootstrap, not best-effort.

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/lpasquali/yage/internal/platform/k8sclient"
)

const (
	yageJobRunnerName = "yage-job-runner"
)

// yageManagedLabel returns the standard managed-by label map.
func yageManagedLabel() map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "yage",
	}
}

// EnsureYageSystemOnCluster creates or updates the yage-job-runner
// ServiceAccount, Role, and RoleBinding in yage-system.
// Idempotent — safe to call on any cluster.
func EnsureYageSystemOnCluster(ctx context.Context, cli *k8sclient.Client) error {
	ns := YageSystemNamespace

	// Ensure the namespace exists before applying namespaced resources.
	if err := cli.EnsureNamespace(ctx, ns); err != nil {
		return fmt.Errorf("EnsureYageSystemOnCluster: ensure namespace %s: %w", ns, err)
	}

	if err := ensureJobRunnerServiceAccount(ctx, cli, ns); err != nil {
		return err
	}
	if err := ensureJobRunnerRole(ctx, cli, ns); err != nil {
		return err
	}
	if err := ensureJobRunnerRoleBinding(ctx, cli, ns); err != nil {
		return err
	}
	return nil
}

// ensureJobRunnerServiceAccount server-side-applies the yage-job-runner SA.
func ensureJobRunnerServiceAccount(ctx context.Context, cli *k8sclient.Client, ns string) error {
	sa := corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      yageJobRunnerName,
			Namespace: ns,
			Labels:    yageManagedLabel(),
		},
	}
	body, err := json.Marshal(sa)
	if err != nil {
		return fmt.Errorf("EnsureYageSystemOnCluster: marshal ServiceAccount: %w", err)
	}
	_, err = cli.Typed.CoreV1().ServiceAccounts(ns).Patch(
		ctx, yageJobRunnerName, types.ApplyPatchType, body,
		metav1.PatchOptions{
			FieldManager: k8sclient.FieldManager,
			Force:        boolTrue(),
		},
	)
	if err != nil {
		return fmt.Errorf("EnsureYageSystemOnCluster: apply ServiceAccount: %w", err)
	}
	return nil
}

// ensureJobRunnerRole server-side-applies the yage-job-runner Role.
func ensureJobRunnerRole(ctx context.Context, cli *k8sclient.Client, ns string) error {
	role := rbacv1.Role{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      yageJobRunnerName,
			Namespace: ns,
			Labels:    yageManagedLabel(),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumeclaims"},
				Verbs:     []string{"get", "list", "watch", "create"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "pods/log"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"jobs"},
				Verbs:     []string{"get", "list", "watch", "create", "delete"},
			},
		},
	}
	body, err := json.Marshal(role)
	if err != nil {
		return fmt.Errorf("EnsureYageSystemOnCluster: marshal Role: %w", err)
	}
	_, err = cli.Typed.RbacV1().Roles(ns).Patch(
		ctx, yageJobRunnerName, types.ApplyPatchType, body,
		metav1.PatchOptions{
			FieldManager: k8sclient.FieldManager,
			Force:        boolTrue(),
		},
	)
	if err != nil {
		return fmt.Errorf("EnsureYageSystemOnCluster: apply Role: %w", err)
	}
	return nil
}

// ensureJobRunnerRoleBinding server-side-applies the yage-job-runner RoleBinding.
func ensureJobRunnerRoleBinding(ctx context.Context, cli *k8sclient.Client, ns string) error {
	rb := rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      yageJobRunnerName,
			Namespace: ns,
			Labels:    yageManagedLabel(),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      yageJobRunnerName,
				Namespace: ns,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     yageJobRunnerName,
		},
	}
	body, err := json.Marshal(rb)
	if err != nil {
		return fmt.Errorf("EnsureYageSystemOnCluster: marshal RoleBinding: %w", err)
	}
	_, err = cli.Typed.RbacV1().RoleBindings(ns).Patch(
		ctx, yageJobRunnerName, types.ApplyPatchType, body,
		metav1.PatchOptions{
			FieldManager: k8sclient.FieldManager,
			Force:        boolTrue(),
		},
	)
	if err != nil {
		return fmt.Errorf("EnsureYageSystemOnCluster: apply RoleBinding: %w", err)
	}
	return nil
}
