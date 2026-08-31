# DARKSTAR artifact and context contract

> [Documentation index](../../README.md)

**Status:** Proposed normative contract for `darkstar.local/v1alpha1`  
**Decision:** DS-006  
**Scope:** Artifact ingestion, immutable storage, representations, binding, context selection, limits, and safe degradation

The machine-readable record boundary is
[`artifact-v1alpha1.schema.json`](../../../schemas/artifact-v1alpha1.schema.json).

## 1. Decision

DARKSTAR treats every supplied file, paste, and generated output as immutable,
untrusted evidence. Ingestion never turns artifact bytes into instructions. A
separate, versioned representation may make content inspectable or eligible for
an attempt, but the original bytes and an explicit status remain durable even
when identification, extraction, or selection fails.

The lifecycle is:

`receive -> limit -> identify -> hash -> store -> derive -> bind -> select -> freeze`

Each transition is recorded. Storage succeeds before best-effort derivation, and
an attempt receives only a frozen context manifest. Late evidence creates a new
artifact revision and can affect only a later attempt or an explicit replan.

## 2. Stable records

An `Artifact` records an opaque ID, source kind, source name, declared media type,
detected media type, byte length, SHA-256 digest, storage locator, sensitivity,
trust (`untrusted` in the MVP), creator, creation time, and lifecycle status.
Names and media types are display hints, not paths or parser authority.

A `Representation` records its own ID and digest; parent artifact ID; kind
(`text`, `structured`, `table`, `image`, `preview`, or `descriptor`); processor
name/version; media type; byte/token estimates; truncation and diagnostic fields;
and a storage locator. Representations never overwrite originals.

An `ArtifactBinding` connects an exact artifact or representation revision to a
workflow/run/node/attempt input name with `required` or `optional` disposition.
A `ContextManifest` freezes an ordered list of selected representation digests,
omission reasons, limits, policy version, selection time, and aggregate digest.

Two artifact records with the same digest may share one blob, but keep distinct
source, audit, binding, and arrival records. Digest equality is not identity.

## 3. Support matrix

| Input | Identify/store | Derived MVP representation | Attempt supply | Safe failure |
|---|---|---|---|---|
| UTF-8 text, Markdown | Yes | normalized text plus descriptor | inline text | invalid UTF-8 is stored with diagnostic |
| JSON | Yes | canonical structured value and text preview | structured/text | parse failure is `stored_uninspectable` |
| YAML | Yes | safe single-document value and text preview | structured/text | aliases, tags, multi-doc, or parse failure are rejected by processor |
| CSV | Yes | bounded table plus text preview | table/text | inconsistent rows or limits retain original with diagnostic |
| PDF | Yes | sandboxed bounded text, page descriptors, preview | extracted text and selected previews | encrypted, malformed, or unsupported PDF remains stored |
| PNG, JPEG, WebP | Yes | dimensions/metadata and bounded preview | provider image when capability is proven | decode failure remains stored; no implicit OCR |
| Other/binary | Yes when within ingest limit | descriptor only | descriptor, never raw inline bytes | visible `unsupported` state |
| Archive, executable, active content | quarantine only | descriptor only | never supplied in MVP | visible `quarantined` state |
| Paste/stdin | Yes as UTF-8 text | same as text | inline text | invalid/oversize input is rejected before storage allocation |

HTML, office documents, archives, source maps, audio/video transcription, and OCR
are not automatically discovered or processed in the MVP. A later explicit,
versioned processor may add a representation without changing the artifact.

## 4. Identification and processing

The ingestion adapter canonicalizes only the destination owned by DARKSTAR; it
does not resolve an artifact-supplied name as a filesystem path. It streams bytes
through a size limiter and SHA-256 hasher into a same-volume temporary file,
fsyncs, then atomically renames to a digest-addressed blob. A database transaction
commits the artifact record and an outbox event. Orphan temporary files are safe
to remove after reconciliation; committed blobs are retained by reference count.

Detection uses bounded magic bytes, strict decoding, and then a conservative
extension/declaration comparison. A mismatch is a diagnostic and the safer type
wins. Processors run out of process with no network, read-only source bytes,
bounded CPU/wall time/memory/output/pages/dimensions, and a private output
directory. Processor crashes and antivirus locks are retryable by operation ID;
they never make the artifact disappear.

