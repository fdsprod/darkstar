import type { components } from "../api/schema.generated";

type Schemas = components["schemas"];
export type ReviewView = "current" | "prior" | "inline" | "split";
export type ReviewAction = "approve" | "request_revisions" | "reject";
export type CandidateState = { kind: "current" } | { kind: "stale"; reason: "resource_changed" | "candidate_superseded" };
export type SafeTextState =
  | { kind: "loading"; version: number }
  | { kind: "available"; version: number; artifactDigest: string; text: string; disclosure: "raw" | "redacted"; truncated: boolean; mediaType: string }
  | { kind: "unavailable"; version: number; reason: "withheld" | "unsupported" | "too_large" | "missing" }
  | { kind: "error"; version: number; message: string };
export type DiffState =
  | { kind: "not_applicable"; reason: "no_prior" }
  | { kind: "loading"; from: number; to: number }
  | { kind: "available"; value: Schemas["ArtifactVersionDiffWithText"] }
  | { kind: "unavailable"; from: number; to: number; reason: "unsupported" | "withheld" | "too_large" }
  | { kind: "error"; from: number; to: number; message: string };
export interface SafeArtifactSubject { artifactId: string; version: number; status: "stored" | "stored_uninspectable" | "quarantined"; sensitivity: "unknown" | "public" | "internal" | "sensitive" | "secret" }
export interface DiffExpectation {
  artifactId: string; from: number; to: number; fromDigest: string; toDigest: string;
  fromRepresentations: ReadonlyArray<{ representationId: string; digest: string; representationKind: "text" | "preview"; mediaType: string; disclosure: "raw" | "redacted" }>;
  toRepresentations: ReadonlyArray<{ representationId: string; digest: string; representationKind: "text" | "preview"; mediaType: string; disclosure: "raw" | "redacted" }>;
}
export type IterationStage = "feedback_recorded" | "awaiting_dispatch" | "queued" | "iteration_recorded" | "running" | "validating" | "new_candidate_ready" | "failed" | "cancelled";
export interface IterationActivity { stage: IterationStage; occurredAt?: string; attemptId?: string; message: string }

export function parseReviewView(value: string | null): ReviewView {
  return value === "prior" || value === "inline" || value === "split" ? value : "current";
}

export function buildReviewFeedback(session: Schemas["CheckpointReviewSession"], message: string): Schemas["CheckpointFeedbackRequest"] {
  const normalized = message.trim();
  if (session.state !== "awaiting_human" || !session.allowedActions.includes("request_changes")) throw new Error("This exact candidate no longer accepts revision feedback.");
  if (!normalized) throw new Error("Describe the revisions required for this candidate.");
  if (new TextEncoder().encode(normalized).length > 16_384) throw new Error("Revision feedback must contain at most 16384 bytes.");
  return { candidateDigest: session.candidateDigest, scopeDigest: session.scopeDigest, message: normalized };
}

export function buildReviewDecision(session: Schemas["CheckpointReviewSession"], action: "approve" | "reject", comment: string): Schemas["CheckpointReviewDecisionRequest"] {
  if (session.state !== "awaiting_human" || !session.allowedActions.includes(action)) throw new Error("This exact candidate no longer accepts that decision.");
  const normalized = comment.trim();
  if (new TextEncoder().encode(normalized).length > 4096) throw new Error("Decision comments must contain at most 4096 bytes.");
  const binding = { candidateDigest: session.candidateDigest, scopeDigest: session.scopeDigest, policyDigest: session.policyDigest };
  if (action === "reject") {
    if (!normalized) throw new Error("Explain why this exact candidate is rejected.");
    return { ...binding, action, comment: normalized };
  }
  return normalized ? { ...binding, action, comment: normalized } : { ...binding, action };
}

export function reviewSessionChanged(left: Schemas["CheckpointReviewSession"], right: Schemas["CheckpointReviewSession"]): boolean {
  return left.id !== right.id || left.revision !== right.revision || left.resourceVersion !== right.resourceVersion || left.state !== right.state ||
    left.candidate.artifactId !== right.candidate.artifactId || left.candidate.version !== right.candidate.version || left.candidateDigest !== right.candidateDigest ||
    left.scopeDigest !== right.scopeDigest || left.policyDigest !== right.policyDigest;
}

