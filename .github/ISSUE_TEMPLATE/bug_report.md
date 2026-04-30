---
name: Bug report
about: Something broken in the bootstrap pipeline, provider, or TUI
title: "bug: "
labels: bug, claude_cli
assignees: lpasquali
---

## What happened

<!-- Describe the incorrect behaviour. One sentence is enough. -->

## What was expected

<!-- What should have happened instead? -->

## Steps to reproduce

1. 
2. 
3. 

## Context

| Field | Value |
|---|---|
| Provider | <!-- proxmox / aws / azure / gcp / hetzner / openstack / vsphere / capd / do / linode / oci / ibmcloud --> |
| Bootstrap phase | <!-- dependency install / identity / kind / clusterctl init / manifest apply / pivot / argocd / plan / xapiri --> |
| Bootstrap mode | <!-- kubeadm / k3s --> |
| Go version | <!-- `go version` output --> |
| yage version / commit | <!-- `git rev-parse --short HEAD` --> |

## Relevant log output

```
<!-- paste logx output or stack trace here -->
```

## Acceptance criteria

- [ ] The described behaviour no longer occurs on the stated path
- [ ] Regression test added or existing test updated to cover this case (cite test name)
