import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink, useRouter } from "../app/router";
import { ActionGuidance, AsyncPanel } from "../components/InteractionPatterns";
import { PageHeader } from "../components/PageStructure";
import { useDashboardState } from "../state/DashboardStateProvider";
import { DetailFailure, DetailLoading, formatDate, StatusPill, SummaryFact } from "./WorkDetailPage";
import { humanize, shortIdentifier } from "./runDetailModel";
import { buildArtifactRevisionRequest, buildArtifactTarget, decodeArtifactImpact, decodeArtifactView, decodeArtifactViews, previousRevision, revisionsForArtifact, type ArtifactImpactAssessment, type ArtifactRepresentation, type ArtifactRevisionInput, type ArtifactTargetKind, type DecodedArtifactView } from "./artifactModel";

type Schemas = components["schemas"];

export function ArtifactPage() {
  const { route, search, navigate } = useRouter();
  const { state } = useDashboardState();
  const artifactId = route.params.artifactId;
  const params = useMemo(() => new URLSearchParams(search), [search]);
  const requestedVersion = Number.parseInt(params.get("revision") ?? "", 10);
  const [revisions, setRevisions] = useState<DecodedArtifactView[]>();
  const [selectedVersion, setSelectedVersion] = useState<number>();
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [extracting, setExtracting] = useState(false);
  const revisionDialog = useRef<HTMLDialogElement>(null);
  const latestVersionRef = useRef<number | undefined>(undefined);

  const load = useCallback(async (signal?: AbortSignal, preferredVersion?: number) => {
    try {
      const all = revisionsForArtifact(decodeArtifactViews(await apiClient.listArtifacts(undefined, undefined, signal)), artifactId);
      if (!all.length) throw new MissingArtifactError();
      if (latestVersionRef.current !== undefined && latestVersionRef.current !== all[0].artifact.version) {
        if (revisionDialog.current?.open) revisionDialog.current.close();
        setNotice(`Artifact history refreshed. Revision ${all[0].artifact.version} is now latest. Review it before creating another revision.`);
      }
      latestVersionRef.current = all[0].artifact.version;
      setRevisions(all);
      setSelectedVersion((current) => {
        const desired = preferredVersion ?? (Number.isSafeInteger(requestedVersion) ? requestedVersion : current);
        return all.some((value) => value.artifact.version === desired) ? desired : all[0].artifact.version;
      });
      setError("");
    } catch (cause) {
      if (!signal?.aborted) setError(artifactLoadError(cause));
    }
  }, [artifactId, requestedVersion]);

  useEffect(() => {
    const abort = new AbortController();
    void load(abort.signal);
    return () => abort.abort();
  }, [load, state.cursor]);

  if (error && !revisions) return <DetailFailure title="Artifact unavailable" message={error} pageTitle="Artifact" breadcrumbs={[{ label: "Artifacts", to: "/artifacts" }, { label: shortIdentifier(artifactId) }]} />;
  if (!revisions || selectedVersion === undefined) return <DetailLoading label="Loading artifact revisions" pageTitle="Artifact" breadcrumbs={[{ label: "Artifacts", to: "/artifacts" }, { label: shortIdentifier(artifactId) }]} />;
  const selected = revisions.find((value) => value.artifact.version === selectedVersion) ?? revisions[0];
  function selectVersion(version: number, replace = false) {
    setSelectedVersion(version);
    const next = new URLSearchParams(params); next.set("revision", String(version));
    navigate(`/artifacts/${encodeURIComponent(artifactId)}?${next}`, { replace });
  }

  async function extract() {
    setExtracting(true); setError("");
    try { await apiClient.extractArtifact(artifactId, selected.artifact.version, `dashboard-artifact-extract-${crypto.randomUUID()}`); setNotice(`Extraction completed for revision ${selected.artifact.version}. Derived representations were refreshed.`); await load(undefined, selected.artifact.version); }
    catch { setError("Extraction could not complete. The immutable original remains stored; review capability diagnostics and retry when a processor is available."); }
    finally { setExtracting(false); }
  }

  return <div className="page detail-page artifact-page">
    <PageHeader className="detail-header artifact-header" eyebrow={`Immutable artifact · revision ${selected.artifact.version}`} title={selected.artifact.sourceName} description="Inspect exact source metadata, safe derived previews, provenance, extraction state, and read-only late-evidence impact." breadcrumbs={[{ label: "Artifacts", to: "/artifacts" }, { label: shortIdentifier(artifactId), to: `/artifacts/${encodeURIComponent(artifactId)}` }, { label: `Revision ${selected.artifact.version}` }]} status={<StatusPill status={selected.artifact.status} />} actions={<>{selected.artifact.status === "stored" ? <DownloadOriginal view={selected} /> : <span className="artifact-download-policy">Original preserved; download unavailable by inspection policy.</span>}<button type="button" className="button" disabled={extracting} onClick={() => void extract()}>{extracting ? "Extracting…" : selected.representations.length ? "Retry extraction" : "Extract"}</button><button type="button" className="button button--primary" disabled={selected.artifact.version !== revisions[0].artifact.version} aria-describedby={selected.artifact.version !== revisions[0].artifact.version ? "artifact-revision-guidance" : undefined} onClick={() => revisionDialog.current?.showModal()}>{selected.artifact.version === revisions[0].artifact.version ? "Create revision" : "Select latest to revise"}</button></>} />
    {selected.artifact.version !== revisions[0].artifact.version && <ActionGuidance id="artifact-revision-guidance">Select the latest revision before creating a new version.</ActionGuidance>}
    {extracting && <AsyncPanel compact state="loading" title="Extraction pending" message={<>Target artifact <code>{artifactId}</code>, revision {selected.artifact.version}.</>} />}
    {notice && <AsyncPanel compact state="success" title="Artifact updated" message={notice} />}
    {error && <AsyncPanel compact state="error" title="Artifact action failed" message={error} />}
    <section className="detail-summary artifact-summary" aria-label="Artifact summary"><SummaryFact label="Artifact" value={selected.artifact.artifactId} mono /><SummaryFact label="Revision" value={String(selected.artifact.version)} /><SummaryFact label="Freshness" value={humanize(selected.freshness)} /><SummaryFact label="Created" value={formatDate(selected.artifact.createdAt)} /></section>
    <div className="artifact-layout"><aside className="artifact-revision-rail" aria-label="Artifact revisions"><div className="section-heading"><div><p className="eyebrow">Recorded history</p><h2>Revisions</h2></div><span className="section-count">{revisions.length}</span></div><ol>{revisions.map((revision) => <li key={revision.artifact.version}><button type="button" aria-current={revision.artifact.version === selected.artifact.version ? "true" : undefined} onClick={() => selectVersion(revision.artifact.version)}><span>v{revision.artifact.version}</span><strong>{revision.artifact.sourceName}</strong><small>{formatDate(revision.artifact.createdAt)}</small><em>{humanize(revision.freshness)}</em></button></li>)}</ol><p>Versions are ordered from the artifact registry. The dashboard does not infer missing revisions.</p></aside><section className="artifact-primary"><ArtifactMetadata view={selected} /><RevisionComparison artifactId={artifactId} selected={selected} revisions={revisions} /><Representations values={selected.representations} /><ImpactPanel view={selected} search={search} /><Provenance view={selected} /></section></div>
    <RevisionDialog refValue={revisionDialog} artifact={selected} onCreated={async (version) => { setNotice(`Artifact revision ${version} was durably stored.`); selectVersion(version, true); await load(undefined, version); }} onStale={async () => { setNotice("Artifact state changed before the revision completed. Review the refreshed history before trying again."); await load(); }} />
  </div>;
}

