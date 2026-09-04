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
  freshness: "current" | "stale" | "invalid";
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

export function decodeArtifactView(value: unknown): DecodedArtifactView {
  const view = record(value, "artifact view");
  const artifact = decodeArtifact(view.artifact);
  const freshness = oneOf(view.freshness, ["current", "stale", "invalid"] as const, "artifact freshness");
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

export function previousRevision(revisions: readonly DecodedArtifactView[], selectedVersion: number) {
  return revisions.find((value) => value.artifact.version < selectedVersion);
}

function decodeArtifact(value: unknown): ArtifactRecord {
  const artifact = record(value, "artifact");
  if (artifact.kind !== "artifact" || artifact.schemaVersion !== 1) throw new Error("Unsupported artifact schema.");
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
  if (provenance.origin === "attempt") return {
    origin: "attempt", runId: text(provenance.runId, "provenance run"), nodeId: text(provenance.nodeId, "provenance node"),
    attemptId: text(provenance.attemptId, "provenance attempt"), operationId: text(provenance.operationId, "provenance operation"),
    ...(typeof provenance.sourceArtifactId === "string" ? { sourceArtifactId: provenance.sourceArtifactId } : {}),
  };
  if (provenance.origin === "operation") return {
    origin: "operation", operationId: text(provenance.operationId, "provenance operation"),
    ...(typeof provenance.sourceArtifactId === "string" ? { sourceArtifactId: provenance.sourceArtifactId } : {}),
  };
  throw new Error("Artifact provenance has an unsupported origin.");
}

function decodeRepresentation(value: unknown): ArtifactRepresentation {
  const representation = record(value, "artifact representation");
  if (representation.kind !== "representation" || representation.schemaVersion !== 1) throw new Error("Unsupported representation schema.");
  return {
    kind: "representation", schemaVersion: 1,
    representationId: text(representation.representationId, "representation ID"), artifactId: text(representation.artifactId, "representation artifact ID"),
    representationKind: oneOf(representation.representationKind, ["text", "structured", "table", "image", "preview", "descriptor"] as const, "representation kind"),
    processorName: text(representation.processorName, "processor name"), processorVersion: text(representation.processorVersion, "processor version"),
    mediaType: text(representation.mediaType, "representation media type"), locator: text(representation.locator, "representation locator"), digest: text(representation.digest, "representation digest"),
    size: nonnegativeInteger(representation.size, "representation size"), tokenEstimate: nonnegativeInteger(representation.tokenEstimate, "token estimate"),
    truncated: boolean(representation.truncated, "truncated"), disclosure: oneOf(representation.disclosure, ["raw", "redacted", "withheld"] as const, "disclosure"),
    diagnostics: array(representation.diagnostics, "representation diagnostics").map((item) => text(item, "representation diagnostic")), createdAt: text(representation.createdAt, "representation creation time"),
  };
}

function utf8Base64(value: string) {
  const bytes = new TextEncoder().encode(value);
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
