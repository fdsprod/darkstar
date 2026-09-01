# Architecture decision conventions

The [decision register](decision-register.json) is the authoritative discovery
index for architecture decisions. A decision may live in this directory or in
the normative architecture contract it governs, but it has exactly one register
entry and one canonical document.

## Lifecycle

Decision lifecycle is a tagged record, not a collection of flags:

- `proposed` has only `state`;
- `accepted` adds `acceptedAt`;
- `superseded` adds `supersededAt` and `supersededBy`; and
- `rejected` adds `rejectedAt` and `rationale`.

Never edit an accepted decision to reverse its outcome. Add the replacement,
then move the old entry to `superseded`; the reverse relationship is derived by
searching for entries whose `supersededBy` names the replacement.

Every entry identifies its originating Linear issue and the stable DS keys of
known implementation work affected by the decision. Add newly affected work as
it is discovered. The decision document records context, outcome, consequences,
alternatives, evidence, and relevant risk IDs using [ADR_TEMPLATE.md](ADR_TEMPLATE.md).

## Required implementation preflight

Before implementing work governed by an architecture decision:

1. identify every applicable DS decision in the work item's dependencies and
   referenced contracts;
2. run the governance check with those IDs;
3. stop if any decision is proposed, rejected, or superseded;
4. read the canonical current documents and surfaced risk records; and
5. update the affected-issue lists when the new work is not already linked.

```powershell
node scripts/governance-reference.mjs docs/decisions/decision-register.json docs/risks/risk-register.json DS-004 DS-010
```

The validation suite also checks every registered document, issue link,
lifecycle shape, risk reference, and the DS-001 through DS-010 spike inventory.
