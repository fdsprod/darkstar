import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { ApiRequestError, apiClient, type AgentLogChunk } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink, useRouter } from "../app/router";
import { AsyncPanel } from "../components/InteractionPatterns";
import { PageHeader } from "../components/PageStructure";
import { useDashboardState } from "../state/DashboardStateProvider";
import { decodeArtifactViews, revisionsForArtifact, type DecodedArtifactView } from "./artifactModel";
import {
  buildReviewDecision, chooseSafeTextRepresentation, exactFeedbackSetForRepresentation, iterationActivity, nextReviewSession,
  mergeDiffPages, orderedReviewSessions, parseReviewView, previousReviewedVersion, representationContentDigestMatches, reviewSessionChanged, splitDiffRows, validateArtifactDiff, verifyCandidateArtifact,
  validateAnnotationComment, type CandidateState, type DiffExpectation, type DiffState, type ReviewAction, type ReviewView, type SafeTextState,
} from "./artifactReviewModel";
import { DetailFailure, DetailLoading, formatDate, StatusPill } from "./WorkDetailPage";
import { humanize, shortIdentifier } from "./runDetailModel";
import { ArtifactAnnotations } from "./ArtifactAnnotations";
import type { FeedbackAnnotationDraft } from "./artifactReviewModel";

type Schemas = components["schemas"];
type ReviewSession = Schemas["CheckpointReviewSession"];
type PriorSelection = { kind: "reviewed" | "artifact_history"; version: number };
type AgentLogState = { kind: "idle" } | { kind: "loading"; attemptId: string } | { kind: "available"; attemptId: string; chunk: AgentLogChunk } | { kind: "error"; attemptId: string; message: string };
type FeedbackSetView = { id: string; state: "draft" | "submitted"; candidate: Schemas["ArtifactVersionRef"]; candidateDigest: string; representation: { representationId: string; digest: string; disclosure: "raw" | "redacted" }; overallInstruction?: string; annotations: FeedbackAnnotationDraft[]; submittedAt?: string };

export function ArtifactReviewPage() {
  const { route } = useRouter();
  const approvalId = route.params.approvalId;
  return <ArtifactReviewWorkspace key={approvalId} approvalId={approvalId} />;
}

