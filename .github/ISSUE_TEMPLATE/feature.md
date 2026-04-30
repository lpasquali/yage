---
name: Feature / Task
about: New capability, provider implementation, refactor, or config change
title: ""
labels: enhancement, claude_cli
assignees: lpasquali
---

## Motivation

<!-- Why does this need to exist? One sentence. -->

## Scope

<!-- What exactly does this issue cover? Keep to one PR worth of work. -->

## Acceptance criteria

- [ ] <!-- criterion 1 -->
- [ ] <!-- criterion 2 -->
- [ ] <!-- criterion 3 -->

## DoD level

<!-- Check ONE. See yage-docs/docs/context/WORKFLOW.md §Definition of Done -->

- [ ] **Level 1 — Full Validation** (orchestrator, provider, config, CSI, kindsync, cluster code)
- [ ] **Level 2 — Test / CI / Config** (test changes, CI config, linter — no runtime code)
- [ ] **Level 3 — Documentation** (yage-docs content, ADRs, runbooks — no Go code)

## Agent assignment

<!-- Which agent should implement this? -->

- [ ] `yage-backend` — orchestrator, provider, config, kindsync
- [ ] `yage-frontend` — xapiri TUI, CLI UX
- [ ] `yage-architect` — ADR, interface contract, technical doc
- [ ] `yage-platform-engineer` — K8s/CAPI/infra, manifest review
- [ ] `yage-po` — backlog, CURRENT_STATE.md, issue management

## Epic

Epic: #<!-- parent epic number, or "none" -->

## Notes

<!-- Implementation hints, references, gotchas. -->
