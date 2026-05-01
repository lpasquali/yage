// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
)

const (
	yageNamespace = "yage-system"
	tofuImage     = "ghcr.io/opentofu/opentofu:latest"
)

// JobRunner implements Runner by creating Kubernetes resources (ConfigMap,
// Secret, Job) on the management cluster and streaming pod logs.
//
// State is stored in Kubernetes Secrets via the OpenTofu kubernetes backend
// (one Secret per module, named tfstate-default-<module> in yage-system).
// No PVCs are created for tofu state; the yage-repos PVC (EnsureRepoSync,
// issue #144) is a separate concern for HCL module content.
//
// TODO(#125): pre-kind ordering conflict — JobRunner requires a reachable
// management cluster and a yage-system namespace. The Fetcher (and thus the
// HCL module files) may be needed before the kind cluster exists (e.g. for
// the OpenTofu identity phase that bootstraps the cluster itself). The
// interface must not assume either ordering; callers must decide which Runner
// implementation to use based on whether a cluster is available.
type JobRunner struct {
	cfg     *config.Config
	client  *k8sclient.Client
	fetcher *Fetcher
}

// fetcher returns the Fetcher to use for resolving module paths.
// Falls back to a zero-value Fetcher (MountRoot defaults to /repos).
func (j *JobRunner) fetcherOrDefault() *Fetcher {
	if j.fetcher != nil {
		return j.fetcher
	}
	return &Fetcher{}
}

// NewJobRunner returns a JobRunner connected to the management cluster via
// the named kubeconfig context (e.g. "kind-yage-mgmt" during the kind phase
// or the mgmt context after pivot).
func NewJobRunner(cfg *config.Config, kubecontext string) (*JobRunner, error) {
	cl, err := k8sclient.ForContext(kubecontext)
	if err != nil {
		return nil, fmt.Errorf("job runner: connect to cluster (context %s): %w", kubecontext, err)
	}
	return &JobRunner{cfg: cfg, client: cl}, nil
}

// NewJobRunnerFromFile returns a JobRunner connected to the management cluster
// via a kubeconfig file path (e.g. after pivot when the mgmt kubeconfig is
// materialised from a Secret).
func NewJobRunnerFromFile(cfg *config.Config, kubeconfigPath string) (*JobRunner, error) {
	cl, err := k8sclient.ForKubeconfigFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("job runner: connect to cluster (file %s): %w", kubeconfigPath, err)
	}
	return &JobRunner{cfg: cfg, client: cl}, nil
}

// tofuImageRef returns the OpenTofu image reference, optionally prefixed
// with cfg.ImageRegistryMirror so airgapped deployments serve the image from
// an internal registry.
func (j *JobRunner) tofuImageRef() string {
	if j.cfg.ImageRegistryMirror != "" {
		// Drop the "ghcr.io/" host prefix and prepend the mirror.
		// ghcr.io/opentofu/opentofu:latest → <mirror>/opentofu/opentofu:latest
		return j.cfg.ImageRegistryMirror + "/opentofu/opentofu:latest"
	}
	return tofuImage
}

// Apply implements Runner. Creates (or updates) a ConfigMap with the HCL
// module files, a Secret for credentials, and a Job that runs
// `tofu init && tofu apply`. State is kept in a Kubernetes Secret via the
// OpenTofu kubernetes backend; no PVC is created for tofu state.
// Streams pod logs in real time and waits for the Job to complete.
func (j *JobRunner) Apply(ctx context.Context, module string, vars map[string]string) error {
	return j.runJob(ctx, module, "apply", vars)
}

// Destroy implements Runner. Same resource lifecycle as Apply but runs
// `tofu destroy -auto-approve`.
func (j *JobRunner) Destroy(ctx context.Context, module string) error {
	return j.runJob(ctx, module, "destroy", nil)
}