function ArtifactReviewWorkspace({ approvalId }: { approvalId: string }) {
  const { search, navigate } = useRouter();
  const { state } = useDashboardState();
  const params = useMemo(() => new URLSearchParams(search), [search]);
  const requestedView = params.has("view") ? parseReviewView(params.get("view")) : undefined;
  const requestedRevision = Number(params.get("revision"));
  const [session, setSession] = useState<ReviewSession>();
  const [history, setHistory] = useState<Schemas["CheckpointReviewHistory"]>();
  const [revisions, setRevisions] = useState<DecodedArtifactView[]>([]);
  const [prior, setPrior] = useState<PriorSelection>();
  const [currentText, setCurrentText] = useState<SafeTextState>();
  const [priorText, setPriorText] = useState<SafeTextState>();
  const [diff, setDiff] = useState<DiffState>({ kind: "not_applicable", reason: "no_prior" });
  const [agent, setAgent] = useState<Schemas["Agent"]>();
  const [candidateState, setCandidateState] = useState<CandidateState>({ kind: "current" });
  const [feedback, setFeedback] = useState("");
  const [annotations, setAnnotations] = useState<FeedbackAnnotationDraft[]>([]);
  const [draftBinding, setDraftBinding] = useState<{ representationId: string; digest: string }>();
  const [submittedSets, setSubmittedSets] = useState<FeedbackSetView[]>([]);
  const [busy, setBusy] = useState<ReviewAction>();
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [log, setLog] = useState<AgentLogState>({ kind: "idle" });
  const [diffBusy, setDiffBusy] = useState(false);
  const [diffRetry, setDiffRetry] = useState(0);
  const currentRef = useRef<ReviewSession | undefined>(undefined);
  const readyRef = useRef<HTMLButtonElement>(null);
  const focusedNewerRef = useRef("");
  const diffCursorRef = useRef("");
  const lifetimeAbortRef = useRef(new AbortController());
  const agentAttemptRef = useRef<string | undefined>(undefined);

  useEffect(() => () => lifetimeAbortRef.current.abort(), []);

  const load = useCallback(async (signal?: AbortSignal, acknowledge = false) => {
    try {
      const exact = await apiClient.getCheckpointReviewSession(approvalId, signal);
      const reviewHistory = await apiClient.getCheckpointReviewHistory(exact.checkpointId, signal);
      const all = revisionsForArtifact(decodeArtifactViews(await apiClient.listArtifacts(undefined, undefined, signal)), exact.candidate.artifactId);
      verifyCandidateArtifact(exact.candidate, exact.candidateDigest, all);
      const reviewedPrior = previousReviewedVersion(reviewHistory, exact);
      const previousArtifact = all.find((item) => item.artifact.version < exact.candidate.version)?.artifact.version;
      const nextPrior: PriorSelection | undefined = reviewedPrior ? { kind: "reviewed", version: reviewedPrior } : previousArtifact ? { kind: "artifact_history", version: previousArtifact } : undefined;
      const previous = currentRef.current;
      if (exact.state === "superseded") setCandidateState({ kind: "stale", reason: "candidate_superseded" });
      else if (!acknowledge && previous && reviewSessionChanged(previous, exact)) setCandidateState({ kind: "stale", reason: "resource_changed" });
      else if (acknowledge || !previous) setCandidateState({ kind: "current" });
      currentRef.current = exact; setSession(exact); setHistory(reviewHistory); setRevisions(all); setPrior(nextPrior); setError("");
      setSubmittedSets([...new Map(reviewHistory.sessions.flatMap(readFeedbackSets).filter((item) => item.state === "submitted").map((item) => [item.id, item])).values()]);
      if (exact.activeIteration) {
        const attemptId = exact.activeIteration.attemptId;
        agentAttemptRef.current = attemptId;
        setAgent((current) => current?.attemptId === attemptId ? current : undefined);
        setLog((current) => current.kind !== "idle" && current.attemptId === attemptId ? current : { kind: "idle" });
        try {
          const nextAgent = await apiClient.getAgent(attemptId, signal);
          if (nextAgent.attemptId !== attemptId) throw new Error("Agent projection identity mismatch.");
          if (!signal?.aborted && agentAttemptRef.current === attemptId) setAgent(nextAgent);
        } catch { if (!signal?.aborted && agentAttemptRef.current === attemptId) setAgent(undefined); }
      } else { agentAttemptRef.current = undefined; setAgent(undefined); setLog({ kind: "idle" }); }
      const newer = nextReviewSession(reviewHistory, exact);
      const newerKey = newer ? `${newer.id}:${newer.revision}` : "";
      if (newer && focusedNewerRef.current !== newerKey) {
        focusedNewerRef.current = newerKey;
        setNotice(`Candidate revision ${newer.revision} is ready for review.`);
        window.requestAnimationFrame(() => readyRef.current?.focus());
      }
    } catch (cause) {
      if (!signal?.aborted) {
        if (currentRef.current) setCandidateState({ kind: "stale", reason: "resource_changed" });
        setError(cause instanceof ApiRequestError && cause.status === 404 ? "This exact review session is unavailable." : "The review workspace could not refresh authoritative state.");
      }
    }
  }, [approvalId]);

  useEffect(() => { const abort = new AbortController(); void load(abort.signal); return () => abort.abort(); }, [load, state.cursor]);

  const priorVersion = prior?.version;
  const view: ReviewView = requestedView ?? (session && session.revision > 1 && prior ? "inline" : "current");
  const shownVersion = Number.isSafeInteger(requestedRevision) && revisions.some((item) => item.artifact.version === requestedRevision) ? requestedRevision : session?.candidate.version;
  useEffect(() => {
    if (!session || shownVersion === undefined) return;
    const abort = new AbortController();
    const shownArtifact = revisions.find((item) => item.artifact.version === shownVersion);
    const candidateArtifact = revisions.find((item) => item.artifact.version === session.candidate.version);
    const priorArtifact = priorVersion ? revisions.find((item) => item.artifact.version === priorVersion) : undefined;
    setCurrentText({ kind: "loading", version: shownVersion });
    if (shownArtifact) void loadSafeText(shownArtifact, abort.signal).then((value) => { if (!abort.signal.aborted) setCurrentText(value); });
    else setCurrentText({ kind: "error", version: shownVersion, message: "The exact artifact revision is absent from authoritative history." });
    if (priorVersion && priorArtifact) { setPriorText({ kind: "loading", version: priorVersion }); void loadSafeText(priorArtifact, abort.signal).then((value) => { if (!abort.signal.aborted) setPriorText(value); }); }
    else setPriorText({ kind: "unavailable", version: 0, reason: "missing" });
    if (priorVersion && priorArtifact && candidateArtifact) {
      const expected = diffExpectation(priorArtifact, candidateArtifact);
      setDiff({ kind: "loading", from: priorVersion, to: session.candidate.version });
      void apiClient.diffArtifactVersions(session.candidate.artifactId, priorVersion, session.candidate.version, {}, abort.signal).then((value) => {
        if (abort.signal.aborted) return;
        const verified = validateArtifactDiff(expected, value);
        setDiff(verified.textDiff.status === "available" ? { kind: "available", value: verified } : { kind: "unavailable", from: priorVersion, to: session.candidate.version, reason: verified.textDiff.reason });
      }).catch(() => { if (!abort.signal.aborted) setDiff({ kind: "error", from: priorVersion, to: session.candidate.version, message: "The exact bounded comparison could not be loaded or verified." }); });
    }
    else setDiff({ kind: "not_applicable", reason: "no_prior" });
    return () => abort.abort();
  }, [diffRetry, priorVersion, revisions, session?.candidate.artifactId, session?.candidate.version, shownVersion]);

  if (error && !session) return <DetailFailure title="Review workspace unavailable" message={error} pageTitle="Artifact review" breadcrumbs={[{ label: "Checkpoints", to: "/checkpoints" }, { label: shortIdentifier(approvalId) }]} />;
  if (!session || !history) return <DetailLoading label="Loading exact artifact review" pageTitle="Artifact review" breadcrumbs={[{ label: "Checkpoints", to: "/checkpoints" }, { label: shortIdentifier(approvalId) }]} />;
  const newer = nextReviewSession(history, session);
  const activity = iterationActivity(session, agent, newer);
  const candidateArtifact = revisions.find((item) => item.artifact.version === session.candidate.version);
  const priorArtifact = priorVersion ? revisions.find((item) => item.artifact.version === priorVersion) : undefined;
  let exactComparisonReady = false;
  if (diff?.kind === "available" && candidateArtifact && priorArtifact) {
    try { validateArtifactDiff(diffExpectation(priorArtifact, candidateArtifact), diff.value); exactComparisonReady = true; } catch { exactComparisonReady = false; }
  }
  const provenanceVersion = view === "prior" ? priorVersion : shownVersion;
  const selectedArtifact = revisions.find((item) => item.artifact.version === provenanceVersion);
  const shownArtifact = revisions.find((item) => item.artifact.version === shownVersion);
  const displayedCurrent = currentText && currentText.version === shownVersion && (currentText.kind !== "available" || currentText.artifactDigest === shownArtifact?.artifact.blobDigest) ? currentText : shownVersion === undefined ? undefined : { kind: "loading" as const, version: shownVersion };
  const displayedPrior = priorText && priorText.version === priorVersion && (priorText.kind !== "available" || priorText.artifactDigest === priorArtifact?.artifact.blobDigest) ? priorText : priorVersion === undefined ? undefined : { kind: "loading" as const, version: priorVersion };
  const diffStateBound = diff.kind === "not_applicable" ? !priorVersion : diff.kind === "available" ? exactComparisonReady : diff.from === priorVersion && diff.to === session.candidate.version;
  const displayedDiff: DiffState = diffStateBound ? diff : priorVersion ? { kind: "loading", from: priorVersion, to: session.candidate.version } : { kind: "not_applicable", reason: "no_prior" };
  const readingExactCandidate = (view === "current" && shownVersion === session.candidate.version && currentText?.kind === "available" && currentText.version === session.candidate.version && currentText.artifactDigest === session.candidateDigest) || ((view === "inline" || view === "split") && exactComparisonReady);
  const mutationAllowed = candidateState.kind === "current" && readingExactCandidate && session.state === "awaiting_human" && !busy;
  const draftBindingMatches = !draftBinding || (displayedCurrent?.kind === "available" && displayedCurrent.representationId === draftBinding.representationId && displayedCurrent.representationDigest === draftBinding.digest);
  const annotationEditingAllowed = mutationAllowed && draftBindingMatches;
  const feedbackSetAllowed = annotationEditingAllowed && annotations.every((item) => { try { validateAnnotationComment(item.comment); return true; } catch { return false; } });
  const exactSubmittedSet = displayedCurrent?.kind === "available" && shownArtifact ? exactFeedbackSetForRepresentation(submittedSets, shownArtifact.artifact, shownArtifact.artifact.blobDigest, displayedCurrent.representationId, displayedCurrent.representationDigest) : undefined;
  const editingCurrentDraft = shownVersion === session.candidate.version && session.state === "awaiting_human";
  const inlineAnnotations = editingCurrentDraft ? annotations : exactSubmittedSet?.annotations ?? [];

  function changeAnnotations(next: FeedbackAnnotationDraft[]) {
    if (next.length && !draftBinding && displayedCurrent?.kind === "available") setDraftBinding({ representationId: displayedCurrent.representationId, digest: displayedCurrent.representationDigest });
    if (!next.length) setDraftBinding(undefined);
    setAnnotations(next);
  }

  function setView(nextView: ReviewView) { const next = new URLSearchParams(params); next.set("view", nextView); next.delete("revision"); navigate(`/checkpoints/${encodeURIComponent(approvalId)}/review?${next}`); }
  function selectRevision(version: number) { const next = new URLSearchParams(params); next.set("view", "current"); next.set("revision", String(version)); navigate(`/checkpoints/${encodeURIComponent(approvalId)}/review?${next}`); }
  function openNewCandidate() { if (newer) navigate(`/checkpoints/${encodeURIComponent(newer.id)}/review?view=inline`); }
  async function mutate(action: ReviewAction) {
    const exactSession = currentRef.current;
    if (!exactSession || exactSession.id !== approvalId || lifetimeAbortRef.current.signal.aborted || !mutationAllowed) return;
    setBusy(action); setError("");
    try {
      if (action === "request_revisions") {
        if (!feedbackSetAllowed) throw new Error("The local annotations are bound to a different safe representation. Keep them for reference, then remove and re-anchor them before submitting.");
        const submitted = await submitAnnotationFeedbackSet(exactSession, displayedCurrent?.kind === "available" ? displayedCurrent : undefined, feedback, annotations, lifetimeAbortRef.current.signal);
        currentRef.current = submitted.session; setSession(submitted.session); setSubmittedSets((sets) => [...sets.filter((item) => item.id !== submitted.set.id), submitted.set]); setFeedback(""); setAnnotations([]); setDraftBinding(undefined); setCandidateState({ kind: "current" });
        setNotice("The complete feedback set was submitted. Progress appears only when authoritative state advances.");
        await load(lifetimeAbortRef.current.signal, true); return;
      }
      const result = await apiClient.decideCheckpointReviewSession(exactSession.id, exactSession.resourceVersion, `dashboard-review-decision-${crypto.randomUUID()}`, buildReviewDecision(exactSession, action, feedback), lifetimeAbortRef.current.signal);
      currentRef.current = result; setSession(result); setFeedback(""); setCandidateState({ kind: "current" });
      setNotice(`${humanize(action)} was durably recorded for the exact candidate.`);
      await load(lifetimeAbortRef.current.signal, true);
    } catch (cause) {
      if (cause instanceof ApiRequestError && (cause.status === 409 || cause.status === 412)) {
        setCandidateState({ kind: "stale", reason: "resource_changed" }); setNotice("This candidate changed before the mutation completed. Your feedback draft is retained."); await load(lifetimeAbortRef.current.signal);
      } else setError(cause instanceof Error ? cause.message : "The review action could not be recorded.");
    } finally { setBusy(undefined); }
  }
  async function readLog() {
    if (!agent?.logReference || agentAttemptRef.current !== agent.attemptId) return;
    const attemptId = agent.attemptId;
    setLog({ kind: "loading", attemptId });
    try {
      const chunk = await apiClient.readAgentLog(attemptId, 0, 65_536, lifetimeAbortRef.current.signal);
      if (!lifetimeAbortRef.current.signal.aborted && agentAttemptRef.current === attemptId) setLog({ kind: "available", attemptId, chunk });
    } catch { if (!lifetimeAbortRef.current.signal.aborted && agentAttemptRef.current === attemptId) setLog({ kind: "error", attemptId, message: "The bounded agent log could not be loaded." }); }
  }
  async function loadMoreDiff() {
    if (!session || !priorVersion || diff?.kind !== "available" || diff.value.textDiff.status !== "available" || !diff.value.textDiff.nextCursor) return;
    const cursor = diff.value.textDiff.nextCursor;
    if (diffCursorRef.current === cursor) return;
    diffCursorRef.current = cursor;
    setDiffBusy(true);
    try {
      const current = diff.value;
      const currentTextDiff = current.textDiff;
      if (currentTextDiff.status !== "available") throw new Error("The current diff page is not available.");
      const priorArtifact = revisions.find((item) => item.artifact.version === priorVersion);
      const candidateArtifact = revisions.find((item) => item.artifact.version === session.candidate.version);
      if (!priorArtifact || !candidateArtifact) throw new Error("Exact diff artifact evidence is unavailable.");
      const next = validateArtifactDiff(diffExpectation(priorArtifact, candidateArtifact), await apiClient.diffArtifactVersions(session.candidate.artifactId, priorVersion, session.candidate.version, { fromRepresentationId: currentTextDiff.from.representationId, toRepresentationId: currentTextDiff.to.representationId, cursor }, lifetimeAbortRef.current.signal));
      const merged = mergeDiffPages(current, next, cursor);
      setDiff({ kind: "available", value: merged });
    } catch { if (!lifetimeAbortRef.current.signal.aborted) setError("The next bounded diff page could not be loaded or verified."); } finally { diffCursorRef.current = ""; setDiffBusy(false); }
  }

  return <div className="page review-page">
    <PageHeader eyebrow="Human-in-the-loop review" title={`Candidate revision ${session.revision}`} description={`Review exact artifact ${shortIdentifier(session.candidate.artifactId)} version ${session.candidate.version} before recording a durable decision.`} breadcrumbs={[{ label: "Checkpoints", to: "/checkpoints" }, { label: shortIdentifier(session.id) }]} status={<StatusPill status={session.state} />} />
    {notice && <AsyncPanel compact state={newer ? "success" : candidateState.kind === "stale" ? "stale" : "success"} title={newer ? "New candidate ready" : candidateState.kind === "stale" ? "Candidate changed" : "Review updated"} message={<>{notice}{newer && <button ref={readyRef} className="button button--compact" type="button" onClick={openNewCandidate}>Review revision {newer.revision}</button>}</>} />}
    {error && <AsyncPanel compact state="error" title="Review action unavailable" message={error} />}
    {candidateState.kind === "stale" && <section className="review-stale" role="alert"><strong>{candidateState.reason === "candidate_superseded" ? "Candidate superseded." : "Displayed decision binding is stale."}</strong><span>Your local feedback remains below. Mutations are disabled {candidateState.reason === "candidate_superseded" ? "for this superseded candidate; open the newer recorded candidate when available." : "until you acknowledge the refetched exact session."}</span>{candidateState.reason === "resource_changed" && <button className="button button--compact" type="button" onClick={() => void load(lifetimeAbortRef.current.signal, true)}>Review refreshed state</button>}</section>}
    {annotations.length > 0 && !draftBindingMatches && <section className="review-stale" role="alert"><strong>Range draft uses an earlier representation.</strong><span>The draft is preserved for reference. Discard its range comments before selecting and anchoring replacement comments.</span><button className="button button--compact button--danger" type="button" onClick={() => { setAnnotations([]); setDraftBinding(undefined); }}>Discard range draft</button></section>}
    {!readingExactCandidate && <section className="review-stale" role="status"><strong>{view === "prior" ? "Prior-only view selected." : view === "current" && shownVersion !== session.candidate.version ? "Historical revision selected." : "Exact review evidence is not ready."}</strong><span>Decision controls are disabled until candidate v{session.candidate.version} safe content or its exact verified comparison is displayed.</span>{(view === "prior" || shownVersion !== session.candidate.version) && <button className="button button--compact" type="button" onClick={() => selectRevision(session.candidate.version)}>Return to current candidate</button>}</section>}
    <div className="review-workspace">
      <RevisionRail revisions={revisions} selected={shownVersion ?? session.candidate.version} reviewed={new Set(orderedReviewSessions(history.sessions).map((item) => item.candidate.version))} onSelect={selectRevision} />
      <section className="review-reader" tabIndex={-1} aria-label="Artifact review reader">
        <nav className="review-view-tabs" aria-label="Reader view">{(["current", "prior", "inline", "split"] as ReviewView[]).map((item) => <button type="button" key={item} aria-current={view === item ? "page" : undefined} disabled={(item === "prior" || item === "inline" || item === "split") && !priorVersion} onClick={() => setView(item)}>{item === "inline" ? "Inline diff" : item === "split" ? "Side-by-side" : humanize(item)}</button>)}</nav>
        {selectedArtifact && <ArtifactProvenance value={selectedArtifact} prefix={view === "prior" && prior?.kind === "artifact_history" ? "Prior artifact-history" : view === "prior" ? "Prior reviewed" : "Selected"} />}
        {view === "current" && displayedCurrent?.kind === "available"
          ? <ArtifactAnnotations text={displayedCurrent.text} annotations={inlineAnnotations} readOnly={!editingCurrentDraft || !annotationEditingAllowed} onChange={changeAnnotations} />
          : <Reader view={view} current={displayedCurrent} prior={displayedPrior} priorKind={prior?.kind} diff={displayedDiff} currentVersion={session.candidate.version} priorVersion={priorVersion} diffBusy={diffBusy} onLoadMore={() => void loadMoreDiff()} onRetry={() => setDiffRetry((value) => value + 1)} />}
      </section>
      <aside className="review-context" aria-label="Feedback and agent iteration">
        <section><p className="eyebrow">Exact decision binding</p><h2>Round {session.revision}</h2><dl><dt>Approval</dt><dd><code>{shortIdentifier(session.id)}</code></dd><dt>Candidate</dt><dd>v{session.candidate.version}</dd><dt>Digest</dt><dd><code title={session.candidateDigest}>{shortIdentifier(session.candidateDigest)}</code></dd><dt>Resource</dt><dd>{session.resourceVersion}</dd></dl></section>
        <section><h2>Revision activity</h2>{activity.length ? <ol className="review-activity">{activity.map((item, index) => <li key={`${item.stage}:${item.attemptId ?? "none"}:${index}`}><span className={`timeline-marker timeline-marker--${item.stage === "failed" || item.stage === "cancelled" ? "danger" : item.stage === "new_candidate_ready" ? "success" : "active"}`} aria-hidden="true" /><div><strong>{humanize(item.stage)}</strong><p>{item.message}</p>{item.occurredAt && <small>{formatDate(item.occurredAt)}</small>}</div></li>)}</ol> : <p>No revision activity has been durably recorded for this candidate.</p>}{agent?.logReference && <><button className="button button--compact" type="button" disabled={log.kind === "loading" && log.attemptId === agent.attemptId} onClick={() => void readLog()}>{log.kind === "loading" && log.attemptId === agent.attemptId ? "Loading log…" : "Load bounded agent log"}</button>{log.kind === "error" && log.attemptId === agent.attemptId && <p role="alert">{log.message}</p>}{log.kind === "available" && log.attemptId === agent.attemptId && <pre className="review-agent-log" tabIndex={0} aria-live="off">{new TextDecoder().decode(log.chunk.bytes)}</pre>}</>}</section>
        <ReviewTurns history={history} />
        <section className="review-separate-context"><h2>Separate attention</h2><p>Provider permission and required-input decisions do not approve this artifact.</p>{session.activeIteration && <><AppLink to={`/agents?tab=permissions&permissionAttemptId=${encodeURIComponent(session.activeIteration.attemptId)}`}>Provider permissions</AppLink><AppLink to={`/checkpoints?tab=inputs&attemptId=${encodeURIComponent(session.activeIteration.attemptId)}`}>Required input</AppLink></>}</section>
      </aside>
    </div>
    <section className="review-decision-bar" aria-label="Exact candidate decision"><label><span>Overall instruction for candidate v{session.candidate.version}</span><textarea rows={2} maxLength={16384} value={feedback} onChange={(event) => setFeedback(event.target.value)} placeholder="Summarize requested changes; range comments are submitted with this instruction" /></label><div><button className="button button--primary" type="button" disabled={!mutationAllowed || !session.allowedActions.includes("approve")} onClick={() => void mutate("approve")}>{busy === "approve" ? "Recording…" : "Approve"}</button><button className="button" type="button" disabled={!feedbackSetAllowed || !session.allowedActions.includes("request_changes") || !feedback.trim()} onClick={() => void mutate("request_revisions")}>{busy === "request_revisions" ? "Submitting set…" : `Submit feedback set${annotations.length ? ` · ${annotations.length}` : ""}`}</button><button className="button button--danger" type="button" disabled={!mutationAllowed || !session.allowedActions.includes("reject") || !feedback.trim()} onClick={() => void mutate("reject")}>{busy === "reject" ? "Recording…" : "Reject"}</button></div><small>{!draftBindingMatches ? "The safe representation changed; the local range draft is preserved but must be re-anchored. " : ""}Bound to approval {shortIdentifier(session.id)}, revision {session.revision}, artifact v{session.candidate.version}, candidate {shortIdentifier(session.candidateDigest)}, resource {session.resourceVersion}. Local drafts remain available when this binding becomes stale.</small></section>
  </div>;
}

