package orchestrator

import "strings"

// imageMirrorFlags lists the clusterctl init flags whose values are
// CAPI provider image references that should be rewritten through the
// operator's internal mirror. clusterctl accepts e.g.
// `--core registry.k8s.io/cluster-api/cluster-api-controller:v1.13.0`,
// and we rewrite the value (not the flag name).
var imageMirrorFlags = map[string]struct{}{
	"--core":          {},
	"--bootstrap":     {},
	"--control-plane": {},
	"--infrastructure": {},
}

// imageMirrorPrefixes lists the public registry prefixes that
// applyImageMirror replaces with the configured mirror prefix. Each
// entry includes the trailing slash so we don't accidentally rewrite a
// value that merely starts with a substring (e.g. "ghcr.iox/...").
var imageMirrorPrefixes = []string{
	"registry.k8s.io/",
	"ghcr.io/",
	"quay.io/",
}

// applyImageMirror rewrites CAPI provider image references on a
// `clusterctl init` argv slice so they come from the operator's
// internal mirror instead of the public registries.
//
// When mirror is empty, args is returned unchanged (zero-cost path for
// non-airgapped deployments).
//
// Otherwise, for each occurrence of an image-bearing flag (--core,
// --bootstrap, --control-plane, --infrastructure) we look at the
// following argv element and, if it begins with one of the known
// public registry prefixes (registry.k8s.io/, ghcr.io/, quay.io/),
// replace that prefix with mirror+"/". Bare provider names like
// `--infrastructure proxmox` are left alone — they don't match any
// registry prefix.
//
// Provider-side ClusterctlInitArgs stays oblivious to the mirror; the
// orchestrator wraps its result with this helper at the call site.
func applyImageMirror(args []string, mirror string) []string {
	if mirror == "" {
		return args
	}
	mirror = strings.TrimRight(mirror, "/")
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i < len(out)-1; i++ {
		if _, ok := imageMirrorFlags[out[i]]; !ok {
			continue
		}
		val := out[i+1]
		for _, p := range imageMirrorPrefixes {
			if strings.HasPrefix(val, p) {
				out[i+1] = mirror + "/" + strings.TrimPrefix(val, p)
				break
			}
		}
	}
	return out
}
