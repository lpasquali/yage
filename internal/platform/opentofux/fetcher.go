// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 Luca Pasquali

package opentofux

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/lpasquali/yage/internal/config"
	"github.com/lpasquali/yage/internal/platform/k8sclient"
	"github.com/lpasquali/yage/internal/ui/logx"
)

// Fetcher resolves yage-tofu module paths from the in-cluster yage-repos PVC.
// The PVC is mounted at /repos inside Job pods. Use EnsureRepoSync (issue #144)
// to populate it before calling ModulePath.
type Fetcher struct {
	// MountRoot is the path at which the yage-repos PVC is mounted in the
	// current pod. Defaults to /repos when empty.
	MountRoot string
}

// ModulePath returns the absolute path to the named module directory.
// e.g. ModulePath("proxmox") → "/repos/yage-tofu/proxmox"
func (f *Fetcher) ModulePath(module string) string {
	root := f.MountRoot
	if root == "" {
		root = "/repos"
	}
	return filepath.Join(root, "yage-tofu", module)
}

const (
	repoSyncJobName = "yage-repo-sync"
	reposPVCName    = "yage-repos"
)

// repoSyncLabels returns labels for yage-repo-sync resources.
func repoSyncLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "yage",
		"app.kubernetes.io/component":  "repo-sync",
	}
}

// repoSyncStorageClass returns the StorageClass to use for the yage-repos PVC,
// using the same priority chain as JobRunner.storageClassName():
//  1. cfg.CSI.DefaultClass
//  2. cfg.Providers.Proxmox.CSIStorageClassName
//  3. "standard" (kind's local-path provisioner default)
func repoSyncStorageClass(cfg *config.Config) string {
	if cfg.CSI.DefaultClass != "" {
		return cfg.CSI.DefaultClass
	}
	if cfg.Providers.Proxmox.CSIStorageClassName != "" {
		return cfg.Providers.Proxmox.CSIStorageClassName
	}
	return "standard"
}

// repoSyncImagePrefix prepends the mirror prefix to an image reference.
// When cfg.ImageRegistryMirror is empty, the image is returned as-is.
// When set, the mirror is prepended with "/": <mirror>/<image>.
// Unlike tofuImageRef, Docker Hub images have no host prefix to strip.
func repoSyncImageRef(cfg *config.Config, image string) string {
	if cfg.ImageRegistryMirror != "" {
		return cfg.ImageRegistryMirror + "/" + image
	}
	return image
}

// reposPVCSize returns the storage size for the yage-repos PVC.
// Falls back to "500Mi" when the config field is empty.
func reposPVCSize(cfg *config.Config) string {
	if cfg.ReposPVCSize != "" {
		return cfg.ReposPVCSize
	}
	return "500Mi"
}

// EnsureRepoSync creates the yage-repos PVC (if absent) and runs the
// yage-repo-sync Job to clone/fetch yage-tofu and yage-manifests.
// Cluster-agnostic: takes a *k8sclient.Client so it can be called on
// both the kind bootstrap cluster and the management cluster after pivot.
func EnsureRepoSync(ctx context.Context, cli *k8sclient.Client, cfg *config.Config) error {
	ns := yageNamespace

	// Ensure the namespace exists (defensive; may already be present).
	if err := cli.EnsureNamespace(ctx, ns); err != nil {
		return fmt.Errorf("EnsureRepoSync: ensure namespace: %w", err)
	}

	// Step 1: Create yage-repos PVC (idempotent via SSA).
	if err := ensureReposPVC(ctx, cli, cfg, ns); err != nil {
		return fmt.Errorf("EnsureRepoSync: PVC: %w", err)
	}

	// Step 2: Delete any pre-existing job and submit a fresh one.
	if err := submitRepoSyncJob(ctx, cli, cfg, ns); err != nil {
		return fmt.Errorf("EnsureRepoSync: submit job: %w", err)
	}

	// Step 3: Wait for a pod, stream logs, wait for completion.
	podName, err := waitForRepoSyncPod(ctx, cli, ns)
	if err != nil {
		return fmt.Errorf("EnsureRepoSync: wait for pod: %w", err)
	}
	if err := streamRepoSyncLogs(ctx, cli, ns, podName); err != nil {
		logx.Warn("EnsureRepoSync: log streaming interrupted for pod %s: %v", podName, err)
	}
	if err := waitForRepoSyncJob(ctx, cli, ns); err != nil {
		return fmt.Errorf("EnsureRepoSync: %w", err)
	}

	// Step 4: Clean up the Job after success.
	background := metav1.DeletePropagationBackground
	_ = cli.Typed.BatchV1().Jobs(ns).Delete(ctx, repoSyncJobName, metav1.DeleteOptions{
		PropagationPolicy: &background,
	})

	logx.Log("EnsureRepoSync: yage-repos PVC populated (yage-tofu@%s, yage-manifests@%s).",
		cfg.TofuRef, cfg.ManifestsRef)
	return nil
}

// ensureReposPVC server-side-applies the yage-repos PVC.
func ensureReposPVC(ctx context.Context, cli *k8sclient.Client, cfg *config.Config, ns string) error {
	sc := repoSyncStorageClass(cfg)
	pvc := corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "PersistentVolumeClaim",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      reposPVCName,
			Namespace: ns,
			Labels:    repoSyncLabels(),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(reposPVCSize(cfg)),
				},
			},
			StorageClassName: &sc,
		},
	}
	body, err := json.Marshal(pvc)
	if err != nil {
		return fmt.Errorf("marshal PVC: %w", err)
	}
	_, err = cli.Typed.CoreV1().PersistentVolumeClaims(ns).Patch(
		ctx, reposPVCName, types.ApplyPatchType, body,
		metav1.PatchOptions{
			FieldManager: k8sclient.FieldManager,
			Force:        boolPtrFetcher(true),
		},
	)
	return err
}