function RevisionRail({ revisions, selected, reviewed, onSelect }: { revisions: DecodedArtifactView[]; selected: number; reviewed: Set<number>; onSelect(version: number): void }) {
  return <aside className="review-revisions" aria-label="Artifact revision history"><h2>Artifact revisions</h2><ol>{revisions.map((item) => <li key={item.artifact.version}><button type="button" aria-current={selected === item.artifact.version ? "true" : undefined} onClick={() => onSelect(item.artifact.version)}><strong>v{item.artifact.version}</strong><span>{item.artifact.sourceName}</span><small>{reviewed.has(item.artifact.version) ? "Reviewed candidate" : "Artifact history"} · {formatDate(item.artifact.createdAt)}</small></button></li>)}</ol><p>History comes from exact immutable artifact versions; missing revisions are not inferred.</p></aside>;
}

function Reader({ view, current, prior, priorKind, diff, currentVersion, priorVersion, diffBusy, onLoadMore, onRetry }: { view: ReviewView; current?: SafeTextState; prior?: SafeTextState; priorKind?: PriorSelection["kind"]; diff: DiffState; currentVersion: number; priorVersion?: number; diffBusy: boolean; onLoadMore(): void; onRetry(): void }) {
  if (view === "current") return <SafeTextReader state={current} label={`Artifact revision ${current?.version ?? currentVersion}`} />;
  if (view === "prior") return <SafeTextReader state={prior} label={`${priorKind === "reviewed" ? "Prior reviewed" : "Prior artifact-history"} revision ${priorVersion ?? "unavailable"}`} />;
  if (!priorVersion) return <ReaderUnavailable reason="missing" version={currentVersion} />;
  if (diff.kind === "not_applicable") return <ReaderUnavailable reason="missing" version={currentVersion} />;
  if (diff.kind === "loading") return <div className="review-reader-loading" aria-busy="true">Loading and verifying exact v{priorVersion} → v{currentVersion} comparison…</div>;
  if (diff.kind === "error") return <div className="review-reader-unavailable" role="alert"><h2>Exact comparison unavailable</h2><p>{diff.message}</p><button className="button" type="button" onClick={onRetry}>Retry comparison</button></div>;
  if (diff.kind === "unavailable") return <ReaderUnavailable reason={diff.reason} version={currentVersion} />;
  const value = diff.value;
  if (value.textDiff.status !== "available") return <ReaderUnavailable reason={value.textDiff.reason} version={currentVersion} />;
  if (view === "inline") return <div className="inline-diff" aria-label={`Inline diff from revision ${priorVersion} to ${currentVersion}`}>{value.textDiff.hunks.map((hunk, hunkIndex) => <section key={`${hunk.fromStart}:${hunk.toStart}:${hunkIndex}`}><h2>Lines {hunk.fromStart} → {hunk.toStart}</h2>{hunk.entries.map((entry, index) => <div key={index} className={`diff-line diff-line--${entry.kind}`}><span aria-hidden="true">{entry.kind === "added" ? "+" : entry.kind === "removed" ? "−" : entry.kind === "omitted" ? "⋯" : " "}</span><code>{entry.kind === "omitted" ? `${entry.omittedLines} lines omitted by bounded diff policy` : entry.text}</code></div>)}</section>)}<DiffContinuation diff={value} busy={diffBusy} onLoadMore={onLoadMore} /></div>;
  return <><div className="split-diff" role="table" aria-label={`Side-by-side diff from revision ${priorVersion} to ${currentVersion}`}><div role="row" className="split-diff__header"><strong role="columnheader">Prior v{priorVersion}</strong><strong role="columnheader">Current v{currentVersion}</strong></div>{splitDiffRows(value).map((row, index) => <div role="row" className={`split-diff__row split-diff__row--${row.kind}`} key={index}><div role="cell"><span>{row.leftLine ?? ""}</span><code>{row.left}</code></div><div role="cell"><span>{row.rightLine ?? ""}</span><code>{row.right}</code></div></div>)}</div><DiffContinuation diff={value} busy={diffBusy} onLoadMore={onLoadMore} /></>;
}

