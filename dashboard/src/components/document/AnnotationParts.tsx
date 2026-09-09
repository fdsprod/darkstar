import type { ReactNode } from 'react';
export function AnnotationPopup({left,top,quote,comment,error,busy,editing,onComment,onSave,onCancel}: {left:number;top:number;quote:string;comment:string;error:string;busy:boolean;editing:boolean;onComment(value:string):void;onSave():void;onCancel():void}) {
 return <section className="annotation-popup" role="dialog" aria-label={editing?'Edit annotation':'Annotate selected text'} style={{left,top}} onKeyDown={event=>{if(event.key==='Escape'){event.preventDefault();onCancel();}}}>
  <header><span aria-hidden="true">▱</span><q>{quote}</q></header>
  <label className="sr-only" htmlFor="new-annotation">Comment on selected text</label>
  <textarea id="new-annotation" rows={3} value={comment} disabled={busy} placeholder="Add a note… (Enter to save)" onChange={event=>onComment(event.target.value)} onKeyDown={event=>{if(event.key==='Enter'&&!event.shiftKey&&!event.nativeEvent.isComposing){event.preventDefault();if(comment.trim()&&!busy)onSave();}}}/>
  {error&&<p role="alert">{error}</p>}
  <footer><button className="button button--compact" type="button" onClick={onCancel} disabled={busy}>Cancel</button><button className="button button--primary" type="button" onClick={onSave} disabled={busy||!comment.trim()}>{busy?'Saving…':editing?'Save annotation':'+ Annotate'}</button></footer>
 </section>;
}
export function AnnotationCard({number,quote,comment,label,readOnly,active,onActivate,onEdit,onRemove}: {number:number;quote:string;comment:string;label:string;readOnly:boolean;active:boolean;onActivate():void;onEdit():void;onRemove():void}) {
 return <article className={`annotation-card annotation-card--compact${active?' is-active':''}`} aria-label={`Annotation ${number}`}>
  <button type="button" className="annotation-number" onClick={onActivate} aria-label={`Show annotation ${number} in document`}>{number}</button>
  <div className="annotation-card-body"><button className="annotation-quote" type="button" title={label} onClick={onActivate}><q>{quote}</q></button>{readOnly?<p>{comment}</p>:<button type="button" className="annotation-comment" aria-label={`Edit annotation ${number}`} onClick={onEdit}>{comment}</button>}</div>
  {!readOnly&&<button type="button" className="annotation-remove" aria-label={`Remove annotation ${number}`} title="Remove annotation" onClick={onRemove}>×</button>}
 </article>;
}
export function AnnotationBadge({number,onActivate}: {number:number;onActivate():void}) {return <button type="button" className="annotation-inline-number" contentEditable={false} aria-label={`Open annotation ${number}`} onClick={onActivate}>{number}</button>;}
export function AnnotationPanel({count,children,composer,actions}: {count:number;children:ReactNode;composer:ReactNode;actions?:ReactNode}) {return <><header><strong>Annotations</strong><span>{count}</span></header><div className="annotation-panel-scroll">{count?children:<p className="annotation-empty">Select text in the document to annotate it, or send a general instruction below.</p>}</div><div className="annotation-panel-footer">{composer}{actions}</div></>;}
export function ApprovalActions({approveDisabled,reviseDisabled,rejectDisabled,busy,revisionLabel,onApprove,onRevise,onReject}: {approveDisabled:boolean;reviseDisabled:boolean;rejectDisabled:boolean;busy:string;revisionLabel:string;onApprove():void;onRevise():void;onReject():void}) {return <div className="approval-actions"><button className="button button--primary" type="button" disabled={reviseDisabled} onClick={onRevise}>{busy==='request_revisions'?'Submitting…':revisionLabel}</button><button className="button" type="button" disabled={approveDisabled} onClick={onApprove}>{busy==='approve'?'Recording…':'Approve'}</button><button className="button button--compact button--danger" type="button" disabled={rejectDisabled} onClick={onReject}>Reject</button></div>;}

