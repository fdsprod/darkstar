import { useEffect, useState } from "react";

import { ApiRequestError } from "../api/client";
import { trackerMappingApi, type Conditions, type MappingDiscovery, type MappingPreview, type MappingState, type TrackerRules, type WorkflowPin } from "../api/trackerMapping";
import type { components } from "../api/schema.generated";
import { AsyncPanel } from "../components/InteractionPatterns";

type Ticket = components["schemas"]["BacklogTicket"];

interface MappingSettingsProps {
  projectId: string;
  bindingRevision: number;
  tickets: Ticket[];
  onChanged(): void;
}

export function TrackerMappingSettings(props: MappingSettingsProps) {
  return <TrackerMappingSession key={`${props.projectId}/${props.bindingRevision}`} {...props} />;
}

function TrackerMappingSession({ projectId, tickets, onChanged }: MappingSettingsProps) {
  const [state, setState] = useState<MappingState>();
  const [discovery, setDiscovery] = useState<MappingDiscovery>();
  const [rules, setRules] = useState<TrackerRules>();
  const [sample, setSample] = useState(tickets.find((ticket) => ticket.currentSource)?.observationId ?? "");
  const [milestoneKey, setMilestoneKey] = useState("");
  const [preview, setPreview] = useState<MappingPreview>();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const abort = new AbortController();
    setDiscovery(undefined);
    void Promise.all([trackerMappingApi.get(projectId, abort.signal), trackerMappingApi.discovery(projectId, sample, abort.signal)]).then(([next, discovered]) => {
      if (!abort.signal.aborted) {
        setState(next);
        setDiscovery(discovered);
        setRules((current) => current?.scope.projectId === projectId && current.scope.bindingRevision === discovered.bindingRevision ? current : structuredClone(next.revisions.find((item) => item.revision === next.activeRevision && item.bindingRevision === discovered.bindingRevision)?.rules ?? discovered.template));
        setError("");
      }
    }).catch((cause: unknown) => {
      if (!abort.signal.aborted) {
        setError(cause instanceof ApiRequestError ? cause.message : "Tracker mapping discovery is unavailable. Reload to retry.");
      }
    });
    return () => abort.abort();
  }, [projectId, sample, reload]);

  function change(next: TrackerRules) {
    setRules(next);
    setPreview(undefined);
  }

  async function command(kind: "preview" | "save" | "activate", revision?: number) {
    if (!rules || !state || !discovery || pending || rules.scope.projectId !== projectId || rules.scope.bindingRevision !== discovery.bindingRevision) {
      return;
    }
    setPending(true);
    setError("");
    try {
      if (kind === "preview") {
        const milestone = discovery?.milestones.find((item) => `${item.workflow.digest}/${item.id}` === milestoneKey);
        setPreview(await trackerMappingApi.preview(projectId, rules, sample, milestone ? { workflow: milestone.workflow, milestoneId: milestone.id } : undefined));
      } else if (kind === "save") {
        const next = { ...rules, revision: Math.max(0, ...state.revisions.map((item) => item.revision)) + 1 };
        await trackerMappingApi.save(projectId, next, sample);
        setState(await trackerMappingApi.get(projectId));
        setRules(next);
        setPreview(undefined);
        onChanged();
      } else if (revision !== undefined) {
        setState(await trackerMappingApi.activate(projectId, revision, state.activeRevision, sample));
        onChanged();
      }
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "The mapping command was not confirmed. Reload before retrying.");
    } finally {
      setPending(false);
    }
  }

  return <section className="tracker-mapping ticket-source-settings" aria-label="Tracker workflow mappings" aria-busy={pending}>
    <h2>Tracker workflow mappings</h2>
    <p>Save an immutable revision, inspect its preview, then activate it for future work. Active work retains its pinned rules and source. Loading or previewing a mapping does not start an agent or change a ticket.</p>
    {error && <AsyncPanel compact state="error" title="Mapping needs attention" message={error} />}
    <button className="button" type="button" disabled={pending} onClick={() => setReload((value) => value + 1)}>Reload discovery and history</button>
    {discovery && <>
      <p>Source binding revision {discovery.bindingRevision}. {discovery.reason}</p>
      <p>Ticket creation: {discovery.creation.available ? "Supported by selected tracker writer" : discovery.creation.reason}</p>
      <details><summary>Discovered capabilities and stable IDs</summary>
        <ul>{discovery.capabilities.map((capability) => <li key={capability.id}>{capability.id}: {capability.state}{capability.value !== undefined ? ` (${capability.value})` : ""} {capability.reason}</li>)}</ul>
        <ul>{discovery.transitions.map((transition) => <li key={transition.id}>{transition.name} ({transition.id}) → {transition.targetStateId}. Required fields: {transition.requiredFields.join(", ") || "None"}. {transition.reason}</li>)}</ul>
      </details>
    </>}
    {rules && discovery && <>
      <MappingEditor rules={rules} discovery={discovery} pending={pending} onChange={change} />
      <fieldset disabled={pending}>
        <legend>Preview against retained evidence</legend>
        <label>Sample ticket<select value={sample} onChange={(event) => {
          setSample(event.target.value);
          setPreview(undefined);
        }}><option value="">Configuration validation only</option>{tickets.filter((ticket) => ticket.currentSource).map((ticket) => <option key={ticket.observationId} value={ticket.observationId}>{ticket.key || ticket.ref.id} · {ticket.title}</option>)}</select></label>
        <label>Sample workflow milestone<select value={milestoneKey} onChange={(event) => {
          setMilestoneKey(event.target.value);
          setPreview(undefined);
        }}><option value="">Intake only</option>{discovery.milestones.map((item) => <option key={`${item.workflow.digest}/${item.id}`} value={`${item.workflow.digest}/${item.id}`}>{item.workflow.id} · {item.id}</option>)}</select></label>
        <p>A sample event projects the requested action; actual milestone execution requires retained workflow evidence.</p>
        <div className="ticket-toolbar"><button className="button" type="button" onClick={() => void command("preview")}>Validate and preview</button><button className="button button--primary" type="button" onClick={() => void command("save")}>Save new revision</button></div>
      </fieldset>
      {preview && <MappingPreviewPanel preview={preview} />}
    </>}
    {state && <details open><summary>Configuration history · active revision {state.activeRevision || "none"}</summary>
      {state.revisions.length === 0 && <p>No saved mapping revisions.</p>}
      {[...state.revisions].reverse().map((revision) => <article key={revision.revision} className="mapping-revision">
        <strong>Revision {revision.revision} · source {revision.bindingRevision}</strong><p>{new Date(revision.createdAt).toLocaleString()}</p>
        <button className="button" type="button" disabled={pending} onClick={() => change(structuredClone(revision.rules))}>Load revision for editing</button>
        <button className="button" type="button" disabled={pending || state.activeRevision === revision.revision || revision.bindingRevision !== discovery?.bindingRevision} onClick={() => void command("activate", revision.revision)}>{state.activeRevision === revision.revision ? "Active" : "Validate and activate"}</button>
        <details><summary>Saved configuration for audit</summary><pre>{JSON.stringify(revision.rules, null, 2)}</pre></details>
      </article>)}
    </details>}
  </section>;
}

