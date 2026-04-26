#!/usr/bin/env bash
# migrate-to-yage.sh — align a running kind cluster with yage's
# renamed conventions, so the new yage binary can take over a kind
# cluster previously bootstrapped by the old bootstrap-capi binary.
#
# Today's rename impact (HEAD):
#   • Secret rename:  <ns>/proxmox-bootstrap-capi-manifest → <ns>/proxmox-yage-manifest
#   • Label rewrite:  app.kubernetes.io/managed-by: bootstrap-capi → yage
#
# The kind-side namespace stays as proxmox-bootstrap-system today;
# Phase D will rename it to yage-system. When Phase D ships, this
# script grows a third step (copy Secrets across namespaces, delete
# the old one). Until then, leave the namespace alone.
#
# Usage:
#   ./scripts/migrate-to-yage.sh                # dry-run (default)
#   ./scripts/migrate-to-yage.sh --apply        # actually make changes
#
# Override the namespace if yours differs from the default:
#   YAGE_NS=my-ns ./scripts/migrate-to-yage.sh --apply
#
# The script is idempotent — running it twice in a row is safe; the
# second run is a no-op.

set -euo pipefail

APPLY=0
[[ "${1:-}" == "--apply" ]] && APPLY=1

NS=${YAGE_NS:-proxmox-bootstrap-system}
OLD_SEC=proxmox-bootstrap-capi-manifest
NEW_SEC=proxmox-yage-manifest

ctx=$(kubectl config current-context 2>/dev/null) || {
    echo "❌ kubectl has no current-context. Switch to your kind context first." >&2
    exit 1
}

case "$ctx" in
    kind-*) : ;;  # standard kind context name
    *) echo "⚠ context '$ctx' doesn't look like a kind context (kind-*). Continuing anyway." ;;
esac

if ! kubectl get ns "$NS" >/dev/null 2>&1; then
    echo "❌ namespace '$NS' not found in $ctx — nothing to migrate." >&2
    exit 1
fi

echo "Cluster:   $ctx"
echo "Namespace: $NS"
echo "Mode:      $( ((APPLY)) && echo apply || echo dry-run )"
echo

run() {
    if ((APPLY)); then
        echo "+ $*"
        "$@"
    else
        echo "[dry-run] $*"
    fi
}

# ─────────────────────────────────────────────────────────────────────
# Step 1: Rename the CAPI manifest Secret
# ─────────────────────────────────────────────────────────────────────
echo "▸ Step 1 — Secret rename: $OLD_SEC → $NEW_SEC"

if ! kubectl -n "$NS" get secret "$OLD_SEC" >/dev/null 2>&1; then
    echo "  $OLD_SEC not present in $NS — already migrated or never set."
elif kubectl -n "$NS" get secret "$NEW_SEC" >/dev/null 2>&1; then
    echo "  $NEW_SEC already exists; deleting stale $OLD_SEC."
    run kubectl -n "$NS" delete secret "$OLD_SEC"
else
    echo "  renaming (preserves data + type, drops resourceVersion/uid)."
    if ((APPLY)); then
        kubectl -n "$NS" get secret "$OLD_SEC" -o yaml \
            | sed -E "s/^(  name: )$OLD_SEC$/\\1$NEW_SEC/" \
            | kubectl apply -f -
        kubectl -n "$NS" delete secret "$OLD_SEC"
    else
        echo "[dry-run] kubectl get | sed name | apply ; then delete old"
    fi
fi
echo

# ─────────────────────────────────────────────────────────────────────
# Step 2: Relabel resources still tagged managed-by=bootstrap-capi
# ─────────────────────────────────────────────────────────────────────
echo "▸ Step 2 — Label rewrite: app.kubernetes.io/managed-by: bootstrap-capi → yage"

# Walk all-namespaces; cover the kinds yage actually labels.
KINDS="secrets,configmaps,deployments,statefulsets,services,daemonsets,jobs,cronjobs"
mapfile -t targets < <(
    kubectl get $KINDS -A \
        -l app.kubernetes.io/managed-by=bootstrap-capi \
        -o jsonpath='{range .items[*]}{.kind}|{.metadata.namespace}|{.metadata.name}{"\n"}{end}' \
        2>/dev/null \
    | grep -v '^$' || true
)

if (( ${#targets[@]} == 0 )); then
    echo "  no resources carry the old label."
else
    echo "  found ${#targets[@]} resource(s) to relabel:"
    for t in "${targets[@]}"; do
        IFS='|' read -r kind tns tname <<<"$t"
        run kubectl -n "$tns" label "$kind" "$tname" \
            app.kubernetes.io/managed-by=yage --overwrite
    done
fi
echo

# ─────────────────────────────────────────────────────────────────────
# Done
# ─────────────────────────────────────────────────────────────────────
if ((APPLY)); then
    echo "✅ migration applied."
else
    echo "🟡 dry-run only — no changes made. Re-run with --apply to commit."
fi
