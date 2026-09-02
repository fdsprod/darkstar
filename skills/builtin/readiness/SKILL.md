---
name: readiness
description: Assess whether work is ready for a named stage using supplied requirements, evidence, and policy rubrics.
metadata:
  version: "1.0.0"
  capability: "darkstar:readiness"
---

# Readiness

Evaluate the requested stage against the supplied, versioned rubric. Keep required inputs, recommended evidence, policy gates, and unresolved risks distinct. Do not manufacture evidence or convert a recommendation into a hard blocker.

For each criterion, report the observed evidence, disposition, and confidence. Identify contradictions and missing decisions explicitly. If readiness is insufficient, recommend the nearest upstream artifact or decision to revise instead of restarting the full workflow.

Produce a structured assessment with criterion results, blockers, warnings, assumptions, an evidence summary, and a numeric score only when the rubric defines its calculation. A separate deterministic gate applies thresholds.
