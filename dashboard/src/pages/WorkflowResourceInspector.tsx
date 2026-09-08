import { apiClient } from "../api/client";
import { useEffect, useState } from "react";
import type { JsonObject } from "./workflowEditorModel";
import { record, removeResource, setOutputContract, updateResource } from "./workflowResourceModel";

export function WorkflowResourceInspector({ document, id, readOnly, onChange }: { document: JsonObject; id: string; readOnly: boolean; onChange(document: JsonObject, message: string): void }) {
  const [, owner, outputID] = id.split(":");
  const input = id.startsWith("$input:");
  const declaration = input ? record(record(record(document.spec).inputs)[owner]) : record(record(record(record(record(document.spec).nodes)[owner]).outputs)[outputID]);
  const [draft, setDraft] = useState(() => structuredClone(declaration));
  const [schema, setSchema] = useState(() => declaration.schemaDefinition ? JSON.stringify(declaration.schemaDefinition, null, 2) : "");
  const [value, setValue] = useState(() => JSON.stringify(record(declaration.resource).value ?? "", null, 2));
  const [error, setError] = useState("");
  const resource = record(draft.resource), kind = String(resource.kind ?? "value");
  const source = (changes: JsonObject) => setDraft({ ...draft, resource: { ...resource, ...changes } });
  const artifact = record(draft.artifact);
  const [configKeys,setConfigKeys]=useState<string[]>([]);
  useEffect(()=>{ if(kind!=="config") return; const abort=new AbortController(); void apiClient.getConfigurationState(undefined,abort.signal).then(state=>setConfigKeys(state.effective.filter(item=>item.value.type!=="secret_reference").map(item=>item.key))).catch(()=>{}); return ()=>abort.abort(); },[kind]);
  function apply() {
    try {
      const next = structuredClone(draft);
      if (schema.trim()) next.schemaDefinition = JSON.parse(schema); else delete next.schemaDefinition;
      if (kind === "constant") record(next.resource).value = JSON.parse(value);
      onChange(input ? updateResource(document, owner, next) : setOutputContract(document, owner, outputID, next), "Resource updated."); setError("");
    } catch (cause) { setError(cause instanceof Error ? cause.message : "Invalid value"); }
  }
  return <fieldset className="workflow-resource-inspector" disabled={readOnly}><legend>{input ? kind.replaceAll("_", " ") : "Output artifact / value"}</legend>
    <label className="field"><span>Name</span><input value={String(draft.description ?? "")} placeholder={input ? owner : outputID} onChange={e => setDraft({ ...draft, description: e.target.value })} /></label>
    {(kind === "constant" || kind === "config" || !input || kind === "value") && <label className="field"><span>Value type</span><select value={String(draft.type)} onChange={e => setDraft({ ...draft, type: e.target.value })}>{["string","boolean","integer","number","object","array","null"].map(t => <option key={t}>{t}</option>)}</select></label>}
    {kind === "template" && <><label className="field"><span>Template version</span><input value={String(resource.version)} onChange={e => source({ version: e.target.value })} /></label><label className="field"><span>Template</span><textarea rows={14} value={String(resource.content)} onChange={e => source({ content: e.target.value })} /></label><label className="field"><span>Required headings (one per line)</span><textarea value={Array.isArray(resource.requiredHeadings) ? resource.requiredHeadings.join("\n") : ""} onChange={e => source({ requiredHeadings: e.target.value.split("\n").map(s => s.trim()).filter(Boolean) })} /></label></>}
    {kind === "artifact" && <><label className="field"><span>Filename</span><input value={String(resource.filename)} onChange={e=>source({filename:e.target.value})}/></label><label className="field"><span>Initial content</span><textarea rows={12} value={String(resource.content)} onChange={e=>source({content:e.target.value})}/></label><p>Supply content here, or connect an action output to make this a generated artifact.</p></>}
    {kind === "constant" && <label className="field"><span>Value (JSON)</span><textarea rows={7} value={value} onChange={e => setValue(e.target.value)} /></label>}
    {kind === "config" && <label className="field"><span>Configuration key</span><input list="workflow-config-keys" value={String(resource.key)} onChange={e => source({ key: e.target.value })} /><datalist id="workflow-config-keys">{configKeys.map(key => <option key={key} value={key}/>)}</datalist></label>}
    {!input && <><label className="field"><span>Filename (optional)</span><input value={String(artifact.filename ?? "")} onChange={e => setDraft({ ...draft, artifact: { ...artifact, filename: e.target.value } })} /></label><label className="field"><span>Template input</span><select value={String(artifact.templateInput ?? "")} onChange={e => { const next = { ...artifact }; if (e.target.value) next.templateInput = e.target.value; else delete next.templateInput; setDraft({ ...draft, artifact: next }); }}><option value="">No template</option>{Object.entries(record(record(record(record(document.spec).nodes)[owner]).inputs)).filter(([, b]) => { const from = String(record(b).from).split("."); return record(record(record(record(document.spec).inputs)[from[2]]).resource).kind === "template"; }).map(([key]) => <option key={key}>{key}</option>)}</select></label></>}
    {(draft.type === "object" || draft.type === "array") && <label className="field"><span>JSON Schema</span><textarea rows={9} value={schema} onChange={e => setSchema(e.target.value)} /></label>}
    {(kind === "open_items" || kind === "decision_log") && <p>Connected actions receive read and append tools. Resolutions and superseding decisions preserve the original history.</p>}
    {error && <p role="alert">{error}</p>}{!readOnly && <><button className="button" onClick={apply}>Apply</button>{input && <button className="button" onClick={() => onChange(removeResource(document, owner), "Resource removed.")}>Remove resource</button>}</>}
  </fieldset>;
}
