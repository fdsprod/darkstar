---
name: tracer-bullets
description: Plan implementation as thin end-to-end tracer bullets with atomic validation boundaries.
metadata:
  version: "1.0.0"
  capability: "darkstar:tracer-bullets"
---

# Tracer Bullets

Convert an accepted story and design into the smallest sequence of end-to-end implementation points that proves the architecture early. A point should cross the necessary layers to deliver observable behavior or retire a named risk; do not create separate database, backend, frontend, and test phases when a thin vertical slice is possible.

For every point, define its outcome, files or boundaries likely affected, dependencies, acceptance checks, focused tests, rollback considerations, and commit boundary. Keep each point coherent enough for one atomic commit and safe to stop after. Put enabling scaffolding in the first point that consumes it rather than creating unused infrastructure.

Return an ordered plan with explicit validation after every point and an integrated final check. Surface uncertainty that still needs research or design instead of burying it in an implementation step.
