// Package argocdx will host Argo CD Operator / ArgoCD CR / CAAPH
// app-of-apps helpers. Stub only.
//
// Bash source map: scattered across L4000-L8500 under apply_workload_argocd_*,
// caaph_apply_workload_argo_helm_proxies, argocd_*_access helpers, …
package argocdx

import "fmt"

func todo(desc string) error { return fmt.Errorf("not yet ported: %s", desc) }