export function orderedReviewSessions(sessions: readonly Schemas["CheckpointReviewSession"][]): Schemas["CheckpointReviewSession"][] {
  const result = sessions.map((session) => structuredClone(session)).sort((left, right) => left.revision - right.revision || left.id.localeCompare(right.id));
  if (new Set(result.map((session) => session.revision)).size !== result.length) throw new Error("Review history contains duplicate revisions.");
  return result;
}

export function nextReviewSession(history: Schemas["CheckpointReviewHistory"], current: Schemas["CheckpointReviewSession"]) {
  return orderedReviewSessions(history.sessions).find((session) => session.revision > current.revision);
}

export function previousReviewedVersion(history: Schemas["CheckpointReviewHistory"], current: Schemas["CheckpointReviewSession"]): number | undefined {
  return orderedReviewSessions(history.sessions).filter((session) => session.revision < current.revision && session.candidate.artifactId === current.candidate.artifactId).at(-1)?.candidate.version;
}

export function verifyCandidateArtifact<T extends { artifact: { artifactId: string; version: number; blobDigest: string } }>(candidate: Schemas["ArtifactVersionRef"], candidateDigest: string, values: readonly T[]): T {
  const exact = values.find((value) => value.artifact.artifactId === candidate.artifactId && value.artifact.version === candidate.version);
  if (!exact) throw new Error("The exact review candidate is absent from artifact history.");
  if (exact.artifact.blobDigest !== candidateDigest) throw new Error("The artifact digest does not match the exact review candidate.");
  return exact;
}

export function chooseSafeTextRepresentation(subject: SafeArtifactSubject, values: readonly Schemas["ArtifactRepresentation"][]):
  | { kind: "available"; representation: Schemas["ArtifactRepresentation"] }
  | { kind: "unavailable"; reason: "withheld" | "unsupported" | "too_large" | "missing" } {
  if (values.some((value) => value.artifact.artifactId !== subject.artifactId || value.artifact.version !== subject.version)) throw new Error("Safe representation identity does not match the exact review candidate.");
  if (subject.status !== "stored" || subject.sensitivity === "unknown") return { kind: "unavailable", reason: "withheld" };
  if (!values.length) return { kind: "unavailable", reason: "missing" };
  const textCandidates = values.filter((value) => safeTextMediaType(value.mediaType) && (value.representationKind === "text" || value.representationKind === "preview"));
  const disclosureAllowed = (value: Schemas["ArtifactRepresentation"]) => value.disclosure === "redacted" || (value.disclosure === "raw" && subject.sensitivity !== "sensitive" && subject.sensitivity !== "secret");
  const readable = textCandidates.filter(disclosureAllowed);
  const preferred = readable.find((value) => value.disclosure === "redacted" && value.representationKind === "text") ?? readable.find((value) => value.disclosure === "redacted" && value.representationKind === "preview") ?? readable.find((value) => value.representationKind === "text") ?? readable.find((value) => value.representationKind === "preview");
  if (preferred && (preferred.size < 0 || preferred.size > 2_097_152)) return { kind: "unavailable", reason: "too_large" };
  if (preferred) return { kind: "available", representation: structuredClone(preferred) };
  if (textCandidates.some((value) => value.disclosure === "withheld" || !disclosureAllowed(value))) return { kind: "unavailable", reason: "withheld" };
  return { kind: "unavailable", reason: "unsupported" };
}

export function representationContentDigestMatches(headerDigest: string | undefined, representationDigest: string): boolean {
  return headerDigest === `sha256=${representationDigest}`;
}

