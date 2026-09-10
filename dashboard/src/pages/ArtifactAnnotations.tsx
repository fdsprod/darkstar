import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Markdown } from '../components/Markdown';
import { DocumentReviewLayout, DocumentToolbar, ContentsTree } from '../components/document/DocumentReviewLayout';
import { AnnotationBadge, AnnotationCard, AnnotationPanel, AnnotationPopup } from '../components/document/AnnotationParts';
import { markdownHeadings, sourceEndpoint } from '../components/document/markdownModel';
import { codeUnitOffsetAt, createTextRangeAnchor, displayLineRange, orderedFeedbackAnnotations, validateAnnotationComment, type FeedbackAnnotationDraft } from './artifactReviewModel';

type Editor = { kind:'new'; start:number; end:number; quote:string; left:number; top:number } | { kind:'edit'; id:string; quote:string; left:number; top:number };
function popupPosition(rect:DOMRect) {
  const width=Math.min(360,window.innerWidth-16), height=270;
  return {left:Math.max(8,Math.min(rect.left,window.innerWidth-width-8)),top:Math.max(8,Math.min(rect.top>height+12?rect.top-height-8:rect.bottom+8,window.innerHeight-height-8))};
}
export function ArtifactAnnotations({text,annotations,readOnly,onChange,actions,generalAnnotations,generalCount=0}: {text:string;annotations:readonly FeedbackAnnotationDraft[];readOnly:boolean;onChange(next:FeedbackAnnotationDraft[]):void;actions?:ReactNode;generalAnnotations?:ReactNode;generalCount?:number}) {
  const readerRef=useRef<HTMLDivElement>(null), panelRef=useRef<HTMLDivElement>(null), savingRef=useRef(false);
  const [format,setFormat]=useState<'formatted'|'raw'>('formatted');
  const [contentsOpen,setContentsOpen]=useState(true),[annotationsOpen,setAnnotationsOpen]=useState(true);
  const [editor,setEditor]=useState<Editor>(),[comment,setComment]=useState(''),[error,setError]=useState(''),[busy,setBusy]=useState(false),[activeId,setActiveId]=useState('');
  const ordered=useMemo(()=>orderedFeedbackAnnotations(annotations),[annotations]);
  const headings=useMemo(()=>markdownHeadings(text),[text]);
  const ranges=useMemo(()=>ordered.map((a,index)=>({...a,number:index+1,start:codeUnitOffsetAt(text,a.anchor.startOffset),end:codeUnitOffsetAt(text,a.anchor.endOffset)})),[text,ordered]);
  useEffect(()=>{if(editor)requestAnimationFrame(()=>document.getElementById('new-annotation')?.focus({preventScroll:true}));},[editor]);
  function closeEditor(){if(savingRef.current)return;setEditor(undefined);setComment('');setError('');window.getSelection()?.removeAllRanges();readerRef.current?.focus({preventScroll:true});}
  function captureSelection(){
    const root=readerRef.current,selected=window.getSelection();
    if(readOnly||!root||!selected||selected.isCollapsed||selected.rangeCount!==1)return;
    const range=selected.getRangeAt(0);if(!root.contains(range.commonAncestorContainer))return;
    const start=sourceEndpoint(root,range.startContainer,range.startOffset,false),end=sourceEndpoint(root,range.endContainer,range.endOffset,true);
    if(start!==undefined&&end!==undefined&&start<end){setComment('');setError('');setEditor({kind:'new',start,end,quote:selected.toString(),...popupPosition(range.getBoundingClientRect())});}
  }
  async function saveComment(){
    if(!editor||!comment.trim()||readOnly||savingRef.current)return;
    savingRef.current=true;setBusy(true);
    try{
      const validated=validateAnnotationComment(comment);let id:string;
      if(editor.kind==='edit'){
        id=editor.id;onChange(annotations.map(a=>a.id===id?{...a,comment:validated}:a));
      }else{
        if(annotations.length>=256)throw new Error('At most 256 annotations are allowed.');
        const anchor=await createTextRangeAnchor(text,editor.start,editor.end);
        id=`annotation_${crypto.randomUUID()}`;onChange([...annotations,{id,anchor,comment:validated}]);
      }
      setActiveId(id);setAnnotationsOpen(true);setEditor(undefined);setComment('');setError('');window.getSelection()?.removeAllRanges();
      requestAnimationFrame(()=>panelRef.current?.querySelector<HTMLElement>(`[data-comment-id="${CSS.escape(id)}"]`)?.scrollIntoView({block:'nearest'}));
    }catch(cause){setError(cause instanceof Error?cause.message:'Could not save annotation');}finally{savingRef.current=false;setBusy(false);}
  }
  function activate(id:string,fromDocument=false){
    setActiveId(id);setAnnotationsOpen(true);
    if(fromDocument)requestAnimationFrame(()=>panelRef.current?.querySelector<HTMLElement>(`[data-comment-id="${CSS.escape(id)}"]`)?.scrollIntoView({block:'nearest',behavior:'smooth'}));
    else readerRef.current?.querySelector<HTMLElement>(`[data-annotation-ids~="${CSS.escape(id)}"]`)?.scrollIntoView({block:'center',behavior:'smooth'});
  }
  function edit(item:FeedbackAnnotationDraft){
    const mark=readerRef.current?.querySelector<HTMLElement>(`[data-annotation-ids~="${CSS.escape(item.id)}"]`);
    setActiveId(item.id);setComment(item.comment);setError('');
    setEditor({kind:'edit',id:item.id,quote:item.anchor.quotedText,...popupPosition(mark?.getBoundingClientRect()??readerRef.current!.getBoundingClientRect())});
  }
  function renderSource(value:string,start:number):ReactNode{
    const pending=editor?.kind==='new'?editor:undefined;
    const end=start+value.length,bounds=[...new Set([start,end,...(pending?[pending.start,pending.end].filter(n=>n>start&&n<end):[]),...ranges.flatMap(r=>[r.start,r.end]).filter(n=>n>start&&n<end)])].sort((a,b)=>a-b);
    return bounds.slice(0,-1).map((a,i)=>{
      const b=bounds[i+1],matches=ranges.filter(r=>r.start<=a&&r.end>=b),content=value.slice(a-start,b-start);
      if(!matches.length)return <span key={a} data-source-start={a} className={pending&&pending.start<=a&&pending.end>=b?'annotation-pending-selection':undefined}>{content}</span>;
      return <span key={a}>{matches.filter(r=>r.start===a).map(r=><AnnotationBadge key={r.id} number={r.number} onActivate={()=>activate(r.id,true)}/>)}<mark data-source-start={a} data-annotation-ids={matches.map(r=>r.id).join(' ')} className={matches.some(r=>r.id===activeId)?'is-active':undefined} onClick={()=>activate(matches[0].id,true)}>{content}</mark></span>;
    });
  }
  const sidebar=<div className="annotation-panel-inner" ref={panelRef}><AnnotationPanel count={ordered.length+generalCount} actions={actions} composer={null}>{generalAnnotations}<ol className="annotation-list" aria-label="Annotation comments">{ordered.map((item,index)=>{
    const lines=displayLineRange(text,item.anchor);
    return <li key={item.id} data-comment-id={item.id}><AnnotationCard number={index+1} quote={item.anchor.quotedText} comment={item.comment} label={`Lines ${lines.start}–${lines.end}`} readOnly={readOnly} active={item.id===activeId} onActivate={()=>activate(item.id)} onEdit={()=>edit(item)} onRemove={()=>{onChange(annotations.filter(a=>a.id!==item.id));if(activeId===item.id)setActiveId('');if(editor?.kind==='edit'&&editor.id===item.id)closeEditor();}}/></li>;
  })}</ol></AnnotationPanel></div>;
  return <div className="artifact-annotations" onKeyDown={event=>{
    if(event.altKey&&['ArrowUp','ArrowDown'].includes(event.key)&&ordered.length){event.preventDefault();const index=ordered.findIndex(a=>a.id===activeId);activate(ordered[(index+(event.key==='ArrowDown'?1:-1)+ordered.length)%ordered.length].id);}
  }}>
    {editor&&!readOnly&&<AnnotationPopup {...editor} quote={editor.quote} comment={comment} error={error} busy={busy} editing={editor.kind==='edit'} onComment={setComment} onSave={()=>void saveComment()} onCancel={closeEditor}/>}
    <DocumentReviewLayout toolbar={<DocumentToolbar format={format} onFormat={value=>{setFormat(value);closeEditor();}} contentsOpen={contentsOpen} annotationsOpen={annotationsOpen} onContents={()=>setContentsOpen(!contentsOpen)} onAnnotations={()=>setAnnotationsOpen(!annotationsOpen)}/>}
      contents={contentsOpen?<ContentsTree headings={headings} onSelect={id=>{if(format==='raw')setFormat('formatted');requestAnimationFrame(()=>readerRef.current?.querySelector(`#${id}`)?.scrollIntoView({block:'start',behavior:'smooth'}));}}/>:undefined}
      document={<div ref={readerRef} className="annotation-reader" tabIndex={0} onContextMenu={event=>{const selected=window.getSelection();if(!readOnly&&selected&&!selected.isCollapsed&&selected.rangeCount===1&&readerRef.current?.contains(selected.getRangeAt(0).commonAncestorContainer)){event.preventDefault();captureSelection();}}} onMouseUp={captureSelection} onKeyUp={captureSelection}>{format==='raw'?<pre>{renderSource(text,0)}</pre>:<Markdown text={text} renderSource={renderSource}/>}</div>}
      annotations={annotationsOpen?sidebar:undefined}/>
  </div>;
}