function ArtifactMetadata({ view }: { view: DecodedArtifactView }) {
  const value = view.artifact;
  return <section className="detail-section artifact-metadata"><div className="section-heading"><div><p className="eyebrow">Stored boundary</p><h2>Revision metadata</h2></div><span className={`artifact-freshness artifact-freshness--${view.freshness}`}>{humanize(view.freshness)}</span></div><dl><Fact label="Declared media" value={value.declaredMediaType} mono /><Fact label="Detected media" value={value.detectedMediaType} mono /><Fact label="Source" value={`${humanize(value.sourceKind)} · ${value.sourceName}`} /><Fact label="Creator" value={value.creator} /><Fact label="Sensitivity" value={humanize(value.sensitivity)} /><Fact label="Trust" value={value.trust} /><Fact label="Size" value={formatBytes(value.size)} /><Fact label="Blob digest" value={value.blobDigest} mono /><Fact label="Registry locator" value={value.locator} mono /></dl><TokenGroup title="Roles" values={value.roles} /><TokenGroup title="Tags" values={value.tags} />{Object.keys(value.metadata).length > 0 && <div className="artifact-metadata-map"><h3>Metadata</h3>{Object.entries(value.metadata).map(([key, item]) => <p key={key}><code>{key}</code><span>{item}</span></p>)}</div>}</section>;
}