export function MappingPreviewPanel({ preview }: { preview: MappingPreview }) {
  return <section className="mapping-preview" aria-label="Mapping preview" role="status">
    <h3>{preview.valid ? "Configuration valid" : "Configuration blocked"}</h3>
    <ul>{preview.issues.map((issue, index) => <li key={`${issue.field}/${issue.code}/${index}`}>{issue.field}: {issue.message || issue.code}</li>)}</ul>
    {preview.intake && <><p>Matched intake rule: {preview.intake.matched ? preview.intake.ruleId : "None"}</p><p>Board placement: {preview.intake.group.groupName}. {preview.intake.group.reason}</p><p>Intake action: {preview.intake.action?.kind === "admit" ? `${preview.intake.action.mode} admission to ${preview.intake.action.workflow.id} (${preview.intake.action.workflow.version})` : "Observe only"}</p></>}
    {preview.outbound && <p>Matched milestone rule: {preview.outbound.matched ? preview.outbound.ruleId : "None"}</p>}
    <p>Approval: {preview.approval || "No approval decision granted by this preview"}</p>
    <p>Required fields: {preview.requiredFields?.join(", ") || "None reported"}</p>
    {preview.requiredEvidence && <p>Required evidence: {preview.requiredEvidence.join(", ") || "None"}</p>}
    {preview.reason && <p>Unavailable because: {preview.reason}</p>}
    <details open><summary>Exact requested action</summary><pre>{JSON.stringify(preview.requestedAction ?? preview.intake?.action ?? { kind: "noop" }, null, 2)}</pre></details>
  </section>;
}

