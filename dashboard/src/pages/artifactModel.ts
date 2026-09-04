import type { components } from "../api/schema.generated";

type Schemas = components["schemas"];

export type ArtifactProvenance =
  | { origin: "attempt"; runId: string; nodeId: string; attemptId: string; operationId: string; sourceArtifactId?: string }
  | { origin: "operation"; operationId: string; sourceArtifactId?: string };

export interface ArtifactRecord {
  kind: "artifact";
  schemaVersion: 1;
  artifactId: string;
  version: number;
  sourceKind: "file" | "paste" | "stdin" | "generated" | "external";
  sourceName: string;
  blobDigest: string;
  size: number;
  declaredMediaType: string;
  detectedMediaType: string;
  locator: string;
  sensitivity: "unknown" | "public" | "internal" | "sensitive" | "secret";
  trust: "untrusted";
  creator: string;
  status: "stored" | "stored_uninspectable" | "quarantined";
  createdAt: string;
  roles: string[];
  tags: string[];
  metadata: Record<string, string>;
  provenance: ArtifactProvenance;
}

export interface ArtifactRepresentation {
  kind: "representation";
  schemaVersion: 1;
  representationId: string;
  artifactId: string;
  representationKind: "text" | "structured" | "table" | "image" | "preview" | "descriptor";
  processorName: string;
  processorVersion: string;
  mediaType: string;
  locator: string;
  digest: string;
  size: number;
  tokenEstimate: number;
  truncated: boolean;
  disclosure: "raw" | "redacted" | "withheld";
  diagnostics: string[];
  createdAt: string;
}

export interface DecodedArtifactView {
  artifact: ArtifactRecord;
  freshness: "current" | "potentially_stale" | "invalidated";
  representations: ArtifactRepresentation[];
}

export interface ArtifactRevisionInput {
  sourceName: string;
  mediaType: string;
  content: string;
  sensitivity: ArtifactRecord["sensitivity"];
  roles: string;
  tags: string;
}

export type ArtifactTargetKind = "work" | "run" | "node" | "checkpoint" | "story" | "implementation_point";
export interface ArtifactTarget { kind: ArtifactTargetKind; id: string }
export type ArtifactIngestSource =
  | { kind: "paste"; sourceName: string; mediaType: string; content: string }
  | { kind: "file"; file: File };
export interface ArtifactIngestInput {
  source: ArtifactIngestSource;
  sensitivity: ArtifactRecord["sensitivity"];
  roles: string;
  tags: string;
}

export type ImpactProposal =
  | { action: "continue"; reason: string }
  | { action: "refresh"; attemptId: string; reason: string }
  | { action: "revise" | "invalidate"; artifacts: Array<{ artifact: { artifactId: string; version: number }; freshness: "potentially_stale" | "invalidated" }>; reason: string }
  | { action: "insert"; runId: string; target: ArtifactTarget; roles: string[]; reason: string };
export interface ArtifactImpactAssessment {
  kind: "impact_assessment";
  schemaVersion: 1;
  evidence: { artifactId: string; version: number };
  target: ArtifactTarget;
  runId?: string;
  roles: string[];
  coverage: Array<{ attemptId: string; nodeId: string; manifestId?: string; state: "supplied" | "pending_freeze" | "not_supplied" | "unavailable" }>;
  proposals: ImpactProposal[];
}

export function decodeArtifactView(value: unknown): DecodedArtifactView {
  const view = record(value, "artifact view");
  const artifact = decodeArtifact(view.artifact);
  const freshness = oneOf(view.freshness, ["current", "potentially_stale", "invalidated"] as const, "artifact freshness");
  const representations = array(view.representations, "artifact representations").map(decodeRepresentation);
  if (representations.some((item) => item.artifactId !== artifact.artifactId)) throw new Error("Artifact representation identity does not match its artifact.");
  return { artifact, freshness, representations };
}

export function decodeArtifactViews(value: unknown): DecodedArtifactView[] {
  return array(value, "artifact list").map(decodeArtifactView);
}

