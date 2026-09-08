# Typed workflow graph

In `darkstar.local/v1alpha3`, reusable nodes carry an exact `definition` reference with a closed `built_in`,
`project`, or `user` scope, semantic version, and content digest. Published
workflow JSON retains that resolved reference, so run snapshots continue to use
the same definition even after a newer version is created or the library entry
is archived. Built-ins are immutable; customization creates a project or user
derivative with explicit provenance.

The `routing` node is a first-class typed executor. Its outputs declare the
selected route, rationale, advice, missing information, assumptions, and whether
confirmation is required. Each named branch references an existing transition
ID, and authoritative validation rejects missing transitions or output type
mismatches before publish; assessment output never edits graph topology.

DAR-145 implements the authoring projection of the accepted DS-002
[execution semantics](../../docs/architecture/workflow/execution-semantics.md).
It preserves the existing draft revision/CAS, validation, route preview and
immutable publication APIs.

Ports and edges are tagged execution or data variants. Stable node IDs and
declared input/output IDs identify endpoints; run inputs are explicit source
ports. The graph is rebuilt from the workflow document, so there is no second
binding table to synchronize with Structure view. Layout and viewport live only
in the existing presentation document and never enter the execution digest.

Solid edges represent execution and dashed edges represent data bindings.
Click or keyboard-activate an output then an input to connect them. The shared
Data bindings form offers the same operation without canvas interaction.
Reconnecting a data port replaces its source with the selected whole value and
removes the old source's JSON pointer. The input inspector can configure a new
pointer or remove the binding. Integer outputs can bind to number inputs; other
whole-value connections require equal JSON types, matching daemon validation.
Existing pointer bindings are preserved and evaluated by the daemon.

Inline connection findings identify missing sources and incompatible whole-value
types before saving. They are advisory authoring checks; saved-revision server
validation remains mandatory for publication and covers topology, predicates,
executor configuration, schemas and references. Selecting a finding opens the
corresponding inspector section and focuses the exact field.

Canvas controls provide auto-layout, zoom-to-fit, zoom, and a node minimap.
Arrow keys pan while the canvas is focused; Shift+arrow moves the selected node.
Escape cancels a pending connection from any canvas control. Structure remains
the default view and works on narrow displays, with no drag needed to create,
connect, configure, reorder or delete nodes. The URL still stores the selected
draft, view and node/transition, and draft conflicts retain both documents until
the operator chooses a base explicitly.

The eight port-model tests cover identity, compatibility, stale selections,
pointer reconnection, immediate findings, malformed types, layout isolation and
node renaming. Browser-level integrated authoring and CAS coverage belongs to
the expanded DAR-140 acceptance gate.
