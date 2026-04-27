// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

// Package k8sclient provides the in-process Kubernetes client used by
// the rest of the orchestrator (no kubectl shell-outs). A single
// Client carries the typed clientset, dynamic client, REST mapper,
// and a server-side-apply helper, all bound to a specific kubeconfig
// context.
//
// The constructors mirror the kubeconfig selection that `kubectl
// --context` performs:
//   - ForContext: load kubeconfig (KUBECONFIG or ~/.kube/config), select context
//   - ForKubeconfigFile: load a specific kubeconfig file (workload clusters)
//   - ForCurrent: current-context fallback
package k8sclient

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/discovery"
	memory "k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/yaml"
)

// FieldManager is the FieldManager string used for all server-side applies.
const FieldManager = "yage"

// Client bundles the four handles every caller needs.
type Client struct {
	Context   string
	Config    *rest.Config
	Typed     kubernetes.Interface
	Dynamic   dynamic.Interface
	Mapper    meta.RESTMapper
	Discovery discovery.DiscoveryInterface
}

// ForContext builds a Client bound to the named kubeconfig context. When
// ctx is empty, the current-context is used.
func ForContext(kubeContext string) (*Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	overrides := &clientcmd.ConfigOverrides{}
	if kubeContext != "" {
		overrides.CurrentContext = kubeContext
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	return fromClientConfig(cc, kubeContext)
}

// ForKubeconfigFile builds a Client from a specific kubeconfig file path.
// Used for workload clusters whose kubeconfig is materialised to a temp
// file from the management cluster Secret.
func ForKubeconfigFile(path string) (*Client, error) {
	loader := &clientcmd.ClientConfigLoadingRules{ExplicitPath: path}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loader, &clientcmd.ConfigOverrides{})
	return fromClientConfig(cc, "")
}

// ForCurrent is sugar for ForContext("").
func ForCurrent() (*Client, error) { return ForContext("") }

func fromClientConfig(cc clientcmd.ClientConfig, requestedCtx string) (*Client, error) {
	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("loading kubeconfig: %w", err)
	}
	cfg.QPS = 50
	cfg.Burst = 100
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("typed clientset: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("discovery client: %w", err)
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(memory.NewMemCacheClient(disc))
	used := requestedCtx
	if used == "" {
		raw, _ := cc.RawConfig()
		used = raw.CurrentContext
	}
	return &Client{
		Context:   used,
		Config:    cfg,
		Typed:     typed,
		Dynamic:   dyn,
		Mapper:    mapper,
		Discovery: disc,
	}, nil
}

// ContextExists reports whether the named kubeconfig context exists in the
// merged client config (env KUBECONFIG + ~/.kube/config).
func ContextExists(name string) bool {
	cfg, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return false
	}
	_, ok := cfg.Contexts[name]
	return ok
}

// CurrentContext returns the merged client config's current-context.
func CurrentContext() string {
	cfg, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return ""
	}
	return cfg.CurrentContext
}

// ListContexts returns the names of all kubeconfig contexts known to the
// merged client config.
func ListContexts() []string {
	cfg, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Contexts))
	for k := range cfg.Contexts {
		out = append(out, k)
	}
	return out
}

// IsNotFound is a thin wrapper over apierrors.IsNotFound for callers that
// don't already import the apierrors package.
func IsNotFound(err error) bool { return apierrors.IsNotFound(err) }

// IsAlreadyExists wraps apierrors.IsAlreadyExists.
func IsAlreadyExists(err error) bool { return apierrors.IsAlreadyExists(err) }

// ApplyYAML server-side-applies a single YAML document. It accepts either
// a raw map[string]any or a YAML byte stream; the latter is parsed first.
// Force=true so we win conflicts on second apply.
func (c *Client) ApplyYAML(ctx context.Context, doc []byte) error {
	u := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(doc, &u.Object); err != nil {
		return fmt.Errorf("unmarshal yaml: %w", err)
	}
	if u.Object == nil || u.GetKind() == "" {
		return nil
	}
	return c.ApplyUnstructured(ctx, u)
}

