---
name: route-assessment
description: Recommend the smallest safe workflow route when a work item needs entry, exit, or optional-stage decisions.
metadata:
  version: "1.0.0"
  capability: "darkstar:route-assessment"
---

# Route Assessment

Honor explicit human entry and terminal choices before making recommendations. Classify the requested outcome, then propose only stages likely to change a downstream decision or reduce material risk; do not turn the default workflow into a mandatory maturity model.

Separate required inputs from recommended evidence. When a required input or policy decision is missing, identify the exact blocker and the closest targeted remedy. Record assumptions, risks, confidence, and alternatives without silently changing the route or directly selecting a workflow edge; deterministic gates own edge selection.

Return a structured route proposal and readiness summary suitable for validation. Preserve an assessment-only terminal when that is the requested outcome.
