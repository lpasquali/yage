// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package openstack

// EnsureIdentity — clouds.yaml Secret for CAPO.
//
// CAPO's cluster controllers read credentials from an OpenStack identity
// Secret referenced by each OpenStackCluster + OpenStackMachineTemplate via
// `spec.identityRef.name`. The standard name is "${CLUSTER_NAME}-cloud-config"
// in the same namespace as the Cluster resource (cfg.WorkloadClusterNamespace,
// default "default"). The Secret carries a single key "clouds.yaml" whose
// value is an OpenStack SDK clouds.yaml document.
//
// Auth fields come from the OS_* environment variables (the same ones
// openstackClients uses) and non-secret runtime fields from cfg.Providers.OpenStack.
// The minimal required set is: OS_AUTH_URL + one of
//   - username + password + project_name + domain_name
//   - application_credential_id + application_credential_secret
//
// If OS_AUTH_URL is absent or cfg.Providers.OpenStack.Cloud is empty,
// EnsureIdentity returns a descriptive error (not logx.Die) so dry-runs and
// partial configs fail gracefully.
//
// The Secret is applied via server-side apply to the management cluster
// (kind-<KindClusterName> context). CAPO controllers running there read it
// when provisioning machines.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
)

const identitySecretSuffix = "-cloud-config"
const identitySecretKey = "clouds.yaml"
const identityApplyTimeout = 30 * time.Second