function RevisionComparison({ artifactId, selected, revisions }: { artifactId: string; selected: DecodedArtifactView; revisions: DecodedArtifactView[] }) {
  const prior = previousRevision(revisions, selected.artifact.version);
  const [diff, setDiff] = useState<Schemas["ArtifactVersionDiff"]>();
  const [lint, setLint] = useState<Schemas["ArtifactLintResult"]>();
  const [error, setError] = useState("");
  useEffect(() => {
    const abort = new AbortController();
    setDiff(undefined); setLint(undefined); setError("");
    void Promise.all([
      apiClient.lintArtifact(artifactId, selected.artifact.version, abort.signal),
      prior ? apiClient.diffArtifactVersions(artifactId, prior.artifact.version, selected.artifact.version, abort.signal) : Promise.resolve(undefined),
    ]).then(([nextLint, nextDiff]) => { setLint(nextLint); setDiff(nextDiff); }).catch((cause) => { if (!abort.signal.aborted) setError(cause instanceof ApiRequestError && cause.status === 404 ? "Revision comparison evidence is no longer available." : "Revision validation evidence could not be loaded."); });
    return () => abort.abort();
  }, [artifactId, prior?.artifact.version, selected.artifact.version]);
  return <section className="detail-section revision-comparison"><div className="section-heading"><div><p className="eyebrow">Exact version evidence</p><h2>Validation &amp; changes</h2></div>{lint && <span className={`artifact-lint artifact-lint--${lint.valid ? "valid" : "invalid"}`}>{lint.valid ? "Valid" : `${lint.issues.length} findings`}</span>}</div>{error && <p className="form-error" role="alert">{error}</p>}{!prior ? <p className="readiness-empty-copy">This is the first recorded revision; there is no earlier version to compare.</p> : !diff ? <p className="readiness-empty-copy">Loading the exact v{prior.artifact.version} → v{selected.artifact.version} comparison…</p> : <><p className="revision-boundary">v{diff.from} <span aria-hidden="true">→</span> v{diff.to}</p>{diff.changed.length ? <ul className="revision-changes">{diff.changed.map((item) => <li key={item}>{humanize(item)}</li>)}</ul> : <p className="readiness-empty-copy">No tracked content or metadata fields changed.</p>}<div className="revision-digests"><code title={diff.fromDigest}>{shortIdentifier(diff.fromDigest)}</code><span aria-hidden="true">→</span><code title={diff.toDigest}>{shortIdentifier(diff.toDigest)}</code></div></>}{lint && (lint.issues.length ? <ul className="artifact-lint-list">{lint.issues.map((issue, index) => <li key={`${issue.code}:${index}`}><code>{issue.code}</code><span>{issue.message}</span></li>)}</ul> : <p className="artifact-valid-note">No lint findings were recorded for this exact revision.</p>)}</section>;
}

function Representations({ values }: { values: DecodedArtifactView["representations"] }) {
  return <section className="detail-section"><div className="section-heading"><div><p className="eyebrow">Derived inspection</p><h2>Representations &amp; previews</h2></div><span className="section-count">{values.length}</span></div>{values.length ? <div className="artifact-representations">{values.map((value) => <RepresentationCard key={value.representationId} value={value} />)}</div> : <p className="readiness-empty-copy">No derived representations are recorded. Run extraction to discover a safe preview or descriptor; original access remains subject to inspection policy.</p>}</section>;
}

