# Tracker workflow mappings

Mappings have three independent sections: `intake` selects local work and a
pinned workflow; `outbound` describes a named milestone's requested source
effect; `display` groups observed status IDs into board columns. A column may
contain several statuses. Its reverse transition is never inferred.

In **Tickets**, select a project and use the tracker board or mapping settings.
The execution board remains available separately. Source identity, business
status, local activity, last workflow outcome, freshness and source handoff are
distinct facts. Existing work detail, artifacts, review, history and run controls
remain accessible. A board move uses an advertised source action and shows the
configured intake consequence; keyboard action controls use the same endpoint.

## Discover, preview and activate

```text
darkstar tracker mapping discover <project-id> --observation <observation-id>
darkstar tracker mapping show <project-id>
darkstar tracker mapping preview <project-id> --file mapping-request.json
darkstar tracker mapping save <project-id> --file mapping-request.json
darkstar tracker mapping activate <project-id> --file activation-request.json
darkstar tracker mapping board <project-id>
darkstar tracker mapping transition <project-id> --file transition-request.json --key <idempotency-key>
```

The same operations are available at `/api/v1/projects/{projectId}/tracker-mapping`
and its `/discovery`, `/preview`, `/activate` subpaths, plus `/tracker-board` and
`/tracker-board/transition`. The OpenAPI contract records method and request
requirements. Source scope selection continues to use the existing backlog
binding API and source picker.

A save/preview request contains `schemaVersion: 1`, `rules`, and an optional
`observationId`. An optional `event` selects `workflow` (its exact ID, version,
digest) and `milestoneId`. A sample event is only a dry run: it cannot authorize
an effect or satisfy evidence requirements. Activation names `revision` and
`expectedActiveRevision`; zero means no currently active revision.

Use discovery's stable IDs, adapter pin, workflow versions, field metadata and
capabilities. Ambiguous intake, unknown fields, unsupported sprint predicates,
unavailable transitions and configuration drift fail validation. Display-only
unknown statuses remain in the explicit unmapped bucket. Saved revisions and
activation history are retained, and a source switch never retargets existing
work or pending operations.

## Admission and execution

Manual admission retains the exact source observation and selected rule bytes.
Automatic rules are evaluated in the daemon's bounded polling pass, independently
of read-only board requests. An admitted observation, its eligibility cursor and
its mapping/workflow pin commit atomically. Restart and repeated observations
do not duplicate admission. Eligibility must leave and re-enter before a bounded
automatic repair admission; previous execution must first settle.

The supported `require_approval` readiness policy preserves preparation and human
launch confirmation. Automatic admission prepares the pinned workflow but does
not launch an agent through that gate. Preparation is attributed to the daemon,
not fabricated as a user message. A run retains its source and rule snapshot;
later configuration edits cannot replace its workflow, tools or checkpoints.

## Source actions and milestones

Built-in source moves use the native writer and durable operation receipts.
Installed external source adapters advertise their actual support; read-only
adapters report creation and transitions unavailable. They never create a shadow
native ticket. Generated story drafts remain artifacts until approved publication.

Milestones name declared workflow outputs and typed evidence contracts. The core
outbound evaluator requires a daemon evidence validator and exact writer
preflight before returning an intent. A generic run-completed event cannot mean
accepted, deployed or externally done. Durable external dispatch/reconciliation
of milestone intents remains the separate DAR-175 synchronization workstream.
The [executable Jira-style example](../src/core/trackerrules/testdata/jira-workflow.json)
and [rule contract guide](../src/core/trackerrules/README.md) cover type-specific
workflows, separate sprint conditions and distinct handoff evidence. These are
fixtures, not a production Jira adapter.
