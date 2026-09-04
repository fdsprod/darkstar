import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent, type FormEvent, type KeyboardEvent } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import { AppLink, useRouter } from "../app/router";
import { PageHeader } from "../components/PageStructure";
import { tabKeyTarget } from "../accessibility/keyboard";
import { useDashboardState } from "../state/DashboardStateProvider";
import { DetailFailure, DetailLoading, formatDate, StatusPill } from "./WorkDetailPage";
import { buildArtifactIngestRequest, buildArtifactTarget, decodeArtifactImpact, decodeArtifactView, decodeArtifactViews, type ArtifactImpactAssessment, type ArtifactIngestInput, type ArtifactTargetKind, type DecodedArtifactView } from "./artifactModel";
import { humanize, shortIdentifier } from "./runDetailModel";

const targetKinds: Array<{ value: ArtifactTargetKind; label: string; hint: string }> = [
  { value: "work", label: "Work item", hint: "work_…" },
  { value: "run", label: "Run", hint: "run_…" },
  { value: "node", label: "Run node", hint: "node identifier" },
  { value: "checkpoint", label: "Checkpoint", hint: "checkpoint_…" },
  { value: "story", label: "Story", hint: "story_…" },
  { value: "implementation_point", label: "Implementation point", hint: "point_…" },
];

export function ArtifactsPage() {
  const { search, navigate } = useRouter();
  const { state } = useDashboardState();
  const params = useMemo(() => new URLSearchParams(search), [search]);
  const [items, setItems] = useState<DecodedArtifactView[]>();
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const dialog = useRef<HTMLDialogElement>(null);
  const requestedTarget = queryTarget(params);

  const load = useCallback(async (signal?: AbortSignal) => {
    try { setItems(decodeArtifactViews(await apiClient.listArtifacts(requestedTarget?.kind, requestedTarget?.id, signal))); setError(""); }
    catch (cause) { if (!signal?.aborted) setError(artifactListError(cause)); }
  }, [requestedTarget?.id, requestedTarget?.kind]);

  useEffect(() => { const abort = new AbortController(); void load(abort.signal); return () => abort.abort(); }, [load, state.cursor]);
  useEffect(() => {
    if (items && params.has("ingest") && dialog.current && !dialog.current.open) dialog.current.showModal();
  }, [items, search]);

  const latest = useMemo(() => latestArtifacts(items ?? []), [items]);
  function closeDialog() { dialog.current?.close(); if (params.has("ingest")) { const next = new URLSearchParams(params); next.delete("ingest"); navigate(`/artifacts${next.size ? `?${next}` : ""}`, { replace: true }); } }

  if (error && !items) return <DetailFailure title="Evidence unavailable" message={error} pageTitle="Artifacts" breadcrumbs={[{ label: "Artifacts" }]} />;
  if (!items) return <DetailLoading label="Loading artifact registry" pageTitle="Artifacts" breadcrumbs={[{ label: "Artifacts" }]} />;
  return <div className="page artifacts-page">
    <PageHeader className="artifacts-header" eyebrow="Immutable evidence registry" title="Artifacts" description="Ingest files or pasted evidence, bind an exact version to execution scope, and inspect durable representations and provenance." breadcrumbs={[{ label: "Artifacts" }]} actions={<button type="button" className="button button--primary" onClick={() => dialog.current?.showModal()}>Add evidence</button>} />
    {notice && <p className="detail-action-message" role="status">{notice}</p>}{error && <p className="detail-action-message detail-action-message--error" role="alert">{error}</p>}
    <ArtifactFilters params={params} navigate={navigate} count={latest.length} />
    {requestedTarget && <p className="artifact-scope-note">Showing exact versions actively bound to <strong>{humanize(requestedTarget.kind)}</strong> <code>{requestedTarget.id}</code>.</p>}
    {latest.length ? <div className="artifact-catalog">{latest.map((view) => <ArtifactCard key={view.artifact.artifactId} view={view} />)}</div> : <div className="detail-empty"><strong>No artifacts in this scope</strong><p>Add a file or pasted note and bind it to this target. Stored evidence remains immutable and auditable.</p><button type="button" className="button" onClick={() => dialog.current?.showModal()}>Add evidence</button></div>}
    <IngestDialog refValue={dialog} initialTarget={requestedTarget} onClose={closeDialog} onComplete={async (result) => { setNotice(result); closeDialog(); await load(); }} />
  </div>;
}