// ApplyUnstructured server-side-applies a parsed unstructured object.
func (c *Client) ApplyUnstructured(ctx context.Context, u *unstructured.Unstructured) error {
	gvk := u.GroupVersionKind()
	mapping, err := c.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return fmt.Errorf("rest mapping for %s: %w", gvk, err)
	}
	var ri dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ns := u.GetNamespace()
		if ns == "" {
			ns = "default"
		}
		ri = c.Dynamic.Resource(mapping.Resource).Namespace(ns)
	} else {
		ri = c.Dynamic.Resource(mapping.Resource)
	}
	data, err := yaml.Marshal(u.Object)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	jdata, err := yaml.YAMLToJSON(data)
	if err != nil {
		return fmt.Errorf("yaml→json: %w", err)
	}
	_, err = ri.Patch(ctx, u.GetName(), types.ApplyPatchType, jdata, metav1.PatchOptions{
		FieldManager: FieldManager,
		Force:        boolPtr(true),
	})
	if err != nil {
		return fmt.Errorf("apply %s/%s %s: %w", mapping.Resource.Resource, u.GetName(), gvk, err)
	}
	return nil
}

// ApplyMultiDocYAML splits a buffer on `\n---\n`, then ApplyYAML each
// non-empty document. Mirrors `kubectl apply -f file.yaml` for multi-doc.
func (c *Client) ApplyMultiDocYAML(ctx context.Context, blob []byte) error {
	for _, doc := range splitYAMLDocs(blob) {
		if len(doc) == 0 {
			continue
		}
		if err := c.ApplyYAML(ctx, doc); err != nil {
			return err
		}
	}
	return nil
}

// DeleteByGVKName deletes a resource by GVK + name (+ namespace for namespaced
// kinds). Returns nil when the object is already gone.
func (c *Client) DeleteByGVKName(ctx context.Context, gvk schema.GroupVersionKind, namespace, name string) error {
	mapping, err := c.Mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return err
	}
	var ri dynamic.ResourceInterface
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		ri = c.Dynamic.Resource(mapping.Resource).Namespace(namespace)
	} else {
		ri = c.Dynamic.Resource(mapping.Resource)
	}
	if err := ri.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// PollUntil polls `check` every `interval` until it returns true or `timeout`
// elapses. Returns nil on success or context.DeadlineExceeded on timeout.
func PollUntil(ctx context.Context, interval, timeout time.Duration, check func(context.Context) (bool, error)) error {
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		ok, err := check(deadline)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		select {
		case <-deadline.Done():
			return deadline.Err()
		case <-tick.C:
		}
	}
}

// RawConfigFor returns the underlying clientcmd RawConfig for a context. Used
// by callers that need to enumerate contexts before constructing a Client.
func RawConfigFor() (clientcmdapi.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	c := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{})
	return c.RawConfig()
}

// ConfigFlagsForContext returns a *genericclioptions.ConfigFlags pointed at
// the named context. Useful for libraries (helm action.Configuration) that
// take a ConfigFlags rather than a *rest.Config.
func ConfigFlagsForContext(name string) *genericclioptions.ConfigFlags {
	f := genericclioptions.NewConfigFlags(true)
	if name != "" {
		f.Context = strPtr(name)
	}
	return f
}

func splitYAMLDocs(blob []byte) [][]byte {
	out := [][]byte{}
	for _, p := range strings.Split(string(blob), "\n---\n") {
		out = append(out, []byte(strings.TrimSpace(p)))
	}
	return out
}

func boolPtr(b bool) *bool   { return &b }
func strPtr(s string) *string { return &s }

// EnsureNamespace creates the namespace if absent, returns nil if it already
// exists. Mirrors `kubectl create namespace X --dry-run=client -o yaml | kubectl apply -f -`.
func (c *Client) EnsureNamespace(ctx context.Context, name string) error {
	_, err := c.Typed.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	body := fmt.Sprintf("apiVersion: v1\nkind: Namespace\nmetadata:\n  name: %s\n", name)
	return c.ApplyYAML(ctx, []byte(body))
}

// FileBacked materialises a kubeconfig file derived from a Secret data key
// to a temp file path; callers use it to drive workflows that still need a
// KUBECONFIG file (e.g. helm). Returns the temp file path and a cleanup fn.
func WriteTempKubeconfig(prefix string, body []byte) (string, func(), error) {
	f, err := os.CreateTemp("", prefix+"-*.kubeconfig")
	if err != nil {
		return "", func() {}, err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", func() {}, err
	}
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}