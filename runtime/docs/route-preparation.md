# Automatic route preparation

DAR-146 is governed by DS-002, DS-005, DS-006, DS-008 and DS-010.

Automatic work preparation evaluates every entry-capable node against the
workflow's authored terminal-capable nodes, required input bindings, and the effective
`routing.assessment` project policy. The configured reasoning provider supplies
outcome/readiness advice for all structurally eligible candidates in a bounded,
read-only attempt. The core rejects unknown, duplicate, incomplete or unready
proposals and chooses the smallest suitable route by node count, then canonical
entry and terminal-set identity. The bounded candidate sets are each declared
single terminal, the workflow defaults, and authored profile terminal sets. A
design-only outcome can therefore end at design instead of delivery.
Validation-only routes cannot be marked suitable merely because they are short:
the advice contract requires the entire requested outcome to be covered.

The immutable assessment is stored inside `run.route_frozen.routeSnapshot` and
returned as `assessment` by run detail. It contains exact work and project
snapshots, workflow document/digest, policy, evidence digests, input answers,
candidate rationale, confidence, assumptions, questions and alternatives. There
is one selected route. Readiness is derived from questions, required inputs and
confirmation reasons; UI state never authorizes a different route. Replay checks
both the stored content digest and deterministic reconstruction. The same input
identity reuses previously recorded advice instead of sampling the provider again.

Evidence may use `artifact:<artifactId>@<version>#<representationId>`. Only exact
stored public/internal, readable text representations admitted by the artifact
resolver enter assessment context. Their bytes and digests are verified. Paths,
URLs, classified content and unresolved references remain uncitable references;
the assessment cannot pretend it inspected them.

Missing information produces a preparation input-required attention item and a
waiting run without provider execution attempts. Answer with a new `run prepare`
command using `--answers-json '{"question-id":"answer"}'` and, for typed authored
run inputs, `--inputs-json '{"input_id":value}'`. A successful replacement
preparation atomically cancels previous preparation-only waits/ready runs, keeping
their history and resolving their attention. Existing executing runs must be
resolved first. A failed assessment command retains an immutable failure result;
replay does not invoke the provider, and retry uses a new idempotency key.

Explicit work routing overrides and named profiles retain their boundaries.
Invalid/prohibited overrides fail with actionable validation errors. Automatic
assessment cannot invent terminal nodes. A selected route extending beyond the
workflow default scope requires confirmation. Medium/low confidence,
unapproved assumptions and consequential nodes require a separate user launch:
`run launch <run-id> --if-match <version> --confirm-assessment <digest>`.
The HTTP equivalent is `POST /api/v1/runs/<id>/start` with an `If-Match` header and
`{"assessmentDigest":"..."}`. Confirmation authorizes only this route launch;
workflow checkpoints, provider permissions and delivery authorization still apply.
Launch rejects changed work, archived projects, changed policy/workflow or changed
evidence availability and records the exact confirmation digest and actor.

Without a configured semantic provider, automatic preparation asks for provider
configuration or an explicit override. It never substitutes a speculative route.