// Output implements Runner. It creates a short-lived Job that runs
// `tofu output -json` against the module's persisted kubernetes-backend state
// Secret, captures the terminated container's stdout log, and decodes the JSON
// map. The Job is deleted after a successful read; on error the Job is left for
// operator inspection.
//
// The pod must complete within 5 minutes. Sensitive values are returned as-is
// (the caller is responsible for not logging them). The map keys and value types
// follow encoding/json.Unmarshal conventions (string, float64, bool, map, slice).
func (j *JobRunner) Output(ctx context.Context, module string) (map[string]any, error) {
	ns := yageNamespace
	if err := j.client.EnsureNamespace(ctx, ns); err != nil {
		return nil, fmt.Errorf("job runner output: ensure namespace: %w", err)
	}

	// The output job needs the module HCL so tofu can initialise the backend
	// and discover the output definitions. Re-use the existing ConfigMap if
	// present (Apply must have run first); if not, build it now.
	cmName := "tofu-module-" + module
	if err := j.ensureModuleConfigMap(ctx, ns, cmName, module); err != nil {
		return nil, fmt.Errorf("job runner output: module configmap (%s): %w", module, err)
	}

	// Output reads from state only — no vars needed. Empty secret is fine;
	// the kubernetes backend authenticates via the pod ServiceAccount.
	secretName := "tofu-creds-" + module + "-output"
	if err := j.ensureCredsSecret(ctx, ns, secretName, map[string]string{}); err != nil {
		return nil, fmt.Errorf("job runner output: creds secret (%s): %w", module, err)
	}

	jobName := "tofu-" + module + "-output"
	background := metav1.DeletePropagationBackground
	_ = j.client.Typed.BatchV1().Jobs(ns).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &background,
	})

	job := j.buildJob(ns, jobName, cmName, secretName, module, "output")
	if _, err := j.client.Typed.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("job runner output: create job: %w", err)
	}
	logx.Log("JobRunner: output job %s/%s created; waiting for pod ...", ns, jobName)

	podName, err := j.waitForPod(ctx, ns, jobName)
	if err != nil {
		return nil, fmt.Errorf("job runner output: wait for pod: %w", err)
	}

	raw, err := j.capturePodLogs(ctx, ns, podName)
	if err != nil {
		return nil, fmt.Errorf("job runner output: capture pod logs: %w", err)
	}

	if err := j.waitForJob(ctx, ns, jobName); err != nil {
		return nil, fmt.Errorf("job runner output: job %s failed: %w", jobName, err)
	}

	// Clean up the output job after successful capture.
	_ = j.client.Typed.BatchV1().Jobs(ns).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &background,
	})

	raw = bytes.TrimSpace(raw)
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("job runner output: parse tofu output json (%s): %w (raw: %s)", module, err, truncate(string(raw), 200))
	}
	return result, nil
}

// capturePodLogs reads the complete stdout of a terminated (or running) pod
// into a buffer and returns it. Unlike streamLogs, this does not print to
// logx — it is used to capture machine-readable output (e.g. `tofu output -json`).
func (j *JobRunner) capturePodLogs(ctx context.Context, ns, podName string) ([]byte, error) {
	req := j.client.Typed.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
		Follow: true,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil, fmt.Errorf("open log stream for pod %s: %w", podName, err)
	}
	defer stream.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, stream); err != nil && err != io.EOF {
		return nil, fmt.Errorf("read logs for pod %s: %w", podName, err)
	}
	return buf.Bytes(), nil
}

// truncate returns the first n bytes of s, appending "..." when the string is longer.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// runJob is the shared implementation for Apply/Destroy/Output.
func (j *JobRunner) runJob(ctx context.Context, module, operation string, vars map[string]string) error {
	ns := yageNamespace
	if err := j.client.EnsureNamespace(ctx, ns); err != nil {
		return fmt.Errorf("job runner: ensure namespace %s: %w", ns, err)
	}

	// 1. Upload module HCL files to a ConfigMap.
	cmName := "tofu-module-" + module
	if err := j.ensureModuleConfigMap(ctx, ns, cmName, module); err != nil {
		return fmt.Errorf("job runner: module configmap (%s): %w", module, err)
	}

	// 2. Create (or replace) credentials Secret.
	// State is stored in a Kubernetes Secret via the OpenTofu kubernetes
	// backend (tfstate-default-<module> in yage-system); no state PVC needed.
	secretName := "tofu-creds-" + module
	if vars == nil {
		vars = map[string]string{}
	}
	if err := j.ensureCredsSecret(ctx, ns, secretName, vars); err != nil {
		return fmt.Errorf("job runner: creds secret (%s): %w", module, err)
	}

	// 3. Create and run the Job.
	jobName := fmt.Sprintf("tofu-%s-%s", module, operation)
	if err := j.createAndWaitJob(ctx, ns, jobName, cmName, secretName, module, operation); err != nil {
		return fmt.Errorf("job runner: job %s: %w", jobName, err)
	}

	return nil
}