function DiffContinuation({ diff, busy, onLoadMore }: { diff: Schemas["ArtifactVersionDiffWithText"]; busy: boolean; onLoadMore(): void }) { return diff.textDiff.status === "available" && diff.textDiff.nextCursor ? <div className="diff-continuation" role="status"><span>This is a partial bounded comparison. More recorded changes are available.</span><button className="button button--compact" type="button" disabled={busy} onClick={onLoadMore}>{busy ? "Loading more changes…" : `Load more changes · ${diff.textDiff.totalEntries} total entries`}</button></div> : null; }
function ArtifactProvenance({ value, prefix }: { value: DecodedArtifactView; prefix: string }) {
  const origin = value.artifact.provenance;
  return <section className="review-provenance" aria-label={`${prefix} artifact revision ${value.artifact.version} provenance`}><strong>{prefix} v{value.artifact.version} provenance</strong><span>{humanize(origin.origin)} · {value.artifact.sourceName} · {value.artifact.creator}</span><span>Operation <code>{shortIdentifier(origin.operationId)}</code>{origin.origin === "attempt" && <> · Attempt <AppLink to={`/agents?tab=agents&attemptId=${encodeURIComponent(origin.attemptId)}`}>{shortIdentifier(origin.attemptId)}</AppLink> · Node <code>{origin.nodeId}</code></>}{origin.sourceArtifactId && <> · Source <code>{shortIdentifier(origin.sourceArtifactId)}</code></>}</span></section>;
}

function ReviewTurns({ history }: { history: Schemas["CheckpointReviewHistory"] }) {
  const sessions = orderedReviewSessions(history.sessions);
  return <section><h2>Recorded revision evidence</h2><ol className="review-turns">{sessions.map((session) => <li key={session.id}><strong>Revision {session.revision} · candidate v{session.candidate.version}</strong>{session.decision ? <><p><b>{humanize(session.decision.action)}</b> by {session.decision.actor.type} · {session.decision.actor.id}</p><p>{session.decision.comment ?? "No decision comment recorded."}</p><small>{formatDate(session.decision.decidedAt)}</small></> : <p>No final decision recorded for this revision.</p>}{session.turns.map((turn) => <div className="review-recorded-turn" key={`${session.id}:${turn.sequence}`}><strong>{turn.kind === "human_feedback" ? "Human feedback" : `Agent ${humanize(turn.outcome)}`}</strong><p>{turn.kind === "human_feedback" && turn.feedbackSet ? turn.feedbackSet.overallInstruction : turn.message ?? (turn.kind === "agent_response" && turn.outcome === "revised" ? `Produced artifact v${turn.resultingCandidate.version}.` : "No message recorded.")}</p>{turn.kind === "human_feedback" && turn.feedbackSet && <ol className="recorded-annotation-list" aria-label="Submitted annotation comments">{turn.feedbackSet.annotations.map((annotation) => <li key={annotation.id}><q>{annotation.anchor.quotedText}</q><p>{annotation.comment}</p></li>)}</ol>}<small>{turn.actor.type} · {turn.actor.id} · {formatDate(turn.occurredAt)} · <AppLink to={`/agents?tab=agents&attemptId=${encodeURIComponent(turn.attemptId)}`}>attempt {shortIdentifier(turn.attemptId)}</AppLink></small></div>)}{session.affectedArtifacts.length ? <details><summary>Affected artifact lineage · {session.affectedArtifacts.length}</summary><ul>{session.affectedArtifacts.map((effect) => <li key={`${effect.trigger.artifactId}:${effect.trigger.version}:${effect.descendant.artifactId}:${effect.descendant.version}`}><code>{shortIdentifier(effect.trigger.artifactId)} v{effect.trigger.version}</code> → <code>{shortIdentifier(effect.descendant.artifactId)} v{effect.descendant.version}</code><small>{humanize(effect.freshness)} · {formatDate(effect.createdAt)}</small></li>)}</ul></details> : <small>No affected artifact lineage recorded.</small>}</li>)}</ol></section>;
}