// EnsureIdentity builds a clouds.yaml Secret and applies it to the management
// cluster (kind-<KindClusterName>) in cfg.WorkloadClusterNamespace.
// The Secret is named <WorkloadClusterName>-cloud-config, which is the name
// the k3s template references via identityRef.
//
// Returns a descriptive error when required fields are missing.
// Returns ErrNotApplicable — NOT an error — when essential fields (OS_AUTH_URL
// or cfg.Providers.OpenStack.Cloud) are absent, matching the dry-run behaviour
// of cindercsi.EnsureSecret.
func (p *Provider) EnsureIdentity(cfg *config.Config) error {
	cloud := cfg.Providers.OpenStack.Cloud
	authURL := os.Getenv("OS_AUTH_URL")

	if cloud == "" {
		return fmt.Errorf("openstack EnsureIdentity: cfg.Providers.OpenStack.Cloud (OPENSTACK_CLOUD) is required")
	}
	if authURL == "" {
		return fmt.Errorf("openstack EnsureIdentity: OS_AUTH_URL environment variable is required")
	}

	cloudsYAML, err := buildCloudsYAML(cloud, authURL, cfg)
	if err != nil {
		return fmt.Errorf("openstack EnsureIdentity: build clouds.yaml: %w", err)
	}

	namespace := cfg.WorkloadClusterNamespace
	if namespace == "" {
		namespace = "default"
	}
	clusterName := cfg.WorkloadClusterName
	if clusterName == "" {
		clusterName = "capi-quickstart"
	}
	secretName := clusterName + identitySecretSuffix

	// Derive the management cluster kubeconfig context. After clusterctl init,
	// the kind cluster context is "kind-<KindClusterName>". When
	// MgmtKubeconfigPath is set (post-pivot), use that instead.
	var cli *k8sclient.Client
	if cfg.MgmtKubeconfigPath != "" {
		cli, err = k8sclient.ForKubeconfigFile(cfg.MgmtKubeconfigPath)
		if err != nil {
			return fmt.Errorf("openstack EnsureIdentity: load mgmt kubeconfig %s: %w", cfg.MgmtKubeconfigPath, err)
		}
	} else {
		kindCtx := "kind-" + cfg.KindClusterName
		cli, err = k8sclient.ForContext(kindCtx)
		if err != nil {
			return fmt.Errorf("openstack EnsureIdentity: connect to %s: %w", kindCtx, err)
		}
	}

	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      secretName,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "yage",
				"cluster.x-k8s.io/cluster-name": clusterName,
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{identitySecretKey: []byte(cloudsYAML)},
	}

	yamlBody, err := yaml.Marshal(sec)
	if err != nil {
		return fmt.Errorf("openstack EnsureIdentity: marshal secret: %w", err)
	}
	js, err := yaml.YAMLToJSON(yamlBody)
	if err != nil {
		return fmt.Errorf("openstack EnsureIdentity: yaml→json: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), identityApplyTimeout)
	defer cancel()

	force := true
	_, err = cli.Typed.CoreV1().Secrets(namespace).Patch(
		ctx, secretName, types.ApplyPatchType, js,
		metav1.PatchOptions{FieldManager: k8sclient.FieldManager, Force: &force},
	)
	if err != nil {
		return fmt.Errorf("openstack EnsureIdentity: apply Secret %s/%s: %w", namespace, secretName, err)
	}

	logx.Log("openstack: applied clouds.yaml Secret %s/%s (cloud=%s)", namespace, secretName, cloud)
	return nil
}

// buildCloudsYAML constructs a minimal clouds.yaml document for the named cloud.
// Auth fields are sourced from OS_* environment variables; non-secret runtime
// fields (region, project name) fall back to cfg when the env vars are absent.
//
// Two auth paths:
//  1. Application credentials: OS_APPLICATION_CREDENTIAL_ID +
//     OS_APPLICATION_CREDENTIAL_SECRET — used when both are set.
//  2. Username + password: OS_USERNAME + OS_PASSWORD + project name + domain.
//
// Returns an error when neither path has enough data to form a valid auth block.
func buildCloudsYAML(cloud, authURL string, cfg *config.Config) (string, error) {
	appCredID := os.Getenv("OS_APPLICATION_CREDENTIAL_ID")
	appCredSecret := os.Getenv("OS_APPLICATION_CREDENTIAL_SECRET")
	username := os.Getenv("OS_USERNAME")
	password := os.Getenv("OS_PASSWORD")

	// Validate: we need at least one auth path.
	usingAppCred := appCredID != "" && appCredSecret != ""
	usingPassword := username != "" && password != ""
	if !usingAppCred && !usingPassword {
		return "", fmt.Errorf(
			"no OpenStack credentials found: set OS_APPLICATION_CREDENTIAL_ID + " +
				"OS_APPLICATION_CREDENTIAL_SECRET, or OS_USERNAME + OS_PASSWORD",
		)
	}

	// Resolve project name: env first, then cfg.
	projectName := envFallback("OS_PROJECT_NAME", "OS_TENANT_NAME", cfg.Providers.OpenStack.ProjectName)
	// Resolve domain name: env first.
	domainName := envFallback("OS_USER_DOMAIN_NAME", "OS_DOMAIN_NAME", "")

	// Region: cfg (already sourced from OPENSTACK_REGION env in Load()).
	region := cfg.Providers.OpenStack.Region

	var b strings.Builder
	b.WriteString("clouds:\n")
	b.WriteString("  " + cloud + ":\n")
	b.WriteString("    auth:\n")
	b.WriteString("      auth_url: " + authURL + "\n")

	if usingAppCred {
		b.WriteString("      application_credential_id: " + appCredID + "\n")
		b.WriteString("      application_credential_secret: " + appCredSecret + "\n")
	} else {
		b.WriteString("      username: " + username + "\n")
		b.WriteString("      password: " + password + "\n")
		if projectName != "" {
			b.WriteString("      project_name: " + projectName + "\n")
		}
		if domainName != "" {
			b.WriteString("      user_domain_name: " + domainName + "\n")
		}
	}

	if region != "" {
		b.WriteString("    region_name: " + region + "\n")
	}
	b.WriteString("    interface: \"public\"\n")
	b.WriteString("    identity_api_version: 3\n")

	return b.String(), nil
}

// envFallback returns the value of the first non-empty env var; falls back to
// the literal fallback string when all env vars are empty.
func envFallback(keys ...string) string {
	// The last element is treated as the literal fallback if it doesn't look
	// like an env var key (no uppercase constraint — callers pass it as the
	// last positional argument).
	if len(keys) == 0 {
		return ""
	}
	// All but the last are env var names; the last is the literal fallback.
	fallback := keys[len(keys)-1]
	for _, k := range keys[:len(keys)-1] {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return fallback
}