export function revisionsForArtifact(values: readonly DecodedArtifactView[], artifactId: string) {
  const revisions = values.filter((value) => value.artifact.artifactId === artifactId).sort((left, right) => right.artifact.version - left.artifact.version);
  const seen = new Set<number>();
  for (const value of revisions) {
    if (seen.has(value.artifact.version)) throw new Error(`Artifact ${artifactId} contains duplicate revision ${value.artifact.version}.`);
    seen.add(value.artifact.version);
  }
  return revisions;
}

export function buildArtifactRevisionRequest(input: ArtifactRevisionInput): Schemas["ArtifactIngestRequest"] {
  const sourceName = input.sourceName.trim();
  const mediaType = input.mediaType.trim().toLowerCase();
  if (!sourceName) throw new Error("Source name is required.");
  if (!mediaType || !mediaType.includes("/")) throw new Error("A valid media type is required.");
  return {
    sourceKind: "paste",
    sourceName,
    mediaType,
    content: utf8Base64(input.content),
    sensitivity: input.sensitivity,
    roles: uniqueTokens(input.roles),
    tags: uniqueTokens(input.tags),
  };
}

export async function buildArtifactIngestRequest(input: ArtifactIngestInput): Promise<Schemas["ArtifactIngestRequest"]> {
  if (input.source.kind === "paste") {
    const sourceName = input.source.sourceName.trim();
    const mediaType = input.source.mediaType.trim().toLowerCase();
    if (!sourceName) throw new Error("Source name is required.");
    if (!mediaType || !mediaType.includes("/")) throw new Error("A valid media type is required.");
    if (!input.source.content.trim()) throw new Error("Paste evidence content before storing it.");
    return { sourceKind: "paste", sourceName, mediaType, content: utf8Base64(input.source.content), sensitivity: input.sensitivity, roles: uniqueTokens(input.roles), tags: uniqueTokens(input.tags) };
  }
  if (input.source.file.size > 25 * 1024 * 1024) throw new Error("Artifact exceeds the 25 MiB dashboard limit.");
  const bytes = new Uint8Array(await input.source.file.arrayBuffer());
  return { sourceKind: "file", sourceName: input.source.file.name, mediaType: input.source.file.type || "application/octet-stream", content: bytesBase64(bytes), sensitivity: input.sensitivity, roles: uniqueTokens(input.roles), tags: uniqueTokens(input.tags) };
}

export function buildArtifactTarget(kind: string, id: string): ArtifactTarget {
  const kinds = ["work", "run", "node", "checkpoint", "story", "implementation_point"] as const;
  if (!kinds.includes(kind as ArtifactTargetKind)) throw new Error("Choose a supported evidence target.");
  if (!id.trim()) throw new Error("Target identifier is required.");
  return { kind: kind as ArtifactTargetKind, id: id.trim() };
}

export function decodeArtifactImpact(value: unknown): ArtifactImpactAssessment {
  const assessment = record(value, "impact assessment");
  if (assessment.kind !== "impact_assessment" || assessment.schemaVersion !== 1) throw new Error("Unsupported impact assessment schema.");
  const evidence = decodeVersionRef(assessment.evidence, "impact evidence");
  const target = decodeTarget(assessment.target);
  const coverage = array(assessment.coverage, "impact coverage").map((entry) => {
    const item = record(entry, "attempt coverage");
    const state = oneOf(item.state, ["supplied", "pending_freeze", "not_supplied", "unavailable"] as const, "coverage state");
    return { attemptId: text(item.attemptId, "coverage attempt"), nodeId: text(item.nodeId, "coverage node"), ...(typeof item.manifestId === "string" ? { manifestId: item.manifestId } : {}), state };
  });
  const proposals = array(assessment.proposals, "impact proposals").map(decodeImpactProposal);
  if (!proposals.length) throw new Error("Impact assessment must contain a proposal.");
  return { kind: "impact_assessment", schemaVersion: 1, evidence, target, ...(typeof assessment.runId === "string" ? { runId: assessment.runId } : {}), roles: optionalStrings(assessment.roles), coverage, proposals };
}

export function previousRevision(revisions: readonly DecodedArtifactView[], selectedVersion: number) {
  return revisions.find((value) => value.artifact.version < selectedVersion);
}