function SafeTextReader({ state, label }: { state?: SafeTextState; label: string }) {
  if (!state || state.kind === "loading") return <div className="review-reader-loading" aria-busy="true">Loading {label} safe representation…</div>;
  if (state.kind === "unavailable") return <ReaderUnavailable reason={state.reason} version={state.version} />;
  if (state.kind === "error") return <div className="review-reader-unavailable" role="alert"><h2>Safe preview unavailable</h2><p>{state.message}</p></div>;
  return <article className="safe-text-reader"><header><strong>{label}</strong><span>{humanize(state.disclosure)} · {state.mediaType}{state.truncated ? " · truncated" : ""}</span></header>{state.truncated && <p className="review-reader-warning">This safe representation is truncated. The decision remains bound to the full candidate digest shown beside the reader.</p>}<pre tabIndex={0}>{state.text}</pre></article>;
}
function ReaderUnavailable({ reason, version }: { reason: "withheld" | "unsupported" | "too_large" | "missing"; version: number }) { return <div className="review-reader-unavailable"><h2>Safe preview unavailable</h2><p>{reason === "withheld" ? `Revision ${version} content is withheld by inspection policy.` : reason === "unsupported" ? `Revision ${version} has no supported plain-text or escaped-markdown representation.` : reason === "too_large" ? "The exact safe content or comparison exceeds the bounded review policy." : "No safe derived representation is recorded for this exact revision."}</p><strong>Raw HTML and original-content fallback are disabled.</strong></div>; }