function RepresentationCard({ value }: { value: ArtifactRepresentation }) {
  const [preview, setPreview] = useState<{ kind: "text"; value: string } | { kind: "image"; value: string }>(); const [loading, setLoading] = useState(false); const [error, setError] = useState("");
  useEffect(() => () => { if (preview?.kind === "image") URL.revokeObjectURL(preview.value); }, [preview]);
  const supported = previewKind(value.mediaType, value.disclosure);
  async function loadPreview() { setLoading(true); setError(""); try { const content = await apiClient.readRepresentationContent(value.representationId); if (supported === "image") setPreview({ kind: "image", value: URL.createObjectURL(content.blob) }); else if (supported === "text") setPreview({ kind: "text", value: await content.blob.text() }); } catch { setError("Preview could not be loaded. The representation metadata remains authoritative."); } finally { setLoading(false); } }
  return <article><header><span>{humanize(value.representationKind)}</span><strong>{humanize(value.disclosure)}</strong></header><code>{shortIdentifier(value.representationId)}</code><p>{value.mediaType} · {formatBytes(value.size)} · {value.tokenEstimate} estimated tokens</p><small>{value.processorName} {value.processorVersion}{value.truncated ? " · truncated" : ""}</small>{supported ? <button type="button" className="artifact-preview-button" disabled={loading} onClick={() => void loadPreview()}>{loading ? "Loading preview…" : preview ? "Reload preview" : "Load safe preview"}</button> : <p className="artifact-preview-unavailable">Preview unavailable for this format or disclosure policy. The immutable original is preserved.</p>}{preview?.kind === "text" && <pre className="artifact-text-preview">{preview.value}</pre>}{preview?.kind === "image" && <img className="artifact-image-preview" src={preview.value} alt={`Derived ${humanize(value.representationKind)} representation`} />}{error && <p className="form-error" role="alert">{error}</p>}{value.diagnostics.length > 0 && <ul>{value.diagnostics.map((item, index) => <li key={index}>{item}</li>)}</ul>}</article>;
}

function DownloadOriginal({ view }: { view: DecodedArtifactView }) {
  const [loading, setLoading] = useState(false); const [error, setError] = useState("");
  async function download() { setLoading(true); setError(""); try { const content = await apiClient.readArtifactContent(view.artifact.artifactId, view.artifact.version); const url = URL.createObjectURL(content.blob); const anchor = document.createElement("a"); anchor.href = url; anchor.download = content.filename ?? view.artifact.sourceName; anchor.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); } catch { setError("Original unavailable by policy"); } finally { setLoading(false); } }
  return <span className="artifact-download-control"><button type="button" className="button" disabled={loading} onClick={() => void download()}>{loading ? "Preparing…" : "Download original"}</button>{error && <small role="status">{error}</small>}</span>;
}