// ensureModuleConfigMap reads all .tf files from the module directory on disk
// and uploads them as a ConfigMap. The caller must have fetched the yage-tofu
// repo before calling (or provided HCL files through another means).
//
// TODO(#144): modDir resolves to the yage-repos PVC mount (/repos/yage-tofu/<module>),
// which is only accessible inside Job pods. This will be empty when running on an
// operator workstation; full implementation is tracked in issue #144 (EnsureRepoSync).
func (j *JobRunner) ensureModuleConfigMap(ctx context.Context, ns, cmName, module string) error {
	// Locate the module directory from the in-cluster PVC mount.
	modDir := j.fetcherOrDefault().ModulePath(module)

	data := map[string]string{}
	err := filepath.WalkDir(modDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".tf") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		data[d.Name()] = string(raw)
		return nil
	})
	if err != nil {
		return fmt.Errorf("read module dir %s: %w", modDir, err)
	}

	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: ns,
			Labels:    yageLabels(),
		},
		Data: data,
	}
	_, err = j.client.Typed.CoreV1().ConfigMaps(ns).Get(ctx, cmName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = j.client.Typed.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{})
	} else if err == nil {
		_, err = j.client.Typed.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{})
	}
	return err
}

// ensureCredsSecret creates (or updates) the credentials Secret. The vars map
// is encoded as TF_VAR_<key> environment variables so OpenTofu picks them up
// without extra -var flags. The Secret is never logged.
func (j *JobRunner) ensureCredsSecret(ctx context.Context, ns, secretName string, vars map[string]string) error {
	data := make(map[string][]byte, len(vars))
	for k, v := range vars {
		// Encode vars as TF_VAR_<key> so OpenTofu treats them as input
		// variable overrides (https://opentofu.org/docs/language/values/variables/#environment-variables).
		data["TF_VAR_"+k] = []byte(v)
	}

	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: ns,
			Labels:    yageLabels(),
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}

	_, err := j.client.Typed.CoreV1().Secrets(ns).Get(ctx, secretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = j.client.Typed.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{})
	} else if err == nil {
		_, err = j.client.Typed.CoreV1().Secrets(ns).Update(ctx, secret, metav1.UpdateOptions{})
	}
	return err
}

// createAndWaitJob creates the Job, waits for a pod to start running, streams
// logs, then waits for the Job to complete or fail.
func (j *JobRunner) createAndWaitJob(ctx context.Context, ns, jobName, cmName, secretName, module, operation string) error {
	// Delete pre-existing job with the same name (from a previous run).
	background := metav1.DeletePropagationBackground
	_ = j.client.Typed.BatchV1().Jobs(ns).Delete(ctx, jobName, metav1.DeleteOptions{
		PropagationPolicy: &background,
	})

	job := j.buildJob(ns, jobName, cmName, secretName, module, operation)
	if _, err := j.client.Typed.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create job: %w", err)
	}
	logx.Log("JobRunner: job %s/%s created; waiting for pod ...", ns, jobName)

	// Wait for a pod created by the Job to reach Running or terminal.
	podName, err := j.waitForPod(ctx, ns, jobName)
	if err != nil {
		return fmt.Errorf("wait for pod: %w", err)
	}

	// Stream pod logs in real time.
	if err := j.streamLogs(ctx, ns, podName); err != nil {
		logx.Warn("JobRunner: log streaming interrupted for pod %s: %v", podName, err)
	}

	// Wait for the Job itself to complete or fail.
	return j.waitForJob(ctx, ns, jobName)
}

