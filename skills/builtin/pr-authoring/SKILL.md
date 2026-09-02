---
name: pr-authoring
description: Author an evidence-backed pull request title and body for a validated change set.
metadata:
  version: "1.0.0"
  capability: "darkstar:pr-authoring"
---

# PR Authoring

Derive the pull request narrative from the accepted work item, actual diff and commits, and recorded validation. Describe what changed and why at reviewer altitude, including user-visible behavior, important design choices, migration or compatibility notes, risk, rollback, and follow-up work.

Report tests and checks exactly as observed; never claim a check passed because it was planned. Preserve repository templates and required checklists when supplied. Link relevant artifacts and work items without exposing secrets, raw sensitive evidence, or internal provider prompts.

Produce a concise title and structured body ready for the delivery connector. Authoring content does not authorize pushing a branch, creating a pull request, or changing external state; those remain separate policy-controlled actions.
