export type CheckpointAction = "approve" | "request_changes" | "reject";
export type CheckpointState = "pending" | "approved" | "changes_requested" | "rejected";

export interface ArtifactVersionReference { artifactId: string; version: number }
export interface CheckpointActor { type: "user" | "system" | "provider" | "external"; id: string }
export interface ArtifactInvalidation { trigger: ArtifactVersionReference; descendant: ArtifactVersionReference; freshness: "potentially_stale" | "invalidated"; createdAt: string }
export interface CheckpointDecision { action: CheckpointAction; effect: "accept_candidate" | "start_revision" | "reject_visit"; actor: CheckpointActor; comment?: string; decidedAt: string }

export interface CheckpointRoundIdentity {
  approvalId: string;
  checkpointId: string;
  revision: number;
  state: CheckpointState;
  allowedActions: CheckpointAction[];
  scopeDigest: string;
  policyDigest: string;
  resourceVersion: number;
}

export interface ArtifactCheckpointRound extends CheckpointRoundIdentity {
  runId: string;
  visitId: string;
  nodeId: string;
  attemptId: string;
  candidate: ArtifactVersionReference;
  candidateDigest: string;
  mode: "approve" | "approve_on_change";
  maxRevisions?: number;
  decision?: CheckpointDecision;
  affectedArtifacts: ArtifactInvalidation[];
  createdAt: string;
  updatedAt: string;
}

export interface ArtifactCheckpointQueue { schemaVersion: 1; items: ArtifactCheckpointRound[] }
export interface ArtifactCheckpointHistory { checkpointId: string; rounds: ArtifactCheckpointRound[] }
export type InputRequestAction = "answer" | "retry_delivery";
export interface UserInputQuestion { id: string; prompt: string; options: string[]; schema: { type: "string"; allowedValues: string[] } }
export interface UserInputRequest { questions: UserInputQuestion[] }
export interface UserInputAnswerEnvelope { answers: Record<string, { answers: string }> }
export interface InputRequest {
  id: string;
  runId: string;
  attemptId: string;
  nodeId: string;
  providerRequestId: string;
  scopeDigest: string;
  request: UserInputRequest;
  status: "pending" | "answer_recorded" | "answered";
  allowedActions: InputRequestAction[];
  answer?: { answer: UserInputAnswerEnvelope; actor: CheckpointActor; recordedAt: string };
  receipt?: { providerRequestId: string; deliveredAt: string };
  resourceVersion: number;
  lastGlobalPosition: number;
  createdAt: string;
  updatedAt: string;
}
export interface InputRequestPage { schemaVersion: 1; items: InputRequest[] }

export type CheckpointDecisionRequest =
  | { action: "approve"; scopeDigest: string; policyDigest: string; comment?: string }
  | { action: "request_changes" | "reject"; scopeDigest: string; policyDigest: string; comment: string };

export function checkpointActionPresentation(action: CheckpointAction) {
  switch (action) {
    case "approve": return { label: "Approve candidate", description: "Record intent to accept this exact immutable candidate.", tone: "primary" as const, destructive: false };
    case "request_changes": return { label: "Request changes", description: "Record revision feedback for this round. A new revision is not claimed until it appears durably.", tone: "neutral" as const, destructive: false };
    case "reject": return { label: "Reject candidate", description: "Record intent to reject this checkpoint round. This may cause the owning visit to be rejected downstream.", tone: "danger" as const, destructive: true };
  }
}

export function checkpointActionKey(round: CheckpointRoundIdentity, action: CheckpointAction) {
  return `${round.approvalId}:${round.resourceVersion}:${action}`;
}

export function buildCheckpointDecisionRequest(round: CheckpointRoundIdentity, action: CheckpointAction, comment: string): CheckpointDecisionRequest {
  if (round.state !== "pending") throw new Error("This checkpoint round already has a terminal decision.");
  if (!round.allowedActions.includes(action)) throw new Error("Choose an action currently allowed by the daemon.");
  const normalizedComment = comment.trim();
  if ((action === "request_changes" || action === "reject") && !normalizedComment) throw new Error("Request changes and reject require an explanatory comment.");
  if (normalizedComment.length > 4096) throw new Error("Comments cannot exceed 4096 characters.");
  if (action === "approve") return { action, scopeDigest: round.scopeDigest, policyDigest: round.policyDigest, ...(normalizedComment ? { comment: normalizedComment } : {}) };
  return { action, scopeDigest: round.scopeDigest, policyDigest: round.policyDigest, comment: normalizedComment };
}