// buildJob constructs the Job spec.
//
// The pod runs as the yage-job-runner ServiceAccount, which has RBAC to
// read and write Secrets in yage-system. This is required by the OpenTofu
// kubernetes backend to store state as a Secret (tfstate-default-<module>).
// KUBE_NAMESPACE instructs the backend client which namespace to target.
func (j *JobRunner) buildJob(ns, jobName, cmName, secretName, module, operation string) *batchv1.Job {
	cmd := j.buildCommand(module, operation)
	backoffLimit := int32(0) // no retries — fail fast so the caller can react

	return &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ns,
			Labels:    yageLabels(),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: yageLabels()},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: "yage-job-runner",
					Containers: []corev1.Container{
						{
							Name:    "tofu",
							Image:   j.tofuImageRef(),
							Command: []string{"/bin/sh", "-c", cmd},
							Env: []corev1.EnvVar{
								{
									// Tells the OpenTofu kubernetes backend which
									// namespace to store state Secrets in.
									Name:  "KUBE_NAMESPACE",
									Value: yageNamespace,
								},
								{
									// Redirect tofu's plugin cache and lock file to
									// a writable path. The module ConfigMap is mounted
									// read-only, so tofu init cannot write .terraform/
									// into -chdir=/workspace/module.
									Name:  "TF_DATA_DIR",
									Value: "/tmp/.terraform",
								},
							},
							EnvFrom: []corev1.EnvFromSource{
								{
									SecretRef: &corev1.SecretEnvSource{
										LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
									},
								},
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "module",
									MountPath: "/workspace/module",
									ReadOnly:  true,
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "module",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: cmName},
								},
							},
						},
					},
				},
			},
		},
	}
}

// buildCommand returns the shell command string for the given operation.
//
// tofu init receives -backend-config=in_cluster_config=true so the
// kubernetes backend authenticates via the pod's ServiceAccount token rather
// than looking for ~/.kube/config (which does not exist inside a container).
// State is stored in a Kubernetes Secret; no -state= flag is used.
func (j *JobRunner) buildCommand(module, operation string) string {
	chdir := "-chdir=/workspace/module"
	initFlags := `-upgrade -backend-config=in_cluster_config=true`
	switch operation {
	case "destroy":
		return fmt.Sprintf(
			`tofu %s init %s && tofu %s destroy -auto-approve`,
			chdir, initFlags, chdir,
		)
	case "output":
		// Init stdout is suppressed so capturePodLogs receives only the JSON
		// from `tofu output -json` without progress text mixed in.
		return fmt.Sprintf(
			`tofu %s init %s >/dev/null 2>&1 && tofu %s output -json`,
			chdir, initFlags, chdir,
		)
	default: // "apply"
		return fmt.Sprintf(
			`tofu %s init %s && tofu %s apply -auto-approve`,
			chdir, initFlags, chdir,
		)
	}
}

// waitForPod polls until a pod owned by the Job is Running (or terminal).
// Returns the pod name when ready.
func (j *JobRunner) waitForPod(ctx context.Context, ns, jobName string) (string, error) {
	var podName string
	err := k8sclient.PollUntil(ctx, 3*time.Second, 5*time.Minute, func(c context.Context) (bool, error) {
		pods, err := j.client.Typed.CoreV1().Pods(ns).List(c, metav1.ListOptions{
			LabelSelector: "job-name=" + jobName,
		})
		if err != nil {
			return false, nil // transient API error — retry
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			switch p.Status.Phase {
			case corev1.PodRunning, corev1.PodSucceeded, corev1.PodFailed:
				podName = p.Name
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return "", fmt.Errorf("timed out waiting for pod (job %s/%s)", ns, jobName)
	}
	return podName, nil
}

// streamLogs opens a follow stream on the pod and prints each line via logx.Log.
func (j *JobRunner) streamLogs(ctx context.Context, ns, podName string) error {
	req := j.client.Typed.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
		Follow: true,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("open log stream for pod %s: %w", podName, err)
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		logx.Log("[tofu] %s", scanner.Text())
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// waitForJob polls until the Job reaches a terminal condition (Complete or Failed).
func (j *JobRunner) waitForJob(ctx context.Context, ns, jobName string) error {
	return k8sclient.PollUntil(ctx, 5*time.Second, 10*time.Minute, func(c context.Context) (bool, error) {
		job, err := j.client.Typed.BatchV1().Jobs(ns).Get(c, jobName, metav1.GetOptions{})
		if err != nil {
			return false, nil // transient — retry
		}
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
				logx.Log("JobRunner: job %s/%s completed successfully.", ns, jobName)
				return true, nil
			}
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
				return true, fmt.Errorf("job %s/%s failed: %s", ns, jobName, cond.Message)
			}
		}
		return false, nil
	})
}

// yageLabels returns the standard yage management labels.
func yageLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "yage",
		"app.kubernetes.io/component":  "tofu-runner",
	}
}