function ImpactPanel({ view, search }: { view: DecodedArtifactView; search: string }) {
  const params = useMemo(() => new URLSearchParams(search), [search]); const [kind, setKind] = useState<ArtifactTargetKind>((params.get("targetKind") as ArtifactTargetKind) || "work"); const [id, setId] = useState(params.get("targetId") ?? ""); const [runId, setRunId] = useState(""); const [assessment, setAssessment] = useState<ArtifactImpactAssessment>(); const [loading, setLoading] = useState(false); const [error, setError] = useState("");
  async function assess(event: FormEvent) { event.preventDefault(); setLoading(true); setError(""); try { const target = buildArtifactTarget(kind, id); setAssessment(decodeArtifactImpact(await apiClient.assessArtifactImpact(view.artifact.artifactId, view.artifact.version, { target, ...(runId.trim() ? { runId: runId.trim() } : {}) }))); } catch (cause) { setAssessment(undefined); setError(cause instanceof ApiRequestError && cause.status === 409 ? "This exact artifact version is not actively bound to that target." : "Impact could not be assessed for this exact target and revision."); } finally { setLoading(false); } }
  return <section className="detail-section artifact-impact"><div className="section-heading"><div><p className="eyebrow">Read-only policy boundary</p><h2>Late-evidence impact review</h2></div>{assessment && <span className="scope-badge">Review required</span>}</div><p className="panel-context">Assessment proposes scoped actions only. No route, attempt, or descendant action is applied here; route-changing proposals require the existing readiness approval flow.</p><form onSubmit={(event) => void assess(event)}><label><span>Target</span><select value={kind} onChange={(event) => setKind(event.target.value as ArtifactTargetKind)}><option value="work">Work item</option><option value="run">Run</option><option value="node">Run node</option><option value="checkpoint">Checkpoint</option><option value="story">Story</option><option value="implementation_point">Implementation point</option></select></label><label><span>Identifier</span><input required value={id} onChange={(event) => setId(event.target.value)} /></label><label><span>Related run <small>optional</small></span><input value={runId} onChange={(event) => setRunId(event.target.value)} /></label><button className="button" type="submit" disabled={loading}>{loading ? "Assessing…" : "Assess impact"}</button></form>{error && <p className="form-error" role="alert">{error}</p>}{assessment && <div className="impact-results"><div><h3>Recommended actions</h3><ol>{assessment.proposals.map((proposal, index) => <li key={`${proposal.action}:${index}`}><strong>{humanize(proposal.action)}</strong><span>{humanize(proposal.reason)}</span>{proposal.action === "refresh" && <code>{proposal.attemptId}</code>}{(proposal.action === "revise" || proposal.action === "invalidate") && <small>{proposal.artifacts.length} affected artifact{proposal.artifacts.length === 1 ? "" : "s"}</small>}</li>)}</ol></div><div><h3>Active attempt coverage</h3>{assessment.coverage.length ? <ul>{assessment.coverage.map((coverage) => <li key={coverage.attemptId}><code>{shortIdentifier(coverage.attemptId)}</code><span>{coverage.nodeId}</span><strong>{humanize(coverage.state)}</strong></li>)}</ul> : <p>No active attempts are in this assessment scope.</p>}</div><p className="impact-boundary"><strong>Assessment complete.</strong> No recommendation has been applied. Continue to Readiness when an approved route change is required.</p></div>}</section>;
}

function previewKind(mediaType: string, disclosure: ArtifactRepresentation["disclosure"]): "text" | "image" | undefined { if (disclosure === "withheld") return undefined; const normalized = mediaType.split(";", 1)[0].trim().toLowerCase(); if (["text/plain", "text/csv", "application/json"].includes(normalized)) return "text"; if (["image/png", "image/jpeg", "image/webp"].includes(normalized)) return "image"; return undefined; }

function Provenance({ view }: { view: DecodedArtifactView }) {
  const value = view.artifact.provenance;
  return <section className="detail-section artifact-provenance"><div className="section-heading"><div><p className="eyebrow">Affected lineage</p><h2>Provenance</h2></div><span className="scope-badge">{value.origin}</span></div><p className="panel-context">These are recorded origin links. Revision order alone is not treated as dependency lineage.</p><dl><Fact label="Operation" value={value.operationId} mono />{value.origin === "attempt" && <><Fact label="Run" value={value.runId} mono /><Fact label="Node" value={value.nodeId} mono /><Fact label="Attempt" value={value.attemptId} mono /></>}{value.sourceArtifactId && <div><dt>Source artifact</dt><dd><AppLink to={`/artifacts/${encodeURIComponent(value.sourceArtifactId)}`}>{value.sourceArtifactId}</AppLink></dd></div>}</dl></section>;
}

