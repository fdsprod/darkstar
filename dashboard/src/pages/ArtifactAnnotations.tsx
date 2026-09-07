import { useMemo, useRef, useState, type KeyboardEvent } from "react";

import { codeUnitOffsetAt, createTextRangeAnchor, displayLineRange, orderedFeedbackAnnotations, validateAnnotationComment, type FeedbackAnnotationDraft } from "./artifactReviewModel";

export function ArtifactAnnotations({ text, annotations, readOnly, onChange }: { text: string; annotations: readonly FeedbackAnnotationDraft[]; readOnly: boolean; onChange(next: FeedbackAnnotationDraft[]): void }) {
  const readerRef = useRef<HTMLPreElement>(null);
  const itemRefs = useRef(new Map<string, HTMLLIElement>());
  const [selection, setSelection] = useState<{ start: number; end: number }>();
  const [newComment, setNewComment] = useState("");
  const [composeError, setComposeError] = useState("");
  const [activeId, setActiveId] = useState(annotations[0]?.id ?? "");
  const ordered = useMemo(() => orderedFeedbackAnnotations(annotations), [annotations]);
  const activeIndex = Math.max(0, ordered.findIndex((item) => item.id === activeId));

  function captureSelection() {
    const root = readerRef.current;
    const selected = window.getSelection();
    if (!root || !selected || selected.rangeCount !== 1 || selected.isCollapsed) { setSelection(undefined); return; }
    const range = selected.getRangeAt(0);
    if (!root.contains(range.commonAncestorContainer)) { setSelection(undefined); return; }
    const before = document.createRange(); before.selectNodeContents(root); before.setEnd(range.startContainer, range.startOffset);
    const through = document.createRange(); through.selectNodeContents(root); through.setEnd(range.endContainer, range.endOffset);
    const start = before.toString().length; const end = through.toString().length;
    setSelection(start < end ? { start, end } : undefined);
  }

  async function addComment() {
    if (!selection || !newComment.trim()) return;
    try {
      if (annotations.length >= 256) throw new Error("A feedback set can contain at most 256 annotations.");
      const anchor = await createTextRangeAnchor(text, selection.start, selection.end);
      const item = { id: `annotation_${crypto.randomUUID()}`, anchor, comment: validateAnnotationComment(newComment) };
      onChange([...annotations, item]); setActiveId(item.id); setSelection(undefined); setNewComment(""); setComposeError(""); window.getSelection()?.removeAllRanges();
    } catch (cause) { setComposeError(cause instanceof Error ? cause.message : "The annotation could not be added."); }
  }

  function updateComment(id: string, comment: string) { onChange(annotations.map((item) => item.id === id ? { ...item, comment } : item)); }
  function removeComment(id: string) { onChange(annotations.filter((item) => item.id !== id)); if (activeId === id) setActiveId(""); }
  function navigate(delta: number) {
    if (!ordered.length) return;
    const next = ordered[(activeIndex + delta + ordered.length) % ordered.length]; setActiveId(next.id);
    itemRefs.current.get(next.id)?.focus();
  }
  function handleKeys(event: KeyboardEvent) {
    if (!event.altKey || (event.key !== "ArrowUp" && event.key !== "ArrowDown")) return;
    event.preventDefault(); navigate(event.key === "ArrowUp" ? -1 : 1);
  }

  return <section className="artifact-annotations" aria-labelledby="annotation-heading" onKeyDown={handleKeys}>
    <header><div><p className="eyebrow">Range feedback</p><h2 id="annotation-heading">Annotations</h2></div><div className="annotation-navigation"><button className="button button--compact" type="button" disabled={!ordered.length} onClick={() => navigate(-1)} aria-label="Previous annotation">Previous</button><span aria-live="polite">{ordered.length ? `${activeIndex + 1} of ${ordered.length}` : "No annotations"}</span><button className="button button--compact" type="button" disabled={!ordered.length} onClick={() => navigate(1)} aria-label="Next annotation">Next</button></div></header>
    <p className="annotation-help">Select text in the candidate below, then add a comment. Anchors use UTF-8 byte offsets; line numbers are display only. Use Alt+Up and Alt+Down to move through comments.</p>
    <pre ref={readerRef} className="annotation-reader" tabIndex={0} onMouseUp={captureSelection} onKeyUp={captureSelection}>{renderAnnotatedText(text, ordered, activeId, setActiveId)}</pre>
    {!readOnly && <div className="annotation-compose"><label htmlFor="new-annotation">Comment on {selection ? `selected text (${text.slice(selection.start, selection.end)})` : "a text selection"}</label><textarea id="new-annotation" rows={2} value={newComment} onChange={(event) => setNewComment(event.target.value)} disabled={!selection || annotations.length >= 256} />{composeError && <p role="alert">{composeError}</p>}<button className="button" type="button" disabled={!selection || !newComment.trim() || annotations.length >= 256} onClick={() => void addComment()}>Add comment</button></div>}
    <ol className="annotation-list" aria-label="Annotation comments">{ordered.map((item, index) => { const lines = displayLineRange(text, item.anchor); return <li key={item.id} ref={(node) => { if (node) itemRefs.current.set(item.id, node); else itemRefs.current.delete(item.id); }} tabIndex={-1} className={item.id === activeId ? "is-active" : undefined} onFocus={() => setActiveId(item.id)}><button type="button" className="annotation-quote" onClick={() => setActiveId(item.id)} aria-label={`Show annotation ${index + 1}, lines ${lines.start} to ${lines.end}`}><span>Lines {lines.start}{lines.end === lines.start ? "" : `–${lines.end}`}</span><q>{item.anchor.quotedText}</q></button>{readOnly ? <p>{item.comment}</p> : <><label><span>Edit comment {index + 1}</span><textarea rows={2} value={item.comment} onChange={(event) => updateComment(item.id, event.target.value)} /></label><button className="button button--compact button--danger" type="button" onClick={() => removeComment(item.id)}>Remove comment</button></>}</li>; })}</ol>
  </section>;
}

function renderAnnotatedText(text: string, annotations: readonly FeedbackAnnotationDraft[], activeId: string, onActivate: (id: string) => void) {
  const boundaries = new Set([0, text.length]);
  for (const item of annotations) { boundaries.add(codeUnitOffsetAt(text, item.anchor.startOffset)); boundaries.add(codeUnitOffsetAt(text, item.anchor.endOffset)); }
  const values = [...boundaries].sort((left, right) => left - right);
  return values.slice(0, -1).map((start, index) => {
    const end = values[index + 1]; const matches = annotations.filter((item) => codeUnitOffsetAt(text, item.anchor.startOffset) <= start && codeUnitOffsetAt(text, item.anchor.endOffset) >= end);
    if (!matches.length) return text.slice(start, end);
    const ids = matches.map((item) => item.id); return <mark key={`${start}:${end}`} className={ids.includes(activeId) ? "is-active" : undefined} data-annotation-ids={ids.join(" ")} onClick={() => onActivate(ids[0])}>{text.slice(start, end)}</mark>;
  });
}
