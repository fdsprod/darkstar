---
name: artifact-reconciliation
description: Reconcile new evidence or upstream revisions with existing artifacts and identify targeted downstream impact.
metadata:
  version: "1.0.0"
  capability: "darkstar:artifact-reconciliation"
---

# Artifact Reconciliation

Compare the new evidence or upstream revision with the current accepted artifact and its recorded dependencies. Separate additive context, contradictions, superseded decisions, and irrelevant changes. Preserve unaffected material and provenance; do not silently merge incompatible claims.

Identify which decisions changed, why, and which descendant artifacts or implementation points actually depend on them. Recommend the closest targeted revision boundary, and distinguish refresh, revise, replan, and no-impact outcomes. Active attempt manifests remain immutable; late evidence can only affect a new attempt or a recorded future plan.

Return a reconciliation result with evidence references, changed decisions, preserved decisions, conflicts, impacted descendants, and the recommended action. State uncertainty when dependency evidence is incomplete.
