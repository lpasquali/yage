// Package ciliumx will host Cilium install / LB-IPAM pool / HelmChartProxy
// helpers. Stub only.
//
// Bash source map:
//   - cilium_needs_kube_proxy_replacement       ~L1106-1128
//   - default_cilium_lb_ipam_pool_cidr_from_nodes ~L1141-1158
//   - append_cilium_lb_ipam_pool_manifest       ~L1160-1214
//   - apply_workload_cilium_helmchartproxy etc. (later in the script)
package ciliumx

import "fmt"

func todo(desc string) error { return fmt.Errorf("not yet ported: %s", desc) }