export function MappingEditor({ rules, discovery, pending, onChange }: { rules: TrackerRules; discovery: MappingDiscovery; pending: boolean; onChange(rules: TrackerRules): void }) {
  function updateIntake(index: number, value: TrackerRules["intake"][number]) {
    onChange({ ...rules, intake: rules.intake.map((rule, offset) => offset === index ? value : rule) });
  }
  function updateOutbound(index: number, value: TrackerRules["outbound"][number]) {
    onChange({ ...rules, outbound: rules.outbound.map((rule, offset) => offset === index ? value : rule) });
  }
  const states = discovery.fields.find((field) => field.id === "state")?.values ?? [];
  return <fieldset disabled={pending}>
    <legend>Mapping definition</legend>
    <label>Rule set ID<input value={rules.id} onChange={(event) => onChange({ ...rules, id: event.target.value })} /></label>
    <h3>Board columns</h3>
    {rules.display.groups.map((group, index) => <div key={group.id} className="mapping-rule">
      <label>Column name<input value={group.name} onChange={(event) => onChange({ ...rules, display: { ...rules.display, groups: rules.display.groups.map((item, offset) => offset === index ? { ...item, name: event.target.value } : item) } })} /></label>
      <label>Source statuses<select multiple value={group.stateIds} onChange={(event) => onChange({ ...rules, display: { ...rules.display, groups: rules.display.groups.map((item, offset) => offset === index ? { ...item, stateIds: Array.from(event.target.selectedOptions, (option) => option.value) } : item) } })}>{states.map((state) => <option key={state.ID} value={state.ID}>{state.Name} ({state.ID})</option>)}</select></label>
      <button className="button" type="button" onClick={() => onChange({ ...rules, display: { ...rules.display, groups: rules.display.groups.filter((_, offset) => offset !== index) } })}>Remove column</button>
    </div>)}
    <button className="button" type="button" onClick={() => onChange({ ...rules, display: { ...rules.display, groups: [...rules.display.groups, { id: `column-${crypto.randomUUID()}`, name: "New column", stateIds: [] }] } })}>Add column</button>
    <p>Unmapped statuses remain visible in {rules.display.unknownGroup.name}. Mapping changes presentation only.</p>
    <label>Unmapped column name<input value={rules.display.unknownGroup.name} onChange={(event) => onChange({ ...rules, display: { ...rules.display, unknownGroup: { ...rules.display.unknownGroup, name: event.target.value } } })} /></label>
    <h3>Intake rules</h3>
    {rules.intake.map((rule, index) => <div className="mapping-rule" key={rule.id}>
      <strong>{rule.id}</strong>
      <ConditionsEditor value={rule.when} discovery={discovery} onChange={(when) => updateIntake(index, { ...rule, when })} />
      <label>Admission<select value={rule.action.kind === "noop" ? "observe" : rule.action.mode} onChange={(event) => updateIntake(index, { ...rule, action: event.target.value === "observe" ? { kind: "noop" } : { kind: "admit", mode: event.target.value as "manual" | "automatic", workflow: discovery.workflows[0] ?? { id: "", version: "", digest: "" }, readinessPolicy: discovery.readinessPolicies[0] ?? "", repair: { maxAdmissions: 1 } } })}><option value="observe">Observe only</option><option value="manual">Manual approval before admission</option><option value="automatic">Automatic admission</option></select></label>
      {rule.action.kind === "admit" && <>
        <WorkflowChoice value={rule.action.workflow} workflows={discovery.workflows} onChange={(workflow) => {
          if (rule.action.kind === "admit") {
            updateIntake(index, { ...rule, action: { ...rule.action, workflow } });
          }
        }} />
        <label>Readiness policy<select value={rule.action.readinessPolicy} onChange={(event) => {
          if (rule.action.kind === "admit") {
            updateIntake(index, { ...rule, action: { ...rule.action, readinessPolicy: event.target.value } });
          }
        }}>{discovery.readinessPolicies.map((policy) => <option key={policy} value={policy}>{policy}</option>)}</select></label>
        <label>Maximum admissions, including initial<input type="number" min={1} value={rule.action.repair.maxAdmissions} onChange={(event) => {
          if (rule.action.kind === "admit") {
            updateIntake(index, { ...rule, action: { ...rule.action, repair: { maxAdmissions: Number(event.target.value) } } });
          }
        }} /></label>
      </>}
      <button className="button" type="button" onClick={() => onChange({ ...rules, intake: rules.intake.filter((_, offset) => offset !== index) })}>Remove intake rule</button>
    </div>)}
    <button className="button" type="button" onClick={() => onChange({ ...rules, intake: [...rules.intake, { id: `intake-${crypto.randomUUID()}`, when: { fields: [] }, action: { kind: "noop" } }] })}>Add intake rule</button>
    <h3>Named milestone outputs</h3>
    {rules.outbound.map((rule, index) => <div className="mapping-rule" key={rule.id}>
      <strong>{rule.id}</strong>
      <ConditionsEditor value={rule.when} discovery={discovery} onChange={(when) => updateOutbound(index, { ...rule, when })} />
      <label>Milestone contract<select value={`${rule.milestone.workflow.digest}/${rule.milestone.id}`} onChange={(event) => {
        const milestone = discovery.milestones.find((item) => `${item.workflow.digest}/${item.id}` === event.target.value);
        if (milestone) {
          updateOutbound(index, { ...rule, milestone });
        }
      }}><option value="">Choose declared milestone</option>{discovery.milestones.map((item) => <option key={`${item.workflow.digest}/${item.id}`} value={`${item.workflow.digest}/${item.id}`}>{item.workflow.id} · {item.id}</option>)}</select></label>
      <p>Required evidence: {rule.milestone.evidenceTypes.join(", ") || "Select a declared milestone"}</p>
      <label>Outbound policy<select value={rule.action.kind} onChange={(event) => updateOutbound(index, { ...rule, action: event.target.value === "report" ? { kind: "report", body: "" } : event.target.value === "transition" ? { kind: "transition", transitionId: "", fields: {} } : { kind: "noop" } })}><option value="noop">Observe only</option><option value="report">Report milestone</option><option value="transition">Request tracker transition</option></select></label>
      {rule.action.kind === "report" && <label>Report text<textarea value={rule.action.body} onChange={(event) => updateOutbound(index, { ...rule, action: { kind: "report", body: event.target.value } })} /></label>}
      {rule.action.kind === "transition" && <>
        <label>Target transition<select value={rule.action.transitionId} onChange={(event) => {
          if (rule.action.kind === "transition") {
            updateOutbound(index, { ...rule, action: { ...rule.action, transitionId: event.target.value } });
          }
        }}><option value="">Choose discovered transition</option>{discovery.transitions.map((transition) => <option key={transition.id} value={transition.id}>{transition.name} ({transition.id})</option>)}</select></label>
        <p>{discovery.transitions.find((transition) => rule.action.kind === "transition" && transition.id === rule.action.transitionId)?.reason}</p>
        <TypedFieldsEditor value={rule.action.fields} requiredFields={discovery.transitions.find((transition) => rule.action.kind === "transition" && transition.id === rule.action.transitionId)?.requiredFields ?? []} onChange={(fields) => {
          if (rule.action.kind === "transition") {
            updateOutbound(index, { ...rule, action: { ...rule.action, fields } });
          }
        }} />
      </>}
      <button className="button" type="button" onClick={() => onChange({ ...rules, outbound: rules.outbound.filter((_, offset) => offset !== index) })}>Remove milestone rule</button>
    </div>)}
    <button className="button" type="button" onClick={() => onChange({ ...rules, outbound: [...rules.outbound, { id: `milestone-${crypto.randomUUID()}`, when: { fields: [] }, milestone: discovery.milestones[0] ?? { id: "", workflow: discovery.workflows[0] ?? { id: "", version: "", digest: "" }, evidenceTypes: [] }, action: { kind: "noop" } }] })}>Add milestone rule</button>
  </fieldset>;
}

