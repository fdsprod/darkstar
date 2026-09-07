import { useCallback, useEffect, useMemo, useState } from "react";

import { apiClient } from "../api/client";
import { AppLink } from "../app/router";
import { AsyncPanel, EmptyState } from "../components/InteractionPatterns";
import { useDashboardState } from "../state/DashboardStateProvider";
import { decodeArtifactViews, type ArtifactTargetKind, type DecodedArtifactView } from "./artifactModel";
import { formatDate, StatusPill } from "./WorkDetailPage";
import { humanize, shortIdentifier } from "./runDetailModel";

export interface ArtifactScope { kind: ArtifactTargetKind; id: string; label: string }

export function ProducedArtifacts({ scopes }: { scopes: readonly ArtifactScope[] }) {
  const { state } = useDashboardState();
  const [items, setItems] = useState<Array<{ scope: ArtifactScope; view: DecodedArtifactView }>>();
  const [error, setError] = useState("");
  const identity = scopes.map((scope) => `${scope.kind}:${scope.id}`).join("\u0000");
  const load = useCallback(async (signal?: AbortSignal) => {
    try {
      const responses = await Promise.all(scopes.map(async (scope) => ({ scope, values: decodeArtifactViews(await apiClient.listArtifacts(scope.kind, scope.id, signal)) })));
      const unique = new Map<string, { scope: ArtifactScope; view: DecodedArtifactView }>();
      for (const response of responses) for (const view of response.values) {
        const key = `${view.artifact.artifactId}:${view.artifact.version}`;
        if (!unique.has(key)) unique.set(key, { scope: response.scope, view });
      }
      setItems([...unique.values()].sort((left, right) => right.view.artifact.createdAt.localeCompare(left.view.artifact.createdAt)));
      setError("");
    } catch { if (!signal?.aborted) setError("Produced evidence could not be refreshed from its owning scopes."); }
  // identity intentionally captures the ordered scope set without making callers memoize it.
  }, [identity]);
  useEffect(() => { const abort = new AbortController(); void load(abort.signal); return () => abort.abort(); }, [load, state.cursor]);
  const latest = useMemo(() => {
    const byArtifact = new Map<string, { scope: ArtifactScope; view: DecodedArtifactView }>();
    for (const item of items ?? []) {
      const current = byArtifact.get(item.view.artifact.artifactId);
      if (!current || item.view.artifact.version > current.view.artifact.version || (item.view.artifact.version === current.view.artifact.version && item.scope.label.localeCompare(current.scope.label) < 0)) byArtifact.set(item.view.artifact.artifactId, item);
    }
    return [...byArtifact.values()].sort((left, right) => right.view.artifact.createdAt.localeCompare(left.view.artifact.createdAt) || left.view.artifact.artifactId.localeCompare(right.view.artifact.artifactId));
  }, [items]);
  if (!items) return <AsyncPanel state={error ? "error" : "loading"} title={error ? "Evidence unavailable" : "Loading produced evidence"} message={error || "Reading exact artifact bindings for this work context."} />;
  return <section className="detail-section produced-artifacts"><div className="section-heading"><div><p className="eyebrow">Owning work context</p><h2>Produced artifacts and revisions</h2></div><AppLink to={`/artifacts?targetKind=${encodeURIComponent(scopes[0]?.kind ?? "work")}&targetId=${encodeURIComponent(scopes[0]?.id ?? "")}`}>Open artifact diagnostics</AppLink></div>{error && <AsyncPanel compact state="error" title="Evidence refresh failed" message={error} />}{latest?.length ? <div className="produced-artifact-list">{latest.map(({ scope, view }) => <article key={view.artifact.artifactId}><header><div><span>{scope.label}</span><h3><AppLink to={`/artifacts/${encodeURIComponent(view.artifact.artifactId)}`}>{view.artifact.sourceName}</AppLink></h3></div><StatusPill status={view.freshness} /></header><p>{humanize(view.artifact.sourceKind)} source · revision {view.artifact.version} · {view.representations.length} representation{view.representations.length === 1 ? "" : "s"}</p><dl><div><dt>Provenance</dt><dd>{view.artifact.provenance.origin === "attempt" ? `${view.artifact.provenance.nodeId} · ${shortIdentifier(view.artifact.provenance.attemptId)}` : shortIdentifier(view.artifact.provenance.operationId)}</dd></div><div><dt>Evidence impact</dt><dd>{humanize(view.freshness)}</dd></div><div><dt>Produced</dt><dd>{formatDate(view.artifact.createdAt)}</dd></div></dl></article>)}</div> : <EmptyState kind="empty" title="No produced artifacts in this context" message="Artifacts appear here from exact work, run, node, and checkpoint bindings. Global artifact diagnostics remain available by deep link." compact />}</section>;
}