function readFeedbackSets(session: ReviewSession): FeedbackSetView[] {
  const turns = (session as unknown as { turns?: unknown }).turns;
  if (!Array.isArray(turns)) return [];
  const values = turns.flatMap((turn) => turn && typeof turn === "object" && "feedbackSet" in turn ? [(turn as { feedbackSet: unknown }).feedbackSet] : []);
  return values.flatMap((value): FeedbackSetView[] => {
    if (!value || typeof value !== "object") return [];
    const item = value as Record<string, unknown>;
    const candidate = item.candidate as Schemas["ArtifactVersionRef"] | undefined;
    const representation = item.representation as FeedbackSetView["representation"] | undefined;
    if (typeof item.id !== "string" || (item.state !== "draft" && item.state !== "submitted") || !Array.isArray(item.annotations) || !candidate || typeof candidate.artifactId !== "string" || !representation || typeof representation.representationId !== "string" || typeof representation.digest !== "string" || typeof item.candidateDigest !== "string") return [];
    return [{ id: item.id, state: item.state, candidate, candidateDigest: item.candidateDigest, representation, overallInstruction: typeof item.overallInstruction === "string" ? item.overallInstruction : undefined, annotations: item.annotations as FeedbackAnnotationDraft[], submittedAt: typeof item.submittedAt === "string" ? item.submittedAt : undefined }];
  });
}