export function iterationActivity(session: Schemas["CheckpointReviewSession"], agent?: Schemas["Agent"], newer?: Schemas["CheckpointReviewSession"]): IterationActivity[] {
  const result: IterationActivity[] = [];
  const feedback = [...session.turns].reverse().find((turn) => turn.kind === "human_feedback");
  if (feedback?.kind === "human_feedback") result.push({ stage: "feedback_recorded", occurredAt: feedback.occurredAt, attemptId: feedback.attemptId, message: "Revision feedback was durably recorded." });
  if (session.state === "awaiting_agent" && !session.activeIteration) result.push({ stage: "awaiting_dispatch", occurredAt: session.updatedAt, message: feedback ? "Feedback is recorded and awaiting dispatch; no revision attempt is recorded yet." : "The review session is awaiting dispatch; no revision attempt is recorded yet." });
  if (session.activeIteration && !agent) result.push({ stage: "iteration_recorded", occurredAt: session.activeIteration.resumedAt, attemptId: session.activeIteration.attemptId, message: "A revision attempt is recorded; its agent projection is not available." });
  if (session.activeIteration && agent?.attemptId === session.activeIteration.attemptId) {
    const common = { attemptId: agent.attemptId, occurredAt: agent.updatedAt };
    if (agent.status === "created" || agent.status === "starting") result.push({ ...common, stage: "queued", message: "The recorded revision attempt is queued or starting." });
    else if (agent.status === "running") result.push({ ...common, stage: "running", message: "The recorded revision attempt is running." });
    else if (agent.status === "validating") result.push({ ...common, stage: "validating", message: "The recorded revision attempt is validating." });
    else if (agent.status === "failed" || agent.status === "interrupted" || agent.status === "reconcile_required") result.push({ ...common, stage: "failed", message: "The recorded revision attempt failed or requires reconciliation." });
    else if (agent.status === "cancelled") result.push({ ...common, stage: "cancelled", message: "The recorded revision attempt was cancelled." });
  }
  const response = [...session.turns].reverse().find((turn) => turn.kind === "agent_response");
  if (response?.kind === "agent_response" && response.outcome === "failed" && !result.some((item) => item.stage === "failed")) result.push({ stage: "failed", occurredAt: response.occurredAt, attemptId: response.attemptId, message: response.message ?? "The agent reported that revision failed." });
  if (response?.kind === "agent_response" && response.outcome === "cancelled" && !result.some((item) => item.stage === "cancelled")) result.push({ stage: "cancelled", occurredAt: response.occurredAt, attemptId: response.attemptId, message: response.message ?? "The agent reported that revision was cancelled." });
  if (newer) result.push({ stage: "new_candidate_ready", occurredAt: newer.createdAt, message: `Candidate revision ${newer.revision} is ready for review.` });
  return result;
}

export function splitDiffRows(diff: Schemas["ArtifactVersionDiffWithText"]): Array<{ kind: "unchanged" | "changed" | "omitted"; left: string; right: string; leftLine?: number; rightLine?: number }> {
  if (diff.textDiff.status !== "available") return [];
  return diff.textDiff.hunks.flatMap((hunk) => {
    const rows: Array<{ kind: "unchanged" | "changed" | "omitted"; left: string; right: string; leftLine?: number; rightLine?: number }> = [];
    const entries = hunk.entries;
    for (let index = 0; index < entries.length; index += 1) {
      const entry = entries[index];
      if (entry.kind === "unchanged") rows.push({ kind: "unchanged", left: entry.text, right: entry.text, leftLine: entry.fromLine, rightLine: entry.toLine });
      else if (entry.kind === "omitted") rows.push({ kind: "omitted", left: `${entry.omittedLines} lines omitted`, right: `${entry.omittedLines} lines omitted`, leftLine: entry.fromLine, rightLine: entry.toLine });
      else if (entry.kind === "removed" && entries[index + 1]?.kind === "added") { const added = entries[++index]; rows.push({ kind: "changed", left: entry.text, right: added.kind === "added" ? added.text : "", leftLine: entry.fromLine, ...(added.kind === "added" ? { rightLine: added.toLine } : {}) }); }
      else if (entry.kind === "removed") rows.push({ kind: "changed", left: entry.text, right: "", leftLine: entry.fromLine });
      else rows.push({ kind: "changed", left: "", right: entry.text, rightLine: entry.toLine });
    }
    return rows;
  });
}

