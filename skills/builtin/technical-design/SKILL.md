---
name: technical-design
description: Design how accepted requirements integrate with an existing system before implementation begins.
metadata:
  version: "1.0.0"
  capability: "darkstar:technical-design"
---

# Technical Design

Ground the design in accepted requirements, repository architecture, and research evidence. Identify the existing boundaries and invariants first, then decide the smallest coherent change. Keep product intent separate from implementation choices and call out any unresolved requirement that prevents a safe decision.

Cover affected components, interfaces, data shapes and ownership, execution flow, failure and recovery behavior, compatibility or migration, security and privacy, observability, validation strategy, deployment, and rollback. Make invalid states and authority boundaries explicit. Discuss real alternatives and why the selected design fits the current system; avoid speculative abstraction.

Produce an implementation-ready design with decisions, risks, open questions, and traceability to requirements. Do not implement the change or claim validation that has not run.
