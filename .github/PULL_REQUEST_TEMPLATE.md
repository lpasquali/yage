## Summary

<!-- 1-3 sentences: what and why -->

Closes #<!-- issue number -->
Epic: #<!-- parent epic number, if applicable -->

## DoD Level

<!-- Check ONE. See yage-docs/docs/context/WORKFLOW.md §Definition of Done -->

- [ ] **Level 1 — Full Validation** (orchestrator, provider, config, CSI, kindsync, cluster code)
- [ ] **Level 2 — Test / CI / Config** (test changes, CI config, linter — no runtime code)
- [ ] **Level 3 — Documentation** (yage-docs content, ADRs, runbooks — no Go code)

## Level 1 Checklist

<!-- Skip if Level 2 or 3. All boxes must be checked. -->

- [ ] `make build` passes
- [ ] `go test ./...` passes
- [ ] `govulncheck ./...` — no new findings (if `go.mod` changed; skip otherwise)
- [ ] Manually walked affected flow **or** change fully covered by existing tests (cite test name)
- [ ] Breaking-change audit: Secret schemas, env var renames, CAPI CRD fields, config namespacing

## Level 2 Checklist

<!-- Skip if Level 1 or 3. -->

- [ ] Full test suite passes
- [ ] Coverage not degraded
- [ ] No unintended CI side effects

## Level 3 Checklist

<!-- Skip if Level 1 or 2. -->

- [ ] `mkdocs build --strict` passes
- [ ] No broken internal links

## Audit Checks

<!-- Required when trigger conditions are met. See WORKFLOW.md §Audit Triggers. -->
<!-- Write "No triggers fired." if none apply. -->

| Check | Result | Evidence |
|---|---|---|
| `govulncheck ./...` (go.mod changed) | PASS / FAIL / N/A | <!-- paste summary or "no new findings" --> |
| PE review (Secret schema / CAPI YAML) | PASS / FAIL / N/A | <!-- link to pe-review doc or comment --> |
| Supply-chain (workflow change) | PASS / FAIL / N/A | <!-- note or N/A --> |

## Acceptance Criteria Evidence

<!-- For each acceptance criterion from the issue, tick and attach evidence. CI green counts. -->

- [ ] <!-- criterion 1 — evidence: ... -->
- [ ] <!-- criterion 2 — evidence: ... -->

## Breaking Changes

<!-- Describe any backward-incompatible changes. If none, write "None." -->

## Notes for Reviewer

<!-- Edge cases, decisions made, open questions. -->