// gitSyncCommand builds the shell command for an init container that
// clones (or fast-forwards) a git repository at a specific ref.
func gitSyncCommand(targetDir string) string {
	return fmt.Sprintf(
		`if [ -d %s/.git ]; then`+
			` git -C %s fetch --depth=1 origin "$REF" && git -C %s checkout FETCH_HEAD;`+
			` else git clone --depth=1 --branch "$REF" "$REPO" %s; fi`,
		targetDir, targetDir, targetDir, targetDir,
	)
}

// submitRepoSyncJob deletes any pre-existing yage-repo-sync Job and creates a fresh one.
func submitRepoSyncJob(ctx context.Context, cli *k8sclient.Client, cfg *config.Config, ns string) error {
	// Delete pre-existing job (background propagation to avoid blocking).
	background := metav1.DeletePropagationBackground
	_ = cli.Typed.BatchV1().Jobs(ns).Delete(ctx, repoSyncJobName, metav1.DeleteOptions{
		PropagationPolicy: &background,
	})

	gitImage := repoSyncImageRef(cfg, "alpine/git:2")
	busyboxImage := repoSyncImageRef(cfg, "busybox:stable")

	ttl := int32(0)
	backoffLimit := int32(0)

	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "batch/v1",
			Kind:       "Job",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      repoSyncJobName,
			Namespace: ns,
			Labels:    repoSyncLabels(),
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			BackoffLimit:            &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: repoSyncLabels(),
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "yage-job-runner",
					RestartPolicy:      corev1.RestartPolicyNever,
					InitContainers: []corev1.Container{
						{
							Name:  "sync-yage-tofu",
							Image: gitImage,
							Env: []corev1.EnvVar{
								{Name: "REPO", Value: cfg.TofuRepo},
								{Name: "REF", Value: cfg.TofuRef},
							},
							Command: []string{"sh", "-c", gitSyncCommand("/repos/yage-tofu")},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "repos", MountPath: "/repos"},
							},
						},
						{
							Name:  "sync-yage-manifests",
							Image: gitImage,
							Env: []corev1.EnvVar{
								{Name: "REPO", Value: cfg.ManifestsRepo},
								{Name: "REF", Value: cfg.ManifestsRef},
							},
							Command: []string{"sh", "-c", gitSyncCommand("/repos/yage-manifests")},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "repos", MountPath: "/repos"},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:    "done",
							Image:   busyboxImage,
							Command: []string{"true"},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "repos",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: reposPVCName,
								},
							},
						},
					},
				},
			},
		},
	}

	_, err := cli.Typed.BatchV1().Jobs(ns).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create job %s: %w", repoSyncJobName, err)
	}
	logx.Log("EnsureRepoSync: job %s/%s created; waiting for pod ...", ns, repoSyncJobName)
	return nil
}

// waitForRepoSyncPod polls until the yage-repo-sync Job has a pod in
// Running, Succeeded, or Failed phase. Returns the pod name.
func waitForRepoSyncPod(ctx context.Context, cli *k8sclient.Client, ns string) (string, error) {
	var podName string
	err := k8sclient.PollUntil(ctx, 3*time.Second, 5*time.Minute, func(c context.Context) (bool, error) {
		pods, err := cli.Typed.CoreV1().Pods(ns).List(c, metav1.ListOptions{
			LabelSelector: "job-name=" + repoSyncJobName,
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
		return "", fmt.Errorf("timed out waiting for pod (job %s/%s)", ns, repoSyncJobName)
	}
	return podName, nil
}

// streamRepoSyncLogs streams the pod logs to logx.Log in real time.
func streamRepoSyncLogs(ctx context.Context, cli *k8sclient.Client, ns, podName string) error {
	req := cli.Typed.CoreV1().Pods(ns).GetLogs(podName, &corev1.PodLogOptions{
		Follow: true,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return fmt.Errorf("open log stream for pod %s: %w", podName, err)
	}
	defer stream.Close()

	scanner := bufio.NewScanner(stream)
	for scanner.Scan() {
		logx.Log("[repo-sync] %s", scanner.Text())
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// waitForRepoSyncJob polls until the yage-repo-sync Job reaches Complete or Failed.
func waitForRepoSyncJob(ctx context.Context, cli *k8sclient.Client, ns string) error {
	return k8sclient.PollUntil(ctx, 5*time.Second, 10*time.Minute, func(c context.Context) (bool, error) {
		job, err := cli.Typed.BatchV1().Jobs(ns).Get(c, repoSyncJobName, metav1.GetOptions{})
		if err != nil {
			return false, nil // transient — retry
		}
		for _, cond := range job.Status.Conditions {
			if cond.Type == batchv1.JobComplete && cond.Status == corev1.ConditionTrue {
				logx.Log("EnsureRepoSync: job %s/%s completed successfully.", ns, repoSyncJobName)
				return true, nil
			}
			if cond.Type == batchv1.JobFailed && cond.Status == corev1.ConditionTrue {
				return true, fmt.Errorf("repo-sync job %s/%s failed: %s", ns, repoSyncJobName, cond.Message)
			}
		}
		return false, nil
	})
}

// boolPtrFetcher is a local bool pointer helper (mirrors boolPtr in job_runner.go
// without introducing a shared unexported name).
func boolPtrFetcher(b bool) *bool { return &b }
