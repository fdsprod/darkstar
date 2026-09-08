# Workflow canvas

The workflow editor shows one workflow version at a time on a canvas. Published versions, including the two shipped workflows, are read-only. **New version** creates an editable draft under the same workflow name. Publishing a higher semantic version makes it the default for future unpinned runs. Existing runs retain their resolved version and snapshot. Historical published versions remain inspectable.

Right-click the canvas (or focus it and press Insert) to add a node. Select a node to open its inspector; click empty canvas or press Escape to close it. Drag cards to position them. Connect matching pins by dragging, or click an output followed by an input. The **+ Input** pin creates a named, typed binding. White execution connections determine order; colored data connections supply values. All connections remain visible. Layout is presentation data and does not change execution semantics.

Start is an explicit movable event card connected to the configured entry. Starting a run invokes that entry when its required inputs resolve. Resource nodes supply values; they do not execute. A Branch / IF node evaluates its predicate and exposes True and False execution pins. Else is the false pin. Command, reasoning, approval, routing, point execution, and subworkflow nodes retain their runtime contracts.

| Resource | Meaning |
| --- | --- |
| Task | Snapshot of the starting board work item and description |
| Repository | Repository identity from the selected project |
| Artifact | Markdown/text input, or a named output produced by an action |
| Template | Versioned Markdown instructions and optional required headings |
| Constant | A typed literal; objects and arrays can carry a user-defined JSON Schema |
| Config | Selected effective non-secret configuration key, resolved at run start |
| Open items | Shared run journal with read, add, defer, and resolve tools |
| Decision log | Shared run journal with read, record, and supersede tools |

Each action output also appears as a separate value/artifact card. Connect it onward to consumers. An artifact input becomes a generated output when a producer is wired into it. A reasoning node may have multiple outputs, each with its own type, schema, filename, and template input. Add artifact outputs in the selected reasoning node inspector, then select each output card to configure its contract. Templates are connected inputs; each output explicitly selects which connected template governs it.

The daemon supplies `read_input`, `submit_output`, and the connected journal tools to the provider. The prompt includes the exact input IDs, output contracts, template contents, and instructions. The agent submits outputs individually and receives validation errors to correct. Final output must match the submitted values; required outputs cannot be skipped. Output values persist with run execution evidence. Journals use daemon-owned append-only SQLite events; Markdown is a read projection, and resolving an item records another event without rewriting history. These tools do not grant arbitrary journal-file mutation.

The shipped Story Execution 2.0.0 contains Questions → clarification → Research → Design → Planning → Execution, with explicit conditional branches and document outputs. Software Delivery 2.0.0 retains the broader delivery stages and pins its Story Execution child to 2.0.0. The former walking-skeleton and split-design examples are test fixtures, not shipped library entries.

Validation still runs before publication. The canvas is the authoring surface; there is no Structure tab, JSON editor, route-preview panel, fixed run-input card, palette sidebar, or data-binding form.