function RevisionDialog({ refValue, artifact, onCreated, onStale }: { refValue: React.RefObject<HTMLDialogElement | null>; artifact: DecodedArtifactView; onCreated(version: number): Promise<void>; onStale(): Promise<void> }) {
  const [input, setInput] = useState<ArtifactRevisionInput>({ sourceName: artifact.artifact.sourceName, mediaType: artifact.artifact.declaredMediaType, content: "", sensitivity: artifact.artifact.sensitivity, roles: artifact.artifact.roles.join(", "), tags: artifact.artifact.tags.join(", ") });
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => setInput({ sourceName: artifact.artifact.sourceName, mediaType: artifact.artifact.declaredMediaType, content: "", sensitivity: artifact.artifact.sensitivity, roles: artifact.artifact.roles.join(", "), tags: artifact.artifact.tags.join(", ") }), [artifact.artifact.artifactId, artifact.artifact.version]);
  function close() { if (!submitting) { refValue.current?.close(); setError(""); } }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    let body: Schemas["ArtifactIngestRequest"];
    try { body = buildArtifactRevisionRequest(input); } catch (cause) { setError(cause instanceof Error ? cause.message : "Revision input is invalid."); return; }
    setSubmitting(true); setError("");
    try {
      const result = await apiClient.reviseArtifact(artifact.artifact.artifactId, artifact.artifact.version, body, `dashboard-artifact-revision-${crypto.randomUUID()}`);
      const created = decodeArtifactView({ artifact: result.artifact, freshness: "current", representations: [] });
      refValue.current?.close(); setInput((current) => ({ ...current, content: "" })); await onCreated(created.artifact.version);
    } catch (cause) {
      if (cause instanceof ApiRequestError && (cause.status === 409 || cause.status === 412)) { refValue.current?.close(); await onStale(); }
      else setError("The artifact revision could not be stored. Check daemon health and review the fields.");
    } finally { setSubmitting(false); }
  }
  return <dialog ref={refValue} className="work-dialog artifact-revision-dialog" aria-labelledby="artifact-revision-title" onCancel={(event) => { if (submitting) event.preventDefault(); }} onClose={() => { if (!submitting) setError(""); }}><form aria-busy={submitting} onSubmit={(event) => void submit(event)}><header className="work-dialog__header"><div><p className="eyebrow">New immutable version</p><h2 id="artifact-revision-title">Revise {shortIdentifier(artifact.artifact.artifactId)}</h2></div><button type="button" className="icon-button" aria-label="Close artifact revision" onClick={close}>×</button></header><p className="work-dialog__intro">Paste the complete replacement content. Stored blob content is intentionally not loaded back into this form.</p><div className="artifact-form-grid"><label className="field"><span>Source name</span><input required value={input.sourceName} onChange={(event) => setInput({ ...input, sourceName: event.target.value })} /></label><label className="field"><span>Media type</span><input required value={input.mediaType} onChange={(event) => setInput({ ...input, mediaType: event.target.value })} placeholder="text/markdown" /></label><label className="field"><span>Sensitivity</span><select value={input.sensitivity} onChange={(event) => setInput({ ...input, sensitivity: event.target.value as ArtifactRevisionInput["sensitivity"] })}><option value="unknown">Unknown</option><option value="public">Public</option><option value="internal">Internal</option><option value="sensitive">Sensitive</option><option value="secret">Secret</option></select></label><label className="field"><span>Roles <small>comma separated</small></span><input value={input.roles} onChange={(event) => setInput({ ...input, roles: event.target.value })} /></label><label className="field artifact-content-field"><span>Complete replacement content</span><textarea autoFocus rows={10} value={input.content} onChange={(event) => setInput({ ...input, content: event.target.value })} /></label><label className="field artifact-content-field"><span>Tags <small>comma separated</small></span><input value={input.tags} onChange={(event) => setInput({ ...input, tags: event.target.value })} /></label></div>{error && <p className="form-error" role="alert">{error}</p>}<footer className="work-dialog__footer"><p className="dialog-draft-note">Closing discards unsaved changes.</p><button type="button" className="button" disabled={submitting} onClick={close}>Cancel</button><button type="submit" className="button button--primary" disabled={submitting}>{submitting ? "Storing…" : "Store revision"}</button></footer></form></dialog>;
}

function TokenGroup({ title, values }: { title: string; values: string[] }) { return <div className="artifact-token-group"><h3>{title}</h3>{values.length ? <div>{values.map((value) => <code key={value}>{value}</code>)}</div> : <p>None recorded</p>}</div>; }
function Fact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) { return <div><dt>{label}</dt><dd className={mono ? "mono" : undefined} title={mono ? value : undefined}>{value}</dd></div>; }
function formatBytes(value: number) { if (value < 1024) return `${value} B`; if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KB`; return `${(value / (1024 * 1024)).toFixed(1)} MB`; }
function artifactLoadError(cause: unknown) { if (cause instanceof MissingArtifactError || (cause instanceof ApiRequestError && cause.status === 404)) return "The requested artifact does not exist."; if (cause instanceof Error && !(cause instanceof ApiRequestError)) return "Artifact data does not match the supported dashboard contract."; return "Authoritative artifact data is temporarily unavailable. Check daemon health and try again."; }
class MissingArtifactError extends Error {}
