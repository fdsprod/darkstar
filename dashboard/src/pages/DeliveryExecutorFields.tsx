import type { NodeExecutor } from "./workflowEditorModel";

type DeliveryExecutor = Extract<NodeExecutor, {type: "git_commit" | "git_push" | "create_pr"}>;

export function DeliveryExecutorFields({value,onChange}: {value:DeliveryExecutor;onChange(value:NodeExecutor):void}) {
  return <fieldset className="inspector-section">
    <legend>{value.type.replaceAll("_", " ")}</legend>
    <label className="field"><span>Workspace input</span><input value={value.workspaceInput} onChange={event=>onChange({...value,workspaceInput:event.target.value})}/></label>
    {value.type === "git_commit" && <label className="field"><span>Changeset input</span><input value={value.changesetInput} onChange={event=>onChange({...value,changesetInput:event.target.value})}/></label>}
    {value.type === "git_push" && <>
      <label className="field"><span>Commit input</span><input value={value.commitInput} onChange={event=>onChange({...value,commitInput:event.target.value})}/></label>
      <label className="field"><span>Remote</span><input value={value.remote} onChange={event=>onChange({...value,remote:event.target.value})}/></label>
    </>}
    {(value.type === "git_commit" || value.type === "create_pr") && <label className="field"><span>Delivery text input</span><input value={value.textInput} onChange={event=>onChange({...value,textInput:event.target.value})}/></label>}
    {value.type === "create_pr" && <>
      <label className="field"><span>Published branch input</span><input value={value.branchInput} onChange={event=>onChange({...value,branchInput:event.target.value})}/></label>
      <label className="field"><span>PR target branch</span><input value={value.base} onChange={event=>onChange({...value,base:event.target.value})}/></label>
      <p className="inspector-help">Use remote_default for the remote repository’s default branch.</p>
      <label><input type="checkbox" checked={value.draft} onChange={event=>onChange({...value,draft:event.target.checked})}/> Create as draft</label>
    </>}
    <p className="inspector-help">Add validation or approval nodes wherever your workflow needs them.</p>
  </fieldset>;
}