function decodeArtifact(value: unknown): ArtifactRecord {
  const artifact = record(value, "artifact");
  if ((artifact.kind !== undefined && artifact.kind !== "artifact") || (artifact.schemaVersion !== undefined && artifact.schemaVersion !== 1)) throw new Error("Unsupported artifact schema.");
  const artifactId = text(artifact.artifactId, "artifact ID");
  if (!artifactId.startsWith("artifact_")) throw new Error("Artifact ID is invalid.");
  const version = positiveInteger(artifact.version, "artifact version");
  const provenance = decodeProvenance(artifact.provenance);
  return {
    kind: "artifact", schemaVersion: 1, artifactId, version,
    sourceKind: oneOf(artifact.sourceKind, ["file", "paste", "stdin", "generated", "external"] as const, "source kind"),
    sourceName: text(artifact.sourceName, "source name"), blobDigest: text(artifact.blobDigest, "blob digest"), size: nonnegativeInteger(artifact.size, "artifact size"),
    declaredMediaType: text(artifact.declaredMediaType, "declared media type"), detectedMediaType: text(artifact.detectedMediaType, "detected media type"),
    locator: text(artifact.locator, "artifact locator"), sensitivity: oneOf(artifact.sensitivity, ["unknown", "public", "internal", "sensitive", "secret"] as const, "sensitivity"),
    trust: oneOf(artifact.trust, ["untrusted"] as const, "trust"), creator: text(artifact.creator, "creator"),
    status: oneOf(artifact.status, ["stored", "stored_uninspectable", "quarantined"] as const, "artifact status"), createdAt: text(artifact.createdAt, "creation time"),
    roles: optionalStrings(artifact.roles), tags: optionalStrings(artifact.tags), metadata: optionalStringMap(artifact.metadata), provenance,
  };
}

function decodeProvenance(value: unknown): ArtifactProvenance {
  const provenance = record(value, "artifact provenance");
  if (provenance.origin === "attempt" || (provenance.origin === undefined && typeof provenance.runId === "string")) return {
    origin: "attempt", runId: text(provenance.runId, "provenance run"), nodeId: text(provenance.nodeId, "provenance node"),
    attemptId: text(provenance.attemptId, "provenance attempt"), operationId: text(provenance.operationId, "provenance operation"),
    ...sourceArtifact(provenance),
  };
  if (provenance.origin === "operation" || (provenance.origin === undefined && typeof provenance.operationId === "string")) return {
    origin: "operation", operationId: text(provenance.operationId, "provenance operation"),
    ...sourceArtifact(provenance),
  };
  throw new Error("Artifact provenance has an unsupported origin.");
}

function decodeRepresentation(value: unknown): ArtifactRepresentation {
  const representation = record(value, "artifact representation");
  if ((representation.kind !== undefined && representation.kind !== "representation") || (representation.schemaVersion !== undefined && representation.schemaVersion !== 1)) throw new Error("Unsupported representation schema.");
  const artifactId = typeof representation.artifactId === "string" ? representation.artifactId : decodeVersionRef(representation.artifact, "representation artifact").artifactId;
  const processor = representation.processor === undefined ? undefined : record(representation.processor, "representation processor");
  return {
    kind: "representation", schemaVersion: 1,
    representationId: text(representation.representationId, "representation ID"), artifactId,
    representationKind: oneOf(representation.representationKind, ["text", "structured", "table", "image", "preview", "descriptor"] as const, "representation kind"),
    processorName: text(representation.processorName ?? processor?.name ?? processor?.Name, "processor name"), processorVersion: text(representation.processorVersion ?? processor?.version ?? processor?.Version, "processor version"),
    mediaType: text(representation.mediaType, "representation media type"), locator: text(representation.locator, "representation locator"), digest: text(representation.digest, "representation digest"),
    size: nonnegativeInteger(representation.size, "representation size"), tokenEstimate: nonnegativeInteger(representation.tokenEstimate, "token estimate"),
    truncated: boolean(representation.truncated, "truncated"), disclosure: oneOf(representation.disclosure, ["raw", "redacted", "withheld"] as const, "disclosure"),
    diagnostics: array(representation.diagnostics, "representation diagnostics").map((item) => text(item, "representation diagnostic")), createdAt: text(representation.createdAt, "representation creation time"),
  };
}

