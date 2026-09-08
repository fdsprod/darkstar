import { useState } from "react";
import { markdownFilenameError } from "./workflowResourceModel";
export function MarkdownFilenameDialog({onAdd,onCancel}:{onAdd(filename:string):void;onCancel():void}) {
 const [filename,setFilename]=useState("");const error=markdownFilenameError(filename);
 return <div className="workflow-node-menu" onKeyDown={event=>{if(event.key === "Escape")onCancel();}} role="dialog" aria-label="New Markdown file" style={{position:"fixed",top:"30%",left:"40%",zIndex:1000}}><form onSubmit={event=>{event.preventDefault();if(!error)onAdd(filename.trim());}}><label>Filename<input autoFocus required placeholder="design.md" value={filename} onChange={event=>setFilename(event.target.value)}/></label>{filename&&error&&<p role="alert">{error}</p>}<button type="submit" disabled={Boolean(error)}>Add Markdown file</button><button type="button" onClick={onCancel}>Cancel</button></form></div>;
}
