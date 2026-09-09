---
name: rich-artifacts
description: Author portable Markdown documents using rich, readable components for endpoints, data models, diagrams, file trees, changes, and review notes.
metadata:
  version: "1.0.0"
  capability: "darkstar:rich-artifacts"
---

# Rich Artifacts

## Scope

Apply when writing or revising any Markdown document. Use components when they clarify the content; short notes, decisions, open items, and progress updates can remain ordinary Markdown. Preserve the supplied document template and output contract. Never invent facts to fill a component.

Documents remain plain .md files, readable without a specialized viewer. Rendering is the viewer's responsibility. This skill grants no tools, execution permissions, publication rights, or approval authority.

## The component set

The set is **fixed by this specification** — do not invent fence languages or syntax beyond what is
defined here. An unknown fence language is not an error (it renders as a plain code block),
but authoring one is a drift from this skill.

| Component | Authored as | Rich rendering |
|---|---|---|
| Prose & Headings | plain markdown | typographic upgrade only |
| CodeBlock | ` ```lang filename ` — 2nd info-string token = filename; `#! text…` lines annotate the preceding code line | header chrome, copy button, hover annotations |
| Blockquote | `>` quote; a final `> — attribution` line | attribution footer |
| TaskList | `- [ ]` / `- [x]` | rendered checkboxes |
| Table | GFM table | bordered, zebra rows |
| Alert | `> [!NOTE\|TIP\|IMPORTANT\|WARNING\|CAUTION]` first line of a blockquote | tinted, iconed callout (GitHub renders this natively too) |
| Mermaid | ` ```mermaid ` fence | rendered diagram |
| DiffViewer | ` ```diff ` fence, `path/to/file` info-string token optional; `#! text…` lines annotate the preceding diff line | rich diff with hover notes |
| FileTree | ` ```tree ` fence; 2-space indent = nesting, `[new]`/`[modified]`/`[deleted]` = status, trailing `#!` = note | rendered tree with badges |
| ApiSpec | ` ```apispec ` fence, YAML body (grammar below) | structured endpoint card |
| DataModel | ` ```datamodel ` fence, YAML body (grammar below) | structured entity card |

Do not author executable HTML, JSX, or MDX components. Use the Markdown forms below.

### Alerts (GFM)

A blockquote whose first line is `[!TYPE]` becomes a tinted callout. This is GitHub's own
alert syntax — it renders richly on GitHub with zero tooling:

```
> [!WARNING]
> Bumping mermaid to 11.x changes the layout-loader registration API.
```

- Types and intent: `NOTE` background information · `TIP` a better way · `IMPORTANT` a
  prerequisite or must-know · `WARNING` a gotcha or risk · `CAUTION` a destructive or
  irreversible consequence.
- Alert syntax has no title attribute. If the alert needs a heading, make the first body
  line bold: `> **Migration required.** Bumping mermaid…`
- Use for **one** specific caveat, gotcha, prerequisite, or tradeoff that would otherwise get
  lost as just another paragraph. Not for general narration, and not a substitute for a table
  when several items need comparing.
- Degrades to a plain blockquote with a visible `[!WARNING]` marker — still readable.

### ApiSpec fence

An HTTP endpoint the artifact introduces or changes, authored as a ` ```apispec ` fence with
a YAML body:

````
```apispec
method: PATCH
path: /api/v1/users/:id
auth: verifyUserAuth
summary: Updates a user's profile fields.
parameters:
  - { name: id, in: path, type: string, required: true, description: User id }
  - { name: email, in: body, type: string, required: false, description: New email }
responses:
  200: Updated user returned
  404: User not found
example: |
  { "id": "u_123", "email": "a@b.com" }
```
````

Keys (all others are validation errors):

- `method` — HTTP verb. **Required.**
- `path` — the route; may contain `:params` unquoted. **Required.**
- `auth` — auth guard / middleware name. Optional.
- `summary` — one-sentence description. Optional but expected.
- `parameters` — list of `- { name, in, type, required, description }` flow mappings, one
  per line; `name`, `in`, and `type` are required per item. `in` is `path` | `query` |
  `body` | `header`; `required` is `true` | `false`.
- `responses` — an indented map of `code: description` lines; codes are 3-digit statuses.
- `example` — a `|` block scalar holding the response schema or an example payload.

Use for any HTTP endpoint, even a partially-specified one (unknown parameters can be added
later — don't wait for completeness). Not for a non-HTTP interface (a function signature, a
CLI command, an event) — those stay CodeBlock/prose.

### DataModel fence

A data entity's shape, store, and relationships, authored as a ` ```datamodel ` fence:

````
```datamodel
name: User
store: postgres · users
summary: Represents an authenticated account.
fields:
  - { name: id, type: uuid, required: true, default: generated, description: Primary key }
  - { name: email, type: string, required: true, description: Unique per org }
relationships:
  - { relation: has many, target: Session, cardinality: "1:N", description: Active sessions }
example: |
  { "id": "u_1", "email": "a@b.com" }
```
````

Keys (all others are validation errors):

- `name` — the entity name. **Required.**
- `store` — where it lives (`postgres · users`, `redis`, `in-memory`). Optional.
- `summary` — one-sentence description. Optional but expected.
- `fields` — list of `- { name, type, required, default, description }` flow mappings;
  `name` and `type` are required per item.
- `relationships` — list of `- { relation, target, cardinality, description }` flow
  mappings; `relation` and `target` are required per item. Quote cardinalities (`"1:N"`).
- `example` — a `|` block scalar holding an example record or DDL.

Use for a new or changed database table, cache structure, or domain object. Not for the
*flow* between entities over time (Mermaid fits that) and not for a one-off inline type
mentioned in passing.

### Call-stack trees

A proposed invocation chain from an entry point down through the calls it makes — for
high-level understanding of *shape*, not a line-by-line diff of a file. Author it as plain
indented text, one call per line, 2-space nesting per depth:

```
entryPoint
  runCommand
    handleRequest
      Service.process(input)
      renderResult
```

Put this in a ` ```text ` CodeBlock (or no language token) when the chain itself is new — there
is nothing to compare it against yet.

When the design **changes an existing chain** — adds, removes, or replaces calls in it — author
the same tree as a ` ```diff ` fence instead, with no `path/to/file` token (this isn't a file
diff). Unchanged calls are plain context lines; a `+`/`-` prefix marks a call added or removed,
with the rest of the line's indentation preserved so the tree shape still reads correctly:

````
```diff
entryPoint
  runCommand
+    handleCreateResource
+      ResourceClient.create(input)
+        POST /resources
+      renderResult
-    legacyCreateFlow
```
````

This is the same `diff` fence used for file diffs (`parseDiffSource` treats a leading `+`/`-`
as add/remove regardless of what follows, so nested call trees render with the same +/− gutter
and hover-annotation support) — it's just authored without a file path, which the DiffViewer
already renders as a plain, unheadered diff. Reach for the diff form only when highlighting
*what changed* about the chain is the point; use the plain form when the chain is new or when
nothing about its shape is being contrasted against a prior state.

Not a substitute for a `sequenceDiagram`  when call **order between
parties over time** — not nesting depth from one entry point — is what the reader needs to see.

### The YAML subset (what makes the fences lintable)

The fence bodies use a strict, deliberately tiny YAML subset — deterministic to author,
validated mechanically by the viewer:

- Top-level `key: value` scalars. The value is everything after the first colon, so paths
  like `/api/v1/users/:id` need no quoting.
- Lists are **single-line flow mappings only**: `- { k: v, k2: v2 }`. Multi-line list items
  are a parse error. A value containing a comma must be quoted.