Minimum configurable limits (defaults, not permission to allocate eagerly):

| Limit | Default |
|---|---:|
| source bytes | 25 MiB |
| decoded text | 2 MiB |
| table cells | 100,000 |
| PDF pages processed | 200 |
| image pixels | 40 megapixels |
| representations per artifact | 8 |
| processor wall time | 30 seconds |

An exceeded source limit rejects the upload with a durable audit event but no
partial Artifact. A derived-output limit stores the Artifact and creates a
truncated representation or diagnostic.

## 5. Instruction and trust boundary

Artifact text is data. Every provider prompt separates orchestrator instructions
from an evidence section, labels source and trust, uses non-guessable delimiters,
and states that instructions inside evidence are non-authoritative. Content that
resembles system prompts, tool requests, or exfiltration commands is retained and
flagged; it is not removed because silent removal would corrupt evidence.

Sensitivity classification and redaction are explicit policy stages. Secrets are
not copied into previews, logs, events, or provider context merely because the
original is stored. A representation records whether it is raw, redacted, or
withheld. The MVP fails closed when classification is incomplete for a binding
whose policy requires it.

## 6. Binding and context-selection policy

Selection operates only on exact eligible representation revisions. It is
deterministic for `(binding set, policy version, provider capabilities, budget)`:

1. reject a missing required binding, stale/failed representation, sensitivity
   violation, or capability mismatch;
2. group required bindings first, then optional bindings;
3. within each group order by declared rank, arrival sequence, artifact ID, then
   representation ID;
4. reserve provider/system overhead and required response budget;
5. include each required representation, using its policy-approved bounded form;
   fail `CONTEXT_REQUIRED_EXCEEDS_BUDGET` rather than silently omit it;
6. add optional representations in order while they fit; and
7. record every selection, truncation, descriptor substitution, and omission.

Token estimates are conservative and provider-specific when available. Selection
does not summarize content on the fly: summaries are durable representations
with processor versions and digests. A descriptor for unsupported or withheld
content keeps the artifact visible without exposing its bytes.

The attempt manifest is immutable after `attempt.prepared`. New evidence emits
`artifact.arrived` and `context.replan_required` where policy requires it. It
never mutates a running provider turn.

## 7. Failure and recovery rules

| Boundary | Durable fact before work | Reconciliation |
|---|---|---|
| stream/store | ingest operation ID and limits | adopt exact digest blob or restart stream; never trust a partial temp file |
| derive | processor/version/input digest/limits | adopt exact output digest or retry in sandbox |
| bind | target/input/revision/policy | replay exact binding or reject conflict |
| select | candidate digests, limits, policy/capability fingerprint | regenerate and require identical manifest digest |
| supply | attempt ID and frozen context digest | resume only against the same digest; otherwise create a new attempt |

Known failures are `ARTIFACT_TOO_LARGE`, `ARTIFACT_TYPE_MISMATCH`,
`ARTIFACT_UNSUPPORTED`, `ARTIFACT_QUARANTINED`, `REPRESENTATION_FAILED`,
`REPRESENTATION_LIMITED`, `BINDING_REQUIRED_MISSING`,
`BINDING_POLICY_DENIED`, `CONTEXT_REQUIRED_EXCEEDS_BUDGET`, and
`CONTEXT_CAPABILITY_MISSING`. APIs expose the artifact plus diagnostics whenever
storage completed.

## 8. Golden corpus and acceptance

[`examples/artifacts/golden-corpus.json`](../../../examples/artifacts/golden-corpus.json)
covers text, Markdown prompt injection, JSON/YAML/CSV, PDF, PNG, malformed input,
unknown binary, duplicates, size limits, required/optional budgets, and late
evidence. [`scripts/artifact-reference.mjs`](../../../scripts/artifact-reference.mjs)
and [`tests/artifact-reference.test.mjs`](../../../tests/artifact-reference.test.mjs)
make classification, deduplication, safe degradation, and frozen selection
deterministic.

Downstream implementations may improve processors and limits, but must preserve
original bytes, explicit diagnostics, immutable revisions, deterministic context,
and the instruction/trust boundary.