function decodeImpactProposal(value: unknown): ImpactProposal {
  const proposal = record(value, "impact proposal");
  const action = oneOf(proposal.action, ["continue", "refresh", "revise", "insert", "invalidate"] as const, "impact action");
  const reason = text(proposal.reason, "impact reason");
  if (action === "continue") return { action, reason };
  if (action === "refresh") return { action, attemptId: text(proposal.attemptId, "refresh attempt"), reason };
  if (action === "insert") return { action, runId: text(proposal.runId, "insert run"), target: decodeTarget(proposal.target), roles: optionalStrings(proposal.roles), reason };
  const artifacts = array(proposal.artifacts, "affected artifacts").map((entry) => { const effect = record(entry, "artifact effect"); return { artifact: decodeVersionRef(effect.artifact, "affected artifact"), freshness: oneOf(effect.freshness, ["potentially_stale", "invalidated"] as const, "effect freshness") }; });
  if (!artifacts.length) throw new Error("Artifact impact proposal must name affected artifacts.");
  return { action, artifacts, reason };
}

function decodeTarget(value: unknown): ArtifactTarget { const target = record(value, "artifact target"); return buildArtifactTarget(text(target.kind, "target kind"), text(target.id, "target ID")); }
function decodeVersionRef(value: unknown, label: string) { const reference = record(value, label); const artifactId = text(reference.artifactId, `${label} ID`); if (!artifactId.startsWith("artifact_")) throw new Error(`${label} ID is invalid.`); return { artifactId, version: positiveInteger(reference.version, `${label} version`) }; }
function sourceArtifact(value: Record<string, unknown>) { if (typeof value.sourceArtifactId === "string") return { sourceArtifactId: value.sourceArtifactId }; if (value.source && typeof value.source === "object" && !Array.isArray(value.source) && typeof (value.source as Record<string, unknown>).artifactId === "string") return { sourceArtifactId: (value.source as Record<string, unknown>).artifactId as string }; return {}; }

function utf8Base64(value: string) {
  return bytesBase64(new TextEncoder().encode(value));
}
function bytesBase64(bytes: Uint8Array) {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}
function uniqueTokens(value: string) { return [...new Set(value.split(",").map((item) => item.trim()).filter(Boolean))].sort(); }
function record(value: unknown, label: string): Record<string, unknown> { if (!value || typeof value !== "object" || Array.isArray(value)) throw new Error(`${label} must be an object.`); return value as Record<string, unknown>; }
function array(value: unknown, label: string): unknown[] { if (!Array.isArray(value)) throw new Error(`${label} must be an array.`); return value; }
function text(value: unknown, label: string): string { if (typeof value !== "string" || value.length === 0) throw new Error(`${label} must be a non-empty string.`); return value; }
function boolean(value: unknown, label: string): boolean { if (typeof value !== "boolean") throw new Error(`${label} must be a boolean.`); return value; }
function positiveInteger(value: unknown, label: string): number { if (!Number.isSafeInteger(value) || Number(value) < 1) throw new Error(`${label} must be a positive integer.`); return Number(value); }
function nonnegativeInteger(value: unknown, label: string): number { if (!Number.isSafeInteger(value) || Number(value) < 0) throw new Error(`${label} must be a nonnegative integer.`); return Number(value); }
function optionalStrings(value: unknown): string[] { return value === undefined ? [] : array(value, "string collection").map((item) => text(item, "collection item")); }
function optionalStringMap(value: unknown): Record<string, string> { if (value === undefined) return {}; const input = record(value, "metadata"); return Object.fromEntries(Object.entries(input).map(([key, item]) => [key, text(item, `metadata ${key}`)])); }
function oneOf<const T extends readonly string[]>(value: unknown, choices: T, label: string): T[number] { if (typeof value !== "string" || !choices.includes(value)) throw new Error(`${label} is unsupported.`); return value as T[number]; }