export function checkpointRoundChanged(previous: CheckpointRoundIdentity, next: CheckpointRoundIdentity) {
  return previous.approvalId !== next.approvalId || previous.checkpointId !== next.checkpointId || previous.revision !== next.revision || previous.resourceVersion !== next.resourceVersion || previous.state !== next.state || previous.scopeDigest !== next.scopeDigest || previous.policyDigest !== next.policyDigest;
}

export function selectedCheckpointRound<T extends CheckpointRoundIdentity>(rounds: readonly T[], approvalId?: string) {
  if (!rounds.length) return undefined;
  if (approvalId) return rounds.find((round) => round.approvalId === approvalId);
  return rounds[0];
}

export function orderedCheckpointHistory<T extends CheckpointRoundIdentity>(rounds: readonly T[]) {
  const ordered = [...rounds].sort((left, right) => left.revision - right.revision || left.approvalId.localeCompare(right.approvalId));
  const seen = new Set<number>();
  for (const round of ordered) {
    if (seen.has(round.revision)) throw new Error(`Checkpoint ${round.checkpointId} contains duplicate revision ${round.revision}.`);
    seen.add(round.revision);
  }
  return ordered;
}

export function parseJSONAnswer(value: string): UserInputAnswerEnvelope {
  if (!value.trim()) throw new Error("Enter a JSON answer.");
  let parsed: unknown;
  try { parsed = JSON.parse(value); } catch { throw new Error("Answer must be valid JSON."); }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed) || !("answers" in parsed)) throw new Error("Answer must contain an answers object.");
  const answers = (parsed as { answers?: unknown }).answers;
  if (!answers || typeof answers !== "object" || Array.isArray(answers)) throw new Error("Answer must contain an answers object.");
  const result: UserInputAnswerEnvelope = { answers: {} };
  for (const [id, entry] of Object.entries(answers)) {
    if (!entry || typeof entry !== "object" || Array.isArray(entry) || Object.keys(entry).length !== 1 || typeof (entry as { answers?: unknown }).answers !== "string") {
      throw new Error(`Answer ${id} must contain one string value.`);
    }
    result.answers[id] = { answers: (entry as { answers: string }).answers };
  }
  return result;
}

export function buildInputAnswerRequest(input: { status: "pending" | "answer_recorded" | "answered"; scopeDigest: string; request: UserInputRequest }, answerText: string) {
  if (input.status !== "pending") throw new Error(input.status === "answer_recorded" ? "This answer is already recorded and awaiting authoritative delivery." : "This input request has already been delivered.");
  const answer = parseJSONAnswer(answerText);
  if (Object.keys(answer.answers).length !== input.request.questions.length) throw new Error("Answer every recorded question exactly once.");
  for (const question of input.request.questions) {
    const value = answer.answers[question.id]?.answers;
    if (!value || value.length > 128) throw new Error(`Answer ${question.id} with a non-empty string.`);
    if (question.schema.allowedValues.length && !question.schema.allowedValues.includes(value)) throw new Error(`Choose a recorded option for ${question.id}.`);
  }
  return { scopeDigest: input.scopeDigest, answer };
}

export function inputActionPresentation(action: InputRequestAction) {
  switch (action) {
    case "answer": return { label: "Answer request", description: "Record one attributable JSON answer, then let the daemon deliver it to the provider." };
    case "retry_delivery": return { label: "Retry delivery", description: "Ask the daemon to redeliver the already recorded answer without changing it." };
  }
}

export function inputRequestChanged(previous: Pick<InputRequest, "id" | "scopeDigest" | "status" | "resourceVersion">, next: Pick<InputRequest, "id" | "scopeDigest" | "status" | "resourceVersion">) {
  return previous.id !== next.id || previous.scopeDigest !== next.scopeDigest || previous.status !== next.status || previous.resourceVersion !== next.resourceVersion;
}
