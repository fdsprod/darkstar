# Linear source adapter

This adapter implements the shared read-only tracker source/browser ports. It
does not implement a ticket writer, workflow selection, run creation or provider
mutations. `Config` contains a protected credential reference; the daemon supplies
`trackerconnection.CredentialResolver` and immutable `EvidenceStore` implementations.

Use `PersonalAPIKey` for a personal API key or `OAuth` for an existing OAuth access
token. The adapter sends the documented raw key or Bearer header respectively,
resolves credentials on every request, refuses redirects and verifies the account
and workspace returned by every query. Tokens are never included in pins, source
evidence or safe failures. The official API endpoint is the production default;
an explicitly configured loopback endpoint supports deterministic integration tests.

Bootstrap `New` with installation/configuration revisions and a credential
reference, leaving account/workspace/team empty. `Health` discovers the current
account/workspace; `DiscoverScopes` paginates accessible teams and each team's
projects. Bind the selected immutable IDs in a new configuration before calling
the shared source ports. An optional project ID further narrows browsing. Exact
issue reads require issue UUIDs and retain workspace identity across team/project
moves, with observed team placement reported separately.

Browsing supports scoped title/description search, advertised stable field filters,
opaque query-bound pagination and an `updated_at`/`after` timestamp predicate.
Archived issues are explicitly included and expose an independent archive
observation. Updated timestamps are exposed for daemon refresh watermarks; the
daemon owns polling/webhook scheduling and should overlap incremental refreshes
and periodically reconcile the full scope, since a filtered result is not proof
of disappearance.

Original GraphQL JSON, comment bodies and attachment reference metadata are
retained in immutable provenance envelopes with source URL, observation time and
SHA-256 digest. External attachment binaries and arbitrary linked web pages are
not downloaded. Exact reads and browse pages complete supported nested
connections before computing the same canonical content revision; label,
relationship and comment edits are detected even without a changed parent
timestamp. Pagination is bounded to 100 continuation pages per collection; an
incomplete read fails explicitly instead of publishing an empty/partial board.
Provider pages are observations over time, not transactional snapshots.

Null or ambiguous inaccessible issue responses remain permission failures.
`Missing` requires an explicit issue-level not-found code alongside the expected
authorized account/workspace identity. Partial GraphQL errors invalidate the
whole response. Rate-limit failures retain normalized retry/reset metadata but
never schedule a retry or include raw error bodies.

Protocol references checked on 2026-09-12:

- [Authentication, errors and archived resources](https://linear.app/developers/graphql)
- [Relay paging and update ordering](https://linear.app/developers/pagination)
- [Filter operators and relationship filters](https://linear.app/developers/filtering)
- [Rate-limit codes and reset headers](https://linear.app/developers/rate-limiting)
- [Official GraphQL schema](https://github.com/linear/linear/blob/master/packages/sdk/src/schema.graphql)

The configuration separates bootstrap discovery from a fully pinned source
binding; successful reads must validate the selected account/workspace. Business
state, archive status, updated time and placement are observations independent of
execution. This prevents credential rotation or team moves from silently
retargeting work, and prevents partial lists from masquerading as observed none.