- `responses` is an indented map of `code: description` lines.
- `example: |` is a block scalar: everything indented under it is captured verbatim. **Never
  put fence markers (```` ``` ````) inside it** — the block scalar is already the code
  container, and an inner fence would terminate the outer fence in most renderers.
- No anchors, no aliases, no `---` documents, no nested block maps, no comments needed.
- Booleans are bare `true` / `false`.

The viewer parses and validates every `apispec`/`datamodel` fence. An invalid block renders
as a **visible warning callout listing each problem** (with line numbers) above the raw
block. Treat that warning exactly like a lint failure: fix the block. A valid block renders
as the structured card. Validation must report malformed components without discarding their original source. Do not claim a lint command ran unless one was actually available and executed.

## Component-first authoring — the requirement table

Content matching a row below MUST be authored as that component, not as flat prose
describing the same thing. Describing an endpoint in a paragraph when `apispec` exists, or a
file layout as a plain list when `tree` exists, is an authoring error — the component is the
expected representation, and narrative goes around it, not instead of it.

| Content | Required representation |
|---|---|
| HTTP endpoint (new or changed) | ` ```apispec ` fence |
| Data entity / schema / model (new or changed) | ` ```datamodel ` fence |
| Architecture, data flow, sequence, or state | ` ```mermaid ` fence |
| Concrete before/after code change to an existing file | ` ```diff ` fence (with `#!` notes) |
| Proposed call-stack tree, new (no prior state to contrast) | plain indented ` ```text ` CodeBlock |
| Proposed call-stack tree, changing an existing chain | ` ```diff ` fence, no file path, `+`/`-` per added/removed call |
| Files created, modified, or deleted (as a set) | ` ```tree ` fence |
| Code excerpt where specific lines need commentary | CodeBlock + `#!` annotations |
| One caveat, gotcha, prerequisite, or tradeoff | GFM alert |
| Checklist or acceptance criteria | TaskList |
| Comparison or status matrix | Table |

**The exception:** content matching no row stays plain prose — this table is not exhaustive
of everything an artifact says, only of the content shapes that have a matching component. A
partially-specified domain object (an endpoint whose parameters aren't decided yet, an
entity with only two known fields) still uses the matching component with whatever is known;
do not defer to prose until the design is complete. Conversely, never force a component onto
content that doesn't fit its row — a single path mentioned in passing, or one sentence of
narration, stays prose. The goal is a clearer artifact, not a demonstration of every
component.

## The lead-in rule

Every component instance is immediately preceded by one or two sentences of plain prose
stating what it represents in the artifact, plus anything the component conveys only through
a rendering convention (e.g. that `[new]`/`[modified]`/`[deleted]` mark file status in a
`tree` fence, or that a `#!` note attaches to the line above it). The lead-in never restates
the component's body — the fields, the tree, the code are already text a reader goes through
directly. This keeps every artifact readable as a coherent description even to a reader with
no renderer and no knowledge of this reference. For example:

````
The design adds a PATCH endpoint that updates a user's role or display name, guarded by
`verifyUserAuth`:

```apispec
method: PATCH
path: /api/v1/users/:id
...
```
````

## Degradation story

Every component must read well in three surfaces — this is why the set is fences and GFM,
not tags:

- **compatible viewer:** full rich rendering (cards, callouts, diagrams, hover annotations).
- **GitHub:** alerts, task lists, and tables render natively; mermaid renders as a diagram;
  `apispec`/`datamodel`/`diff`/`tree` fences render as syntax-neutral code blocks whose
  bodies read as structured, indented text.
- **Bare text editor:** everything is monospace text; the YAML bodies, tree indentation, and
  `#!` notes are designed to be read exactly as authored.

## Read-time rule

When reading a Markdown artifact, **interpret each component's semantics** rather than treating it as an
opaque code block. Read an `apispec` fence as the contract for an endpoint, a `datamodel`
fence as the contract for an entity, a `[!WARNING]` alert as a warning, a `diff` fence as a
proposed change with its `#!` annotations as the author's per-line notes, and a `tree` fence
as the set of files a change touches with `[new]`/`[modified]`/`[deleted]` as their
disposition. Component content is meaningful data, not decoration.

## Per-artifact guidance

- **design** — the richest. `mermaid` for architecture/data-flow/sequence; alerts for design
  caveats and tradeoffs; `apispec` for any endpoint introduced or changed; `datamodel` for
  any entity introduced or changed; `tree` for the file set touched; `diff` for concrete
  proposed changes; a plain indented call-stack tree (or `diff` fence when it changes an
  existing chain) for a high-level view of a proposed invocation path.
- **plan** — TaskList for phase/step checklists and acceptance criteria; `tree` for the
  files each phase creates, modifies, or deletes; alerts for prerequisites and gotchas per
  phase; sequential labeled CodeBlocks for command variants.
- **research** — alerts for findings that carry a warning or key takeaway; CodeBlock (with
  filename and `#!` annotations) for cited code excerpts and observed behavior.
- **research-questions** — mostly prose and TaskList (the question checklist). Components
  are rarely needed here; the requirement table still governs when they are.

## What this skill requires

- Nothing beyond the artifact write itself — no config, no extra tooling. The component
  syntax is plain markdown; validation happens in the viewer when the artifact is reviewed.

## Language and framework agnosticism

This skill is language and framework agnostic. The components describe *shapes of content*
(an endpoint, an entity, a flow, a file set) — not any particular stack. `apispec` fits any
HTTP framework; `datamodel` fits any store; the fences carry the same semantics whatever the
target codebase is written in.