export function validateArtifactDiff(expected: DiffExpectation, value: Schemas["ArtifactVersionDiffWithText"]): Schemas["ArtifactVersionDiffWithText"] {
  if (value.artifactId !== expected.artifactId || value.from !== expected.from || value.to !== expected.to || value.fromDigest !== expected.fromDigest || value.toDigest !== expected.toDigest) throw new Error("Diff response does not match the exact artifact versions.");
  if (value.textDiff.status === "available") {
    const { from, to } = value.textDiff;
    const exactFrom = from.artifact.artifactId === expected.artifactId && from.artifact.version === expected.from && expected.fromRepresentations.some((item) => item.representationId === from.representationId && item.digest === from.digest && item.representationKind === from.representationKind && item.mediaType === from.mediaType && item.disclosure === from.disclosure);
    const exactTo = to.artifact.artifactId === expected.artifactId && to.artifact.version === expected.to && expected.toRepresentations.some((item) => item.representationId === to.representationId && item.digest === to.digest && item.representationKind === to.representationKind && item.mediaType === to.mediaType && item.disclosure === to.disclosure);
    const policy = value.textDiff.policy;
    const exactPolicy = policy.algorithm === "darkstar-line-dp/v1" && policy.contextLines === 3 && policy.maxInputBytes === 2_097_152 && policy.maxWorkUnits === 1_000_000 && policy.maxPageBytes === 3_145_728 && Number.isSafeInteger(policy.pageSize) && policy.pageSize > 0 && policy.pageSize <= 200;
    if (!exactFrom || !exactTo || !exactPolicy || !Number.isSafeInteger(value.textDiff.totalEntries) || value.textDiff.totalEntries < 0 || !/^[0-9a-f]{64}$/.test(value.textDiff.policyDigest) || !/^[0-9a-f]{64}$/.test(value.textDiff.resultDigest)) throw new Error("Diff representation evidence does not match the exact artifact versions.");
  }
  return structuredClone(value);
}

export function mergeDiffPages(current: Schemas["ArtifactVersionDiffWithText"], next: Schemas["ArtifactVersionDiffWithText"], consumedCursor: string): Schemas["ArtifactVersionDiffWithText"] {
  if (current.artifactId !== next.artifactId || current.from !== next.from || current.to !== next.to || current.fromDigest !== next.fromDigest || current.toDigest !== next.toDigest ||
      current.textDiff.status !== "available" || next.textDiff.status !== "available" || current.textDiff.policyDigest !== next.textDiff.policyDigest || current.textDiff.resultDigest !== next.textDiff.resultDigest ||
      current.textDiff.from.representationId !== next.textDiff.from.representationId || current.textDiff.from.digest !== next.textDiff.from.digest || current.textDiff.from.artifact.artifactId !== next.textDiff.from.artifact.artifactId || current.textDiff.from.artifact.version !== next.textDiff.from.artifact.version ||
      current.textDiff.from.representationKind !== next.textDiff.from.representationKind || current.textDiff.from.mediaType !== next.textDiff.from.mediaType || current.textDiff.from.disclosure !== next.textDiff.from.disclosure ||
      current.textDiff.to.representationId !== next.textDiff.to.representationId || current.textDiff.to.digest !== next.textDiff.to.digest || current.textDiff.to.artifact.artifactId !== next.textDiff.to.artifact.artifactId || current.textDiff.to.artifact.version !== next.textDiff.to.artifact.version || current.textDiff.to.representationKind !== next.textDiff.to.representationKind || current.textDiff.to.mediaType !== next.textDiff.to.mediaType || current.textDiff.to.disclosure !== next.textDiff.to.disclosure ||
      current.textDiff.totalEntries !== next.textDiff.totalEntries || current.textDiff.nextCursor !== consumedCursor || next.textDiff.nextCursor === consumedCursor || JSON.stringify(current.textDiff.policy) !== JSON.stringify(next.textDiff.policy) || JSON.stringify(current.changed) !== JSON.stringify(next.changed) || JSON.stringify(current.representations) !== JSON.stringify(next.representations)) throw new Error("Diff continuation does not match the exact bounded comparison.");
  return { ...structuredClone(next), textDiff: { ...structuredClone(next.textDiff), hunks: [...structuredClone(current.textDiff.hunks), ...structuredClone(next.textDiff.hunks)] } };
}

function safeTextMediaType(mediaType: string) {
  const [base, ...parameters] = mediaType.split(";").map((value) => value.trim().toLowerCase());
  const charset = parameters.find((value) => value.startsWith("charset="))?.slice("charset=".length).replace(/^"|"$/g, "");
  return (base === "text/plain" || base === "text/markdown") && (!charset || charset === "utf-8");
}
