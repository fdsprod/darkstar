export interface ContentReference {
  id: string;
  version: string;
  digest: string;
}

export type PromptCondition = { kind: "input_linked" | "input_absent"; input: string } | { kind: "revision" | "always" };
export interface PromptSection {
  id: string;
  when: PromptCondition;
  instructions: string;
}
export type ContentDocument =
  | { kind: "template"; content: string; requiredHeadings?: string[] }
  | { kind: "prompt"; instructions: string; sections?: PromptSection[] };
export interface ContentVersion {
  reference: ContentReference;
  document: ContentDocument;
  createdAt: string;
}
export interface ContentItem {
  id: string;
  name: string;
  description: string;
  kind: ContentDocument["kind"];
  archivedAt?: string;
  draft: { revision: number; document: ContentDocument };
  versions: ContentVersion[];
}
export interface PromptPreview {
  instructions: string;
  sections: { id: string; included: boolean; reason: string }[];
  estimatedTokens: number;
  tokenEstimateMethod: string;
}

export function documentText(document: ContentDocument): string {
  if (document.kind === "template") {
    return document.content;
  }
  return [document.instructions, ...(document.sections ?? []).map((section) => {
    const condition = "input" in section.when ? `${section.when.input}: ${section.when.kind === "input_linked" ? "linked" : "not linked"}` : section.when.kind === "revision" ? "During revision" : "Always";
    return `## ${section.id} — ${condition}\n\n${section.instructions}`;
  })].join("\n\n");
}

export function sameReference(left: ContentReference | undefined, right: ContentReference): boolean {
  return left?.id === right.id && left.version === right.version && left.digest === right.digest;
}

export function filterContent(items: ContentItem[], query: string, kind: ContentDocument["kind"], includeArchived: boolean): ContentItem[] {
  const needle = query.trim().toLowerCase();
  return items.filter((item) => item.kind === kind && (includeArchived || !item.archivedAt) && `${item.name} ${item.description}`.toLowerCase().includes(needle));
}

export function contentReference(value: unknown): ContentReference | undefined {
  if (!value || typeof value !== "object") {
    return undefined;
  }
  const source = value as Record<string, unknown>;
  if (typeof source.id !== "string" || typeof source.version !== "string" || typeof source.digest !== "string") {
    return undefined;
  }
  return { id: source.id, version: source.version, digest: source.digest };
}