function ArtifactFilters({ params, navigate, count }: { params: URLSearchParams; navigate(to: string): void; count: number }) {
  const [kind, setKind] = useState(params.get("targetKind") ?? ""); const [id, setId] = useState(params.get("targetId") ?? "");
  function submit(event: FormEvent) { event.preventDefault(); const next = new URLSearchParams(); if (kind && id.trim()) { next.set("targetKind", kind); next.set("targetId", id.trim()); } navigate(`/artifacts${next.size ? `?${next}` : ""}`); }
  return <form className="artifact-filters" onSubmit={submit}><label><span>Target type</span><select value={kind} onChange={(event) => setKind(event.target.value)}><option value="">All artifacts</option>{targetKinds.map((target) => <option key={target.value} value={target.value}>{target.label}</option>)}</select></label><label><span>Target identifier</span><input value={id} disabled={!kind} onChange={(event) => setId(event.target.value)} placeholder={targetKinds.find((target) => target.value === kind)?.hint ?? "Choose a target type"} /></label><button type="submit" className="button">Apply</button>{(params.has("targetKind") || params.has("targetId")) && <button type="button" className="button" onClick={() => navigate("/artifacts")}>Clear</button>}<strong>{count} artifact{count === 1 ? "" : "s"}</strong></form>;
}

function ArtifactCard({ view }: { view: DecodedArtifactView }) {
  const value = view.artifact;
  return <AppLink className="artifact-card" to={`/artifacts/${encodeURIComponent(value.artifactId)}`}><header><span className="artifact-card__kind">{humanize(value.sourceKind)}</span><StatusPill status={view.freshness} /></header><h2>{value.sourceName}</h2><code>{shortIdentifier(value.artifactId)} · v{value.version}</code><p>{value.detectedMediaType} · {formatBytes(value.size)}</p><footer><span>{view.representations.length} representation{view.representations.length === 1 ? "" : "s"}</span><time dateTime={value.createdAt}>{formatDate(value.createdAt)}</time></footer></AppLink>;
}

