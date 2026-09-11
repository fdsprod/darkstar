import { useRef, useState, type FormEvent } from "react";
import { apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import type { JsonObject } from "./workflowEditorModel";
import { record } from "./workflowResourceModel";
import { addStagePreset, type StagePreset } from "./workflowStageModel";

const stages = ["questions", "research", "design", "technical_design", "plan"] as const;
const labels = ["Questions", "Research", "Product Design", "Technical Design", "Plan"];

export function WorkflowStageTools({ document, presets, onChange }: {
  document: JsonObject;
  presets: StagePreset[];
  onChange: (document: JsonObject, id: string, message: string) => void;
}) {
  const [presetID, setPresetID] = useState("");
  const [open, setOpen] = useState(false);
  const [routerID, setRouterID] = useState("assessment_router");
  const [selected, setSelected] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const currentDocument = useRef(document);
  currentDocument.current = document;
  const nodes = record(record(document.spec).nodes);
  const target = (stage: string) => {
    const presetID = stage === "design" ? "product_design" : stage;
    return selected[stage] ?? Object.keys(nodes).find((id) => id === presetID || id.startsWith(`${presetID}_`)) ?? "";
  };

  async function addRouter(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    const source = document;
    try {
      const result = await apiClient.operation("buildAssessmentRouter", {
        body: { document: document as unknown as components["schemas"]["WorkflowDocument"], id: routerID, targets: { questions: target("questions"), research: target("research"), design: target("design"), technical_design: target("technical_design"), plan: target("plan") } },
      });
      if (currentDocument.current !== source) {
        throw new Error("The workflow changed while preparing the router. Add it again using the current draft.");
      }
      onChange(result.document as JsonObject, result.assessmentId, "Assessment Router added with durable assessment and gate visits.");
      setOpen(false);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Assessment Router could not be added.");
    } finally {
      setBusy(false);
    }
  }

  return <div className="workflow-stage-tools">
    <div className="workflow-editor-toolbar">
      <select aria-label="Stage preset" value={presetID} onChange={(event) => setPresetID(event.target.value)}>
        <option value="">Choose a stage…</option>
        {presets.map((preset) => <option key={preset.id} value={preset.id}>{preset.name}</option>)}
      </select>
      <button className="button button--compact" disabled={!presetID || busy} onClick={() => {
        const preset = presets.find((item) => item.id === presetID);
        if (preset) {
          const result = addStagePreset(document, preset);
          onChange(result.document, result.id, `${preset.name} added with linked prompt and template.`);
        }
      }}>Add stage</button>
      <button className="button button--compact" aria-expanded={open} onClick={() => setOpen(!open)}>Assessment Router</button>
    </div>
    {open && <form className="workflow-router-form" onSubmit={(event) => void addRouter(event)}>
      <p>Select the existing stages that the assessment can enter. Design, Technical Design, and Plan require human review.</p>
      <label className="field"><span>Router name</span><input required pattern="[a-z][a-z0-9_]{0,34}" value={routerID} onChange={(event) => setRouterID(event.target.value)} /></label>
      {stages.map((stage, index) => <label className="field" key={stage}><span>{labels[index]} exit</span><select required value={target(stage)} onChange={(event) => setSelected({ ...selected, [stage]: event.target.value })}>
        <option value="">Choose a node…</option>
        {Object.entries(nodes).map(([id, raw]) => <option key={id} value={id}>{String(record(raw).displayName ?? id)}</option>)}
      </select></label>)}
      <p>Skipped designs require explicit approval attestations in the run inputs. Without them, the router sends the work through the required review.</p>
      {error && <p role="alert">{error}</p>}
      <button className="button button--primary" disabled={busy}>{busy ? "Preparing…" : "Add router"}</button>
    </form>}
  </div>;
}
