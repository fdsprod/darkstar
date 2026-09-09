import { useState } from "react";
import type { NodeExecutor } from "./workflowEditorModel";
import requirements from "../../../runtime/src/core/workflow/component_requirements.json" with { type: "json" };

type WorkspaceExecutor=Extract<NodeExecutor,{type:"workspace_prepare"|"workspace_validate"}>;
export function WorkspaceExecutorFields({value,onChange}:{value:WorkspaceExecutor;onChange(value:NodeExecutor):void}){
 const [error,setError]=useState("");
 if(value.type==="workspace_prepare")return <section><h3>Prepare workspace</h3><p>{requirements.workspace_prepare.instructions}</p>
 <label>Repository input<input required value={value.repositoryInput} onChange={event=>onChange({...value,repositoryInput:event.target.value})}/></label>
 <label>Workspace mode<select value={value.checkout.mode} onChange={event=>onChange({...value,checkout:event.target.value==="current_checkout"?{mode:"current_checkout"}:{mode:"new_worktree",baseRef:"",branch:"darkstar/{runId}"}})}><option value="current_checkout">Use current checkout</option><option value="new_worktree">Create isolated worktree</option></select></label>
 {value.checkout.mode==="new_worktree"&&<><label>Base ref<input required placeholder="Explicit branch, ref, or commit" value={value.checkout.baseRef} onChange={event=>{if(value.checkout.mode==="new_worktree")onChange({...value,checkout:{...value.checkout,baseRef:event.target.value}})}}/></label><label>New branch<input required value={value.checkout.branch} onChange={event=>{if(value.checkout.mode==="new_worktree")onChange({...value,checkout:{...value.checkout,branch:event.target.value}})}}/></label><p>The daemon freezes the base commit and chooses a managed worktree path. Existing branches cause a conflict; they are never overwritten.</p></>}
 </section>;
 return <section><h3>Validate workspace</h3><p>{requirements.workspace_validate.instructions}</p><label>Workspace input<input required value={value.workspaceInput} onChange={event=>onChange({...value,workspaceInput:event.target.value})}/></label>
 <label>Required checks (JSON argument arrays)<textarea key={JSON.stringify(value.checks)} rows={5} defaultValue={JSON.stringify(value.checks,null,2)} placeholder={'[["git", "diff", "--check"]]'} onBlur={event=>{try{const checks:unknown=JSON.parse(event.target.value);if(!Array.isArray(checks)||!checks.every(argv=>Array.isArray(argv)&&argv.length>0&&argv.every(arg=>typeof arg==="string")))throw new Error("Use arrays of executable and argument strings.");setError("");onChange({...value,checks:checks as string[][]});}catch{setError("Enter a JSON array of command argument arrays, for example [[\"git\",\"diff\",\"--check\"]].");}}}/></label>{error&&<p role="alert">{error}</p>}
 </section>;
}
