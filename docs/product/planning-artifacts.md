# Planning artifact templates and schemas

> [Documentation index](../README.md)

DARKSTAR ships ten Markdown planning templates in `templates/planning` and one
strict tagged-union schema in
`schemas/planning-artifact-v1alpha1.schema.json`. The `artifactType` field is
the discriminator: each value selects exactly one permitted shape, and fields
from unrelated artifact types are rejected.

The default workflows attach the schema as an output validator for product
briefs, POC findings, product requirements, experience design, technical and
story research, technical and story design, delivery/story plans, and
implementation plans. Template level-two headings are derived from the titles
of required schema properties and checked by `npm run planning:check`.

Templates are guidance rather than evidence. Replace instructional comments
with observed facts, cite sources where the schema requests evidence, preserve
open questions instead of inventing answers, and use a new artifact version
when accepted content changes.

The opt-in [feature planning contract](../architecture/artifacts/FEATURE_PLANNING_CONTRACT.md)
adds versioned feature briefs and story backlogs with stable keys, explicit
repository impact, revision lineage, and separate external links and handoffs.
Its schema is `schemas/feature-planning-v1alpha1.schema.json`; example Markdown
projections ship in `templates/feature-planning`. These additions do not change
the default workflow output types or replace existing templates.