function IngestDialog({ refValue, initialTarget, onClose, onComplete }: { refValue: React.RefObject<HTMLDialogElement | null>; initialTarget?: { kind: ArtifactTargetKind; id: string }; onClose(): void; onComplete(message: string): Promise<void> }) {
  const [mode, setMode] = useState<"file" | "paste">("file"); const [file, setFile] = useState<File>(); const [dragging, setDragging] = useState(false);
  const sourceTabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const [paste, setPaste] = useState(""); const [sourceName, setSourceName] = useState("pasted-evidence.md"); const [mediaType, setMediaType] = useState("text/markdown");
  const [targetKind, setTargetKind] = useState<ArtifactTargetKind>(initialTarget?.kind ?? "work"); const [targetId, setTargetId] = useState(initialTarget?.id ?? ""); const [runId, setRunId] = useState("");
  const [sensitivity, setSensitivity] = useState<ArtifactIngestInput["sensitivity"]>("internal"); const [roles, setRoles] = useState("evidence"); const [tags, setTags] = useState("");
  const [submitting, setSubmitting] = useState(false); const [error, setError] = useState(""); const [partial, setPartial] = useState<{ artifactId: string; version: number; target: ReturnType<typeof buildArtifactTarget> }>();
  const operationKeys = useRef({ ingest: `dashboard-artifact-ingest-${crypto.randomUUID()}`, extract: `dashboard-artifact-extract-${crypto.randomUUID()}`, attach: `dashboard-artifact-attach-${crypto.randomUUID()}` });
  useEffect(() => { if (initialTarget && !partial) { setTargetKind(initialTarget.kind); setTargetId(initialTarget.id); } }, [initialTarget?.id, initialTarget?.kind, partial]);
  function pick(next?: File) { if (next) { setFile(next); setMode("file"); setError(""); } }
  function drop(event: DragEvent) { event.preventDefault(); setDragging(false); pick(event.dataTransfer.files[0]); }
  function onSourceTabKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const modes: Array<"file" | "paste"> = ["file", "paste"];
    const target = tabKeyTarget(index, event.key, modes.length);
    if (target === undefined) return;
    event.preventDefault(); setMode(modes[target]); sourceTabRefs.current[target]?.focus();
  }
  async function submit(event: FormEvent) {
    event.preventDefault(); setSubmitting(true); setError(""); let stored = partial;
    try {
      const target = stored?.target ?? buildArtifactTarget(targetKind, targetId);
      if (!stored) {
        const source: ArtifactIngestInput["source"] = mode === "file" ? (file ? { kind: "file", file } : (() => { throw new Error("Choose a file to ingest."); })()) : { kind: "paste", sourceName, mediaType, content: paste };
        const body = await buildArtifactIngestRequest({ source, sensitivity, roles, tags });
        const raw = await apiClient.ingestArtifact(body, operationKeys.current.ingest);
        const ingested = decodeArtifactView({ artifact: raw.artifact, freshness: "current", representations: [] });
        stored = { artifactId: ingested.artifact.artifactId, version: ingested.artifact.version, target }; setPartial(stored);
      }
      let extraction = "Extraction unavailable";
      try { const result = await apiClient.extractArtifact(stored.artifactId, stored.version, operationKeys.current.extract); const count = result.representations.length; extraction = `${humanize(result.support.state)} · ${count} representation${count === 1 ? "" : "s"}`; } catch { extraction = "Extraction unavailable; retry from the artifact"; }
      await apiClient.attachArtifact({ artifact: { artifactId: stored.artifactId, version: stored.version }, target }, operationKeys.current.attach);
      let impact: ArtifactImpactAssessment | undefined;
      try { impact = decodeArtifactImpact(await apiClient.assessArtifactImpact(stored.artifactId, stored.version, { target, ...(runId.trim() ? { runId: runId.trim() } : {}) })); } catch { impact = undefined; }
      const actions = impact?.proposals.map((proposal) => humanize(proposal.action)).join(", ");
      setPartial(undefined); setFile(undefined); setPaste("");
      operationKeys.current = { ingest: `dashboard-artifact-ingest-${crypto.randomUUID()}`, extract: `dashboard-artifact-extract-${crypto.randomUUID()}`, attach: `dashboard-artifact-attach-${crypto.randomUUID()}` };
      await onComplete(`Stored ${shortIdentifier(stored.artifactId)} v${stored.version} and attached it to ${humanize(target.kind)}. ${extraction}${actions ? `. Impact: ${actions}. Review approval-bound proposals on the artifact` : ". Impact assessment was unavailable; open the artifact to retry the read-only review"}.`);
    } catch (cause) { setError(stored ? `The artifact is durably stored as ${stored.artifactId} v${stored.version}, but attachment did not complete. Retry attachment without re-ingesting the source.` : ingestError(cause)); }
    finally { setSubmitting(false); }
  }
  return <dialog ref={refValue} className="work-dialog artifact-ingest-dialog" aria-labelledby="artifact-ingest-title" onCancel={(event) => { if (submitting) event.preventDefault(); }} onClose={() => { if (!submitting) { setError(""); onClose(); } }}><form aria-busy={submitting} onSubmit={(event) => void submit(event)}><header className="work-dialog__header"><div><p className="eyebrow">Immutable source + exact target</p><h2 id="artifact-ingest-title">Add evidence</h2></div><button type="button" className="icon-button" aria-label="Close evidence ingestion" onClick={onClose}>×</button></header><p className="work-dialog__intro">The original bytes are stored as untrusted evidence. Ingestion and attachment are separate durable operations; impact assessment never changes a route by itself.</p>
    <div className="ingest-mode" role="tablist" aria-label="Evidence source"><button ref={(value) => { sourceTabRefs.current[0] = value; }} id="evidence-source-tab-file" type="button" role="tab" tabIndex={mode === "file" ? 0 : -1} aria-selected={mode === "file"} aria-controls="evidence-source-panel-file" onKeyDown={(event) => onSourceTabKeyDown(event, 0)} onClick={() => setMode("file")}>File / drop</button><button ref={(value) => { sourceTabRefs.current[1] = value; }} id="evidence-source-tab-paste" type="button" role="tab" tabIndex={mode === "paste" ? 0 : -1} aria-selected={mode === "paste"} aria-controls="evidence-source-panel-paste" onKeyDown={(event) => onSourceTabKeyDown(event, 1)} onClick={() => setMode("paste")}>Paste text</button></div>
    <div id="evidence-source-panel-file" role="tabpanel" tabIndex={mode === "file" ? 0 : -1} aria-labelledby="evidence-source-tab-file" hidden={mode !== "file"}>{mode === "file" && <label className="artifact-dropzone" data-dragging={dragging} onDragEnter={(event) => { event.preventDefault(); setDragging(true); }} onDragOver={(event) => event.preventDefault()} onDragLeave={() => setDragging(false)} onDrop={drop}><input type="file" onChange={(event) => pick(event.target.files?.[0])} /><span aria-hidden="true">↓</span><strong>{file?.name ?? "Drop a file here or browse"}</strong><small>{file ? `${formatBytes(file.size)} · ${file.type || "application/octet-stream"}` : "Up to 25 MiB · originals are never overwritten"}</small></label>}</div>
    <div id="evidence-source-panel-paste" role="tabpanel" tabIndex={mode === "paste" ? 0 : -1} aria-labelledby="evidence-source-tab-paste" hidden={mode !== "paste"}>{mode === "paste" && <div className="artifact-paste-fields"><label className="field"><span>Source name</span><input required value={sourceName} onChange={(event) => setSourceName(event.target.value)} /></label><label className="field"><span>Media type</span><input required value={mediaType} onChange={(event) => setMediaType(event.target.value)} /></label><label className="field artifact-content-field"><span>Evidence content</span><textarea required rows={8} value={paste} onChange={(event) => setPaste(event.target.value)} placeholder="Paste notes, a transcript, structured evidence, or other text…" /></label></div>}</div>
    <fieldset className="artifact-target-fields"><legend>Attach to</legend><label className="field"><span>Target type</span><select value={targetKind} disabled={Boolean(partial)} onChange={(event) => setTargetKind(event.target.value as ArtifactTargetKind)}>{targetKinds.map((target) => <option key={target.value} value={target.value}>{target.label}</option>)}</select></label><label className="field"><span>Target identifier</span><input required value={targetId} disabled={Boolean(partial)} onChange={(event) => setTargetId(event.target.value)} placeholder={targetKinds.find((target) => target.value === targetKind)?.hint} /></label><label className="field"><span>Related run <small>optional for scoped impact</small></span><input value={runId} onChange={(event) => setRunId(event.target.value)} placeholder="run_…" /></label>{partial && <p className="artifact-target-lock" role="status">Attachment target locked to {humanize(partial.target.kind)} <code>{partial.target.id}</code> for this idempotent retry.</p>}</fieldset>
    <details className="artifact-ingest-details"><summary>Classification and selection metadata</summary><div className="artifact-form-grid"><label className="field"><span>Sensitivity</span><select value={sensitivity} onChange={(event) => setSensitivity(event.target.value as ArtifactIngestInput["sensitivity"])}><option value="unknown">Unknown</option><option value="public">Public</option><option value="internal">Internal</option><option value="sensitive">Sensitive</option><option value="secret">Secret</option></select></label><label className="field"><span>Semantic roles <small>comma separated</small></span><input value={roles} onChange={(event) => setRoles(event.target.value)} /></label><label className="field artifact-content-field"><span>Tags <small>comma separated</small></span><input value={tags} onChange={(event) => setTags(event.target.value)} /></label></div></details>
    {error && <p className="form-error" role="alert">{error}</p>}<footer className="work-dialog__footer"><button type="button" className="button" disabled={submitting} onClick={onClose}>Back</button><button type="submit" className="button button--primary" disabled={submitting}>{submitting ? (partial ? "Retrying attachment…" : "Storing & attaching…") : (partial ? "Retry attachment" : "Store & attach evidence")}</button></footer></form></dialog>;
}

