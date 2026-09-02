---
name: change-inspection
description: Inspect a proposed code or configuration change for correctness, regressions, and acceptance coverage.
metadata:
  version: "1.0.0"
  capability: "darkstar:change-inspection"
---

# Change Inspection

Compare the actual change with its stated outcome, accepted design, and repository invariants. Read the surrounding code and tests needed to understand behavior; do not judge a diff in isolation. Focus on concrete defects, missing cases, unsafe authority expansion, compatibility problems, and claims not supported by evidence.

Keep inspection read-only unless the user separately requests fixes. Rank findings by user or operational impact, cite precise locations, explain the triggering scenario, and distinguish actionable defects from questions or optional improvements. Check that tests exercise meaningful behavior and that rollback or migration assumptions remain valid.

Return findings first, followed by residual risks and a short validation summary. If no actionable defect is found, say so without inventing concerns.