function ConditionsEditor({ value, discovery, onChange }: { value: Conditions; discovery: MappingDiscovery; onChange(value: Conditions): void }) {
  return <div className="mapping-conditions">
    <p>All fields must match; any selected value within a field can match. Empty conditions match all tickets in the pinned source scope.</p>
    {value.fields.map((predicate, index) => <div key={index}>
      <label>Condition field<select value={predicate.fieldId} onChange={(event) => onChange({ ...value, fields: value.fields.map((item, offset) => offset === index ? { fieldId: event.target.value, values: [] } : item) })}>{discovery.fields.map((field) => <option key={field.id} value={field.id}>{field.id}</option>)}</select></label>
      <label>Accepted values<select multiple value={predicate.values} onChange={(event) => onChange({ ...value, fields: value.fields.map((item, offset) => offset === index ? { ...item, values: Array.from(event.target.selectedOptions, (option) => option.value) } : item) })}>{(discovery.fields.find((field) => field.id === predicate.fieldId)?.values ?? []).map((item) => <option key={item.ID} value={item.ID}>{item.Name} ({item.ID})</option>)}</select></label>
      <button className="button" type="button" onClick={() => onChange({ ...value, fields: value.fields.filter((_, offset) => offset !== index) })}>Remove condition</button>
    </div>)}
    <button className="button" type="button" disabled={discovery.fields.length === 0} onClick={() => onChange({ ...value, fields: [...value.fields, { fieldId: discovery.fields[0]?.id ?? "", values: [] }] })}>Add admission condition</button>
    <label>Sprint or cycle membership<select multiple value={value.sprintIds ?? []} disabled={discovery.sprints.length === 0} onChange={(event) => onChange({ ...value, sprintIds: Array.from(event.target.selectedOptions, (option) => option.value) })}>{discovery.sprints.map((sprint) => <option key={sprint.ID} value={sprint.ID}>{sprint.Name} ({sprint.ID})</option>)}</select></label>
    <p>{discovery.sprintReason || (discovery.sprints.length === 0 ? "No sprint or cycle values are available from this source. Status does not imply sprint membership." : "Sprint and cycle membership is an independent condition; leave empty for any membership.")}</p>
  </div>;
}