async function submitAnnotationFeedbackSet(session: ReviewSession, representation: Extract<SafeTextState, { kind: "available" }> | undefined, overallInstruction: string, annotations: readonly FeedbackAnnotationDraft[], signal: AbortSignal): Promise<{ session: ReviewSession; set: FeedbackSetView }> {
  if (!representation || representation.version !== session.candidate.version || representation.artifactDigest !== session.candidateDigest) throw new Error("The exact safe representation is unavailable for annotation submission.");
  const instruction = overallInstruction.trim();
  if (!instruction) throw new Error("Add an overall instruction before submitting this feedback set.");
  if (new TextEncoder().encode(instruction).length > 16_384) throw new Error("The overall instruction must contain at most 16384 UTF-8 bytes.");
  if (annotations.length > 256) throw new Error("A feedback set can contain at most 256 annotations.");
  const normalizedAnnotations = annotations.map((item) => ({ ...item, comment: validateAnnotationComment(item.comment) }));
  const created = await apiClient.createCheckpointFeedbackSet(session.id, session.resourceVersion, `dashboard-feedback-set-create-${crypto.randomUUID()}`, {
    candidate: session.candidate, candidateDigest: session.candidateDigest, scopeDigest: session.scopeDigest, policyDigest: session.policyDigest,
    representation: { representationId: representation.representationId, digest: representation.representationDigest, disclosure: representation.disclosure },
  }, signal);
  if (typeof created.id !== "string" || created.state !== "draft") throw new Error("The server did not return the exact draft feedback set.");
  const binding = { representationId: representation.representationId, digest: representation.representationDigest, disclosure: representation.disclosure };
  const result = await apiClient.submitCheckpointFeedbackSet(session.id, created.id, session.resourceVersion, `dashboard-feedback-set-submit-${crypto.randomUUID()}`, { feedbackSetId: created.id, candidate: session.candidate, candidateDigest: session.candidateDigest, scopeDigest: session.scopeDigest, policyDigest: session.policyDigest, representation: binding, overallInstruction: instruction, annotations: normalizedAnnotations }, signal);
  const submitted = readFeedbackSets(result).at(-1);
  if (!submitted || submitted.id !== created.id || submitted.state !== "submitted") throw new Error("The server did not return the submitted feedback set receipt.");
  return { session: result, set: submitted };
}

function diffExpectation(from: DecodedArtifactView, to: DecodedArtifactView): DiffExpectation {
  return {
    artifactId: to.artifact.artifactId, from: from.artifact.version, to: to.artifact.version, fromDigest: from.artifact.blobDigest, toDigest: to.artifact.blobDigest,
    fromRepresentations: safeDiffRepresentations(from),
    toRepresentations: safeDiffRepresentations(to),
  };
}

function safeDiffRepresentations(value: DecodedArtifactView) {
  const blockedArtifact = value.artifact.status !== "stored" || value.artifact.sensitivity === "unknown";
  if (blockedArtifact) return [];
  const confidential = value.artifact.sensitivity === "sensitive" || value.artifact.sensitivity === "secret";
  return value.representations.filter((item) => {
    const [mediaType, ...parameters] = item.mediaType.split(";").map((part) => part.trim().toLowerCase());
    const charset = parameters.find((part) => part.startsWith("charset="))?.slice("charset=".length).replace(/^"|"$/g, "");
    return (item.representationKind === "text" || item.representationKind === "preview") && (mediaType === "text/plain" || mediaType === "text/markdown") && (!charset || charset === "utf-8") && !item.truncated && item.size >= 0 && item.size <= 2_097_152 && item.disclosure !== "withheld" && !(confidential && item.disclosure === "raw");
  }).map((item) => ({ representationId: item.representationId, digest: item.digest, representationKind: item.representationKind as "text" | "preview", mediaType: item.mediaType, disclosure: item.disclosure as "raw" | "redacted" }));
}

async function loadSafeText(value: DecodedArtifactView, signal: AbortSignal): Promise<SafeTextState> {
  const { artifact } = value;
  const version = artifact.version;
  try {
    const choice = chooseSafeTextRepresentation({ artifactId: artifact.artifactId, version, status: artifact.status, sensitivity: artifact.sensitivity }, await apiClient.listArtifactRepresentations(artifact.artifactId, version, signal));
    if (choice.kind === "unavailable") return { kind: "unavailable", version, reason: choice.reason };
    const recorded = value.representations.find((item) => item.representationId === choice.representation.representationId);
    if (!recorded || recorded.digest !== choice.representation.digest || recorded.representationKind !== choice.representation.representationKind || recorded.mediaType !== choice.representation.mediaType || recorded.size !== choice.representation.size || recorded.truncated !== choice.representation.truncated || recorded.disclosure !== choice.representation.disclosure) return { kind: "error", version, message: "The safe representation is not bound to the listed artifact evidence." };
    const content = await apiClient.readRepresentationContent(choice.representation.representationId, signal);
    if (!representationContentDigestMatches(content.digest, choice.representation.digest)) return { kind: "error", version, message: "The safe representation response omitted or changed its exact digest." };
    if (content.blob.size > 2_097_152) return { kind: "unavailable", version, reason: "too_large" };
    if (content.blob.size !== choice.representation.size) return { kind: "error", version, message: "The safe representation size changed during review." };
    return { kind: "available", version, artifactDigest: artifact.blobDigest, representationId: choice.representation.representationId, representationDigest: choice.representation.digest, text: await content.blob.text(), disclosure: choice.representation.disclosure as "raw" | "redacted", truncated: choice.representation.truncated, mediaType: choice.representation.mediaType };
  } catch { return { kind: "error", version, message: "The exact safe representation could not be loaded." }; }
}
