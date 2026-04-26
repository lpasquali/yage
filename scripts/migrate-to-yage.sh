#!/usr/bin/env bash
# migrate-to-yage.sh — align a running kind cluster with yage's
# renamed conventions, so the new yage binary can take over a kind
# cluster previously bootstrapped by the old bootstrap-capi binary.
#
# Today's rename impact (HEAD):
#   • Secret rename:  <old-ns>/proxmox-bootstrap-capi-manifest → <old-ns>/proxmox-yage-manifest
#   • Label rewrite:  app.kubernetes.io/managed-by: bootstrap-capi → yage
#   • Namespace move: proxmox-bootstrap-system → yage-system
#                     (every Secret + ConfigMap copied across, then
#                      the old namespace is deleted)
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

OLD_NS=${YAGE_OLD_NS:-proxmox-bootstrap-system}
NEW_NS=${YAGE_NEW_NS:-yage-system}
NS=$OLD_NS    # step 1 + 2 still operate on the old namespace
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

if ! kubectl get ns "$OLD_NS" >/dev/null 2>&1; then
    if kubectl get ns "$NEW_NS" >/dev/null 2>&1; then
        echo "ℹ namespace '$OLD_NS' is already gone and '$NEW_NS' exists — nothing to migrate."
        exit 0
    fi
    echo "❌ namespace '$OLD_NS' not found in $ctx — nothing to migrate." >&2
    exit 1
fi

echo "Cluster:    $ctx"
echo "From ns:    $OLD_NS"
echo "To ns:      $NEW_NS"
echo "Mode:       $( ((APPLY)) && echo apply || echo dry-run )"
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
# Step 3: Move the namespace
# ─────────────────────────────────────────────────────────────────────
echo "▸ Step 3 — Namespace move: $OLD_NS → $NEW_NS"

if ! kubectl get ns "$NEW_NS" >/dev/null 2>&1; then
    run kubectl create namespace "$NEW_NS"
fi

# Mirror every Secret + ConfigMap from OLD_NS into NEW_NS. We use
# `kubectl get -o yaml | sed namespace | kubectl apply` so labels +
# annotations + data + immutable type round-trip cleanly. Skip
# server-managed default service-account-token Secrets — those are
# regenerated automatically in the new namespace.
copy_resources() {
    local kind=$1
    mapfile -t names < <(
        kubectl -n "$OLD_NS" get "$kind" \
            -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' \
            2>/dev/null \
        | grep -v '^$' \
        | grep -Ev '^default-token-|^kube-root-ca\.crt$|^default$' \
        || true
    )
    if (( ${#names[@]} == 0 )); then
        echo "  (no $kind to copy)"
        return
    fi
    for n in "${names[@]}"; do
        if ((APPLY)); then
            echo "+ copy $kind/$n"
            kubectl -n "$OLD_NS" get "$kind" "$n" -o yaml \
                | sed -E "s/^(  namespace: )$OLD_NS\$/\\1$NEW_NS/" \
                | sed -E '/^  resourceVersion:/d; /^  uid:/d; /^  creationTimestamp:/d' \
                | kubectl apply -f -
        else
            echo "[dry-run] copy $kind/$n from $OLD_NS to $NEW_NS"
        fi
    done
}

copy_resources secret
copy_resources configmap

# Once everything is mirrored, delete the old namespace. This is
# the destructive step; only do it if APPLY and only after both
# copies above succeeded (set -e bails before reaching this line
# otherwise).
if ((APPLY)); then
    echo "+ delete namespace $OLD_NS"
    kubectl delete namespace "$OLD_NS"
else
    echo "[dry-run] delete namespace $OLD_NS (after the copies above)"
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