function WorkflowChoice({ value, workflows, onChange }: { value: WorkflowPin; workflows: WorkflowPin[]; onChange(value: WorkflowPin): void }) {
  return <label>Workflow version<select value={value.digest} onChange={(event) => {
    const workflow = workflows.find((item) => item.digest === event.target.value);
    if (workflow) {
      onChange(workflow);
    }
  }}><option value="">Choose a workflow</option>{workflows.map((workflow) => <option key={workflow.digest} value={workflow.digest}>{workflow.id} · {workflow.version}</option>)}</select></label>;
}

type TransitionFields = Extract<TrackerRules["outbound"][number]["action"], { kind: "transition" }>["fields"];

function TypedFieldsEditor({ value, requiredFields, onChange }: { value: TransitionFields; requiredFields: string[]; onChange(value: TransitionFields): void }) {
  return <div><p>Required transition fields: {requiredFields.join(", ") || "None"}</p>{[...new Set([...requiredFields, ...Object.keys(value)])].map((id) => {
    const field = value[id];
    return <div key={id}>
      <label>{id} value type<select value={field?.kind ?? ""} onChange={(event) => {
        const kind = event.target.value;
        if (kind === "text") {
          onChange({ ...value, [id]: { kind, value: "" } });
        } else if (kind === "number") {
          onChange({ ...value, [id]: { kind, value: 0 } });
        } else if (kind === "ids") {
          onChange({ ...value, [id]: { kind, value: [] } });
        }
      }}><option value="">Not supplied</option><option value="text">Text</option><option value="number">Number</option><option value="ids">Stable IDs</option></select></label>
      {field && <label>{id}<input type={field.kind === "number" ? "number" : "text"} value={field.kind === "ids" ? field.value.join(", ") : field.value} onChange={(event) => onChange({ ...value, [id]: field.kind === "number" ? { kind: "number", value: Number(event.target.value) } : field.kind === "ids" ? { kind: "ids", value: event.target.value.split(",").map((item) => item.trim()).filter(Boolean) } : { kind: "text", value: event.target.value } })} /></label>}
    </div>;
  })}</div>;
}
