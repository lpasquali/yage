// Package kubectlx will host thin wrappers around kubectl/clusterctl that
// the rest of the bootstrap calls repeatedly (apply manifests, get
// resources, wait for endpoints, ...). Stub only.
//
// Bash source map:
//   - _resolve_bootstrap_kubectl_context                 ~L821-839
//   - wait_for_service_endpoint                          ~L2059-2070
//   - apply_workload_cluster_manifest_to_management_cluster ~L2075-2155
//   - warn_regenerated_capi_manifest_immutable_risk      ~L2604-2613
package kubectlx

import "fmt"

func todo(desc string) error { return fmt.Errorf("not yet ported: %s", desc) }
