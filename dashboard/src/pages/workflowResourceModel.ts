import type { JsonObject, ValueType } from "./workflowEditorModel";

export const resourceKinds = ["task", "repository", "artifact", "template", "constant", "config", "open_items", "decision_log"] as const;
export type ResourceKind = typeof resourceKinds[number];
export const record = (value: unknown): JsonObject => value && typeof value === "object" && !Array.isArray(value) ? value as JsonObject : {};
export const inputNodeId = (id: string) => `$input:${id}`;
export const outputNodeId = (node: string, id: string) => `$output:${node}:${id}`;

export function resourceLabel(id: string, declaration: JsonObject) {
  const kind = String(record(declaration.resource).kind ?? (id === "story" ? "task" : id === "repository" ? "repository" : "value"));
  return { kind, title: String(kind === "artifact" ? record(declaration.resource).filename : declaration.description || id.replaceAll("_", " ")) };
}

export function addResource(document: JsonObject, kind: ResourceKind, filename?: string) {
  if(kind === "artifact" && markdownFilenameError(filename ?? "")) throw new Error(markdownFilenameError(filename ?? ""));
  const next = structuredClone(document), spec = record(next.spec);
  next.apiVersion = "darkstar.local/v1alpha3";
  const inputs = record(spec.inputs); spec.inputs = inputs;
  let id: string = kind; for (let i = 2; Object.hasOwn(inputs, id); i++) id = `${kind}_${i}`;
  const source: JsonObject = { kind };
  if (kind === "template") Object.assign(source, { version: "1.0.0", content: "# Document\n\n## Overview\n\nDescribe the proposed solution.\n", requiredHeadings: ["Overview"] });
  if (kind === "constant") source.value = "";
  if (kind === "artifact") Object.assign(source,{filename:filename!.trim(),content:""});
  if (kind === "config") source.key = "provider.codex.executable";
  inputs[id] = { type: kind === "constant" || kind === "config" ? "string" : kind === "artifact" ? "markdown" : kind, resource: source };
  return { document: next, id: inputNodeId(id) };
}

export function updateResource(document: JsonObject, id: string, declaration: JsonObject): JsonObject {
  const next = structuredClone(document); record(record(next.spec).inputs)[id] = declaration; return next;
}

export function removeResource(document: JsonObject, id: string): JsonObject {
  const next = structuredClone(document), spec = record(next.spec);
  // Do not leave dangling edges: removing a source also removes its bindings.
  for (const raw of Object.values(record(spec.nodes))) {
    const inputs = record(record(raw).inputs);
    for (const [key, binding] of Object.entries(inputs)) if (record(binding).from === `run.input.${id}`) delete inputs[key];
  }
  delete record(spec.inputs)[id]; return next;
}

export function outputDeclaration(document: JsonObject, node: string, id: string) {
  return record(record(record(record(document.spec).nodes)[node]).outputs)[id] as JsonObject | undefined;
}

export function setOutputContract(document: JsonObject, node: string, id: string, changes: JsonObject) {
  const next = structuredClone(document), outputs = record(record(record(record(next.spec).nodes)[node]).outputs);
  next.apiVersion = "darkstar.local/v1alpha3";
  outputs[id] = { ...record(outputs[id]), ...changes }; return next;
}

export function addArtifactOutput(document: JsonObject, node: string, filename: string) {
  if(markdownFilenameError(filename)) throw new Error(markdownFilenameError(filename));
  const outputs = record(record(record(record(document.spec).nodes)[node]).outputs);
  let id = filename.trim().replace(/\.md$/i,"").toLowerCase().replace(/[^a-z0-9_]/g,"_");
  if(!/^[a-z]/.test(id))id="file_"+id; id=id.slice(0,60); const stem=id; for (let i = 2; Object.hasOwn(outputs, id); i++) id = `${stem}_${i}`;
  return { document: setOutputContract(document, node, id, { type: "markdown" satisfies ValueType, artifact: { filename: filename.trim() } }), id: outputNodeId(node, id) };
}

/** Semantic ordering for the version picker; build metadata has no precedence. */
export function compareWorkflowVersions(left:string,right:string):number {
 const parts=(value:string)=>value.split("+")[0].split(/-(.*)/s);
 const [a,ap]=parts(left),[b,bp]=parts(right);const an=a.split(".").map(Number),bn=b.split(".").map(Number);
 for(let i=0;i<3;i++){const difference=an[i]-bn[i];if(difference)return difference;}
 if(ap===undefined||bp===undefined)return ap===bp?0:ap===undefined?1:-1;
 const ax=ap.split("."),bx=bp.split(".");for(let i=0;i<Math.max(ax.length,bx.length);i++){if(ax[i]===undefined)return -1;if(bx[i]===undefined)return 1;if(ax[i]===bx[i])continue;const ad=/^\d+$/.test(ax[i]),bd=/^\d+$/.test(bx[i]);if(ad&&bd)return Number(ax[i])-Number(bx[i]);if(ad!==bd)return ad?-1:1;return ax[i]<bx[i]?-1:1;}return 0;
}

export function markdownFilenameError(value:string):string|undefined {const name=value.trim();if(!name)return "A filename is required.";if(!/^[^<>:\"/\\|?*\x00-\x1f]+\.md$/i.test(name)||name===".md")return "Enter a single Markdown filename ending in .md.";return undefined;}