function queryTarget(params: URLSearchParams) { const kind = params.get("targetKind") ?? ""; const id = params.get("targetId") ?? ""; try { return kind || id ? buildArtifactTarget(kind, id) : undefined; } catch { return undefined; } }
function latestArtifacts(values: DecodedArtifactView[]) { const map = new Map<string, DecodedArtifactView>(); for (const value of values) { const current = map.get(value.artifact.artifactId); if (!current || value.artifact.version > current.artifact.version) map.set(value.artifact.artifactId, value); } return [...map.values()].sort((left, right) => right.artifact.createdAt.localeCompare(left.artifact.createdAt) || left.artifact.artifactId.localeCompare(right.artifact.artifactId)); }
function formatBytes(value: number) { if (value < 1024) return `${value} B`; if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`; return `${(value / (1024 * 1024)).toFixed(1)} MB`; }
function artifactListError(cause: unknown) { if (cause instanceof Error && !(cause instanceof ApiRequestError)) return "Artifact data does not match the supported dashboard contract."; return "Authoritative artifact data is temporarily unavailable. Check daemon health and try again."; }
function ingestError(cause: unknown) { if (cause instanceof Error && !(cause instanceof ApiRequestError)) return cause.message; if (cause instanceof ApiRequestError && cause.status === 409) return "The artifact or binding conflicts with current authoritative state. Refresh and try again."; return "Evidence could not be stored. Check daemon health and review the source and target."; }
