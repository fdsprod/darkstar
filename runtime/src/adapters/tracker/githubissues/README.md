# GitHub Issues read-only source

`New(Config, Options)` implements `worksource.TrackerSourceV1` and
`TrackerBrowserV1`. It receives protected credential references and an immutable
evidence store through `ports/trackerconnection`. `NewGHCredentialResolver` can
reuse an explicitly selected existing `gh` account; the daemon can instead supply
its protected token-reference resolver. No API in this package writes tickets,
publishes pull requests, changes workflows, or creates execution records.

Use `NewConnection` with an empty account ID only to bootstrap `ProbeHealth`.
Recreate the connection with the returned native account ID before paging
`DiscoverDestinations`. Construct the bound source with the selected destination's
repository node ID and owner node ID, plus exact binding/configuration revisions.
No local code repository is required or inferred. The account identity is verified
in every authenticated GraphQL response; binding discovery additionally verifies
issue read access and repository identity/owner. Lost private access blocks reads;
owner transfers require explicit rebinding.

`Query.Text` is a case-insensitive literal substring of title/body. State and
state-reason filters use the stable advertised lowercase IDs, not GitHub search
syntax. The adapter walks the repository issue connection, so pull requests and
GitHub's capped search results cannot affect completeness. Each call reads one
bounded provider page; a filtered empty page may still carry `More`. Exhaust
the opaque query/scope/pin-bound cursor to complete a refresh. There is no
advertised incremental `After` filter, and pages are not transactional snapshots.

Issue GraphQL node IDs are identity; issue numbers and URLs are display/routing
attributes. Digests capture observed content, not provider-side compare-and-swap.
Exact missing and unchanged results are distinct; ambiguous provider `NOT_FOUND`
errors remain missing-or-inaccessible failures rather than proving deletion.
Original successful GraphQL response bytes are retained with SHA-256, provider,
host, observation time, scope and pin; normalized ticket evidence references that
original observation. Raw error bodies, authentication headers and resolved
credentials are never retained. Source Markdown is preserved as untrusted content.

More than 100 assignees/labels is `Unknown` until fully observed. Organization issue
types and native dependency/hierarchy relationships are currently `Unknown`.
Priority, sprint and issue archival are `Unsupported`; repository/Projects
archival and milestones are not substituted. Updated time and placement are
observed separately from business state. Rate limits return bounded retry timing
and exponential-backoff information; the daemon owns all retry scheduling.

The data design separates connection authority, repository identity, binding pin,
source evidence and normalized ticket knowledge. Closed knowledge variants avoid
claiming that incomplete metadata is empty, while immutable source references
prevent a renamed or moved provider resource from silently retargeting execution.

Verified against official documentation on 2026-09-12:

- [Global node IDs](https://docs.github.com/en/graphql/guides/using-global-node-ids)
- [GitHub GraphQL issues](https://docs.github.com/en/graphql/reference/issues)
- [GraphQL repositories](https://docs.github.com/en/graphql/reference/repos)
- [Rate limits and retry guidance](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api)
- [GitHub CLI selected-account token lookup](https://cli.github.com/manual/gh_auth_token)

Local transport fixtures verify behavior without live credentials or provider
writes. Authenticated tenant/host compatibility remains an installation probe.
