# Workflow templates, prompts, and assessment

The **Templates** tab manages reusable artifact templates and prompt definitions.
The library belongs to the local daemon and is shared across its projects. A
template defines the document shape and required headings; a prompt defines the
scoped task and conditional instruction sections. They can be linked independently.

## Authoring and versions

Each library item has an editable draft and immutable published versions. Saving
and publishing require the current draft revision, so another editor's changes
cannot be silently overwritten. Authors can duplicate an item, compare versions,
copy a historical version into the draft, and archive or restore an item.
Archiving preserves published versions and existing workflow references.

A workflow links the exact item ID, semantic version, and content digest. Editing
a library draft or publishing a newer version does not update existing links.
Authors explicitly choose another version in the workflow inspector. The Used by
view derives references from saved workflow drafts and published definitions.
Unsaved editor changes appear there only after the workflow draft is saved.

In `v1alpha3`, reasoning and implementation nodes may carry a `prompt` reference.
Artifact templates are run-input resources with kind `template_reference`; an
output's `artifact.templateInput` names its template binding. Existing inline
instructions and inline template resources remain supported. Additional node
instructions augment the linked prompt.

## Conditional prompt builder

A prompt contains base instructions and ordered sections. Each section applies
always, when a named input is linked, when it is absent, or during revision.
The builder selects sections deterministically from the node's declared bindings;
it does not ask another model to generate instructions. Preview shows the assembled
instructions, included and excluded sections with reasons, and an approximate
instruction-token count. The estimate excludes runtime input contents.

Every supplied stage offers optional `open_items` and `deferred_work` links:

| Connection | Behavior |
|---|---|
| Not linked | Include absence guidance; preserve new findings without inventing a register. |
| Linked with an empty collection | Include linked guidance; there are no existing supplied entries. |
| Linked with entries | Consider relevant entries, preserve their identities, and propose changes with evidence. |
| Linked but unavailable | Fail preparation rather than quietly treating it as absent. |

These rules also support custom named conditional inputs. Input content is evidence
and cannot change permissions or orchestration. Each declared input is delivered
once. Only the selected node's declared inputs enter its execution context.

The run freezes prompt versions and resolved template content. For journal-backed
tracking inputs, the daemon freezes the selected open or deferred entries per node
visit. Retries and artifact revisions reuse that selection; a later visit can
observe updated entries. Revisions retain their exact template and prompt and
submit only the output requested by the revision contract.

Stage `findings` outputs preserve blockers and proposed additions, updates, or
resolutions. Linked-prompt tasks cannot directly mutate the journal. Proposals
remain pending human review: approving a document does not automatically apply
them. Accepted journal changes currently require a separate explicit action using
the existing journal controls. Automatic reconciliation is not implemented.

## Stage presets and human review

The workflow editor can insert these independently editable presets:

| Preset | Source prompt |
|---|---|
| Assessment | `assess.md` |
| Questions | `1-questions.md` |
| Research | `2-research.md` |
| Product Design | `3-design.md` |
| Technical Design | System-contract concerns extracted from `3-design.md` |
| Implementation Plan | `4-plan.md` |
| Implementation | `5-implement.md` and `5a-auto-implement.md` |
| Validation Review | `6-validate.md` |
| PR Summary | `7-pr.md` |
| Change Impact Assessment | Revision concerns from `3-design.md` and `4-plan.md` |

The source documents were adapted as reference material. Instructions that would
delegate scheduling, approvals, Git operations, or delivery to an agent were not
carried into execution prompts. Each preset links a published prompt and document
template. Optional tracking ports are offered without creating phantom bindings.
Authors connect stage outputs to the next stage's declared inputs in the canvas.

Product Design, Technical Design, and Plan presets require approval checkpoints.
They use the existing artifact review workflow: version-bound feedback, revisions,
diffs, and human decisions. Model text cannot approve an artifact. A Validation
Review provides reasoning evidence; required executable checks still belong to
daemon-controlled validation nodes. PR Summary creates text, not a pull request.

## Assessment Router

The Assessment Router authoring action selects five existing destination nodes:
Questions, Research, Product Design, Technical Design, and Plan. It inserts one
scoped assessment node followed by four deterministic gates. These are visible,
ordinary workflow nodes, not a new model-controlled executor.

Assessment commits readiness evidence for questions, research, product behavior,
and system behavior. Gates select the earliest missing stage. Supplied product
and technical designs can be skipped only with explicit human approval
attestations in frozen run inputs; the default is that approval is absent.
Readiness claimed by the assessment cannot manufacture those attestations.
When all prerequisites are ready and approved, the route enters Plan.

The authoring action sets the default entry, preserves terminal boundaries, merges
alternative incoming paths with one-joins, and requires approval on the three
design/planning destinations. It rejects conflicting identifiers, duplicate
destinations, incompatible all-joins, and invalid resulting graphs.

This action chooses initial entry; it does not generate feedback loops. Authors
can add bounded paths for new questions or feasibility changes using existing
workflow transitions and gates. Later evidence must be committed before a gate
routes it. Human approval remains bound to the actual artifact revision.

## API and CLI

The [Adaptive Planning example](../../examples/workflows/v1alpha3/adaptive-planning.json)
combines these stages with bounded feedback paths and human design/plan reviews.
Plan declares `darkstar:tracer-bullets`; the bundled instructions are loaded for
planning attempts and retained for revisions. After plan approval, its default
route prepares a worktree, implements the plan, prepares PR text, commits, pushes,
and creates a pull request. It intentionally has no separate validation stage.
The project supplies its worktree base; delivery uses `origin` and the remote's
default target branch. Import the example with `darkstar workflow draft-create`
to review and configure it before publishing.

The versioned API exposes `/api/v1/content-library` and its draft, publish,
duplicate, archive, restore, and preview operations. CLI equivalents are under
`darkstar content`. The authoring-only assessment transformation is available as
`darkstar workflow assessment-router <request.json>` and
`POST /api/v1/workflows/patterns/assessment-router`; it returns a candidate
document and does not publish or start execution.
`darkstar workflow content-usage <content-id>` lists saved workflow references.

See [execution semantics](../architecture/workflow/execution-semantics.md),
[artifact contracts](../architecture/artifacts/ARTIFACT_AND_CONTEXT_CONTRACT.md),
and [approval contracts](../architecture/security/APPROVAL_AND_PERMISSION_MODEL.md).
