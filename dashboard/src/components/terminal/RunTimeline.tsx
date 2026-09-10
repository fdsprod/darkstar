import { useRef, type PointerEvent } from "react";

import { inTimeRange, type TranscriptEntry } from "./transcriptModel";
import { clock, eventColors as colors, localDate } from "./theme";

export interface RunTimelineProps {
  entries: TranscriptEntry[];
  start: number;
  end: number;
  range: [number, number] | null;
  onChange(value: [number, number] | null): void;
}

// A brushable rail over saved event times. The selection is reported upward;
// what a narrowed right edge means for future events is the caller's decision,
// kept in timelineModel.ts.

export function RunTimeline({ entries, start, end, range, onChange }: RunTimelineProps) {
  const rail = useRef<HTMLDivElement>(null);
  const drag = useRef<{ kind: "start" | "end" | "move" | "select"; anchor: number; range: [number, number] } | null>(null);
  const max = Math.max(start + 1, end), span = max - start;
  const selected: [number, number] = range ?? [start, max];
  const percent = (v: number) => `${Math.max(0, Math.min(100, (v - start) / span * 100))}%`;
  const at = (e: PointerEvent) => { const rect = rail.current!.getBoundingClientRect(); return start + Math.max(0, Math.min(1, (e.clientX - rect.left) / rect.width)) * span; };
  function begin(e: PointerEvent, kind: "start" | "end" | "move" | "select") { e.preventDefault(); e.stopPropagation(); rail.current?.setPointerCapture(e.pointerId); drag.current = { kind, anchor: at(e), range: selected }; }
  function move(e: PointerEvent) {
    const d = drag.current; if (!d) return;
    const v = at(e), minSpan = Math.min(1000, span / 100);
    if (d.kind === "start") onChange([Math.min(v, d.range[1] - minSpan), d.range[1]]);
    else if (d.kind === "end") onChange([d.range[0], Math.max(v, d.range[0] + minSpan)]);
    else if (d.kind === "move") { const width = d.range[1] - d.range[0], lo = Math.max(start, Math.min(max - width, d.range[0] + v - d.anchor)); onChange([lo, lo + width]); }
    else onChange([Math.min(d.anchor, v), Math.max(d.anchor + minSpan, v)]);
  }
  function finish(e: PointerEvent) { const d = drag.current; if (d?.kind === "select" && Math.abs(at(e) - d.anchor) < span / 200) { const width = Math.max(1, span / 10), lo = Math.max(start, Math.min(max - width, d.anchor - width / 2)); onChange([lo, lo + width]); } drag.current = null; }
  return <div className="run-timeline" aria-label="Event timeline">
    <div className="run-timeline-legend">{Object.entries(colors).map(([key, color]) => <span key={key}><i style={{ background: color }} />{key === "reasoning" ? "Reasoning summary" : key}</span>)}</div>
    <div className="run-timeline-rail" ref={rail} onPointerDown={e => begin(e, "select")} onPointerMove={move} onPointerUp={finish} onPointerCancel={() => { drag.current = null; }}>
      {entries.map(entry => <span key={entry.id} className="run-timeline-tick" style={{ left: percent(entry.time), background: colors[entry.kind], opacity: inTimeRange(entry, range) ? 1 : .25 }} title={`${clock(entry.time)} · ${entry.title}`} />)}
      <div className="run-timeline-window" style={{ left: percent(selected[0]), width: `${(selected[1] - selected[0]) / span * 100}%` }} onPointerDown={e => begin(e, range ? "move" : "select")}><button aria-label="Timeline range start" onPointerDown={e => begin(e, "start")} onKeyDown={e => { if (e.key === "ArrowLeft" || e.key === "ArrowRight") { e.preventDefault(); onChange([Math.max(start, Math.min(selected[1] - 1, selected[0] + (e.key === "ArrowLeft" ? -1 : 1) * span / 100)), selected[1]]); } }} /><button aria-label="Timeline range end" onPointerDown={e => begin(e, "end")} onKeyDown={e => { if (e.key === "ArrowLeft" || e.key === "ArrowRight") { e.preventDefault(); onChange([selected[0], Math.min(max, Math.max(selected[0] + 1, selected[1] + (e.key === "ArrowLeft" ? -1 : 1) * span / 100))]); } }} /></div>
    </div>
    <div className="run-timeline-times"><span>{clock(start)}</span><span>{range ? `${clock(range[0])} – ${clock(range[1])}` : "Entire run"}</span><span>{clock(max)}</span></div>
    <div className="run-timeline-range"><label>From <input aria-label="Timeline minimum time" type="datetime-local" step="1" value={localDate(selected[0])} onChange={e => { const v = new Date(e.target.value).getTime(); if (Number.isFinite(v) && v >= start && v <= selected[1]) onChange([v, selected[1]]); }} /></label><label>To <input aria-label="Timeline maximum time" type="datetime-local" step="1" value={localDate(selected[1])} onChange={e => { const v = new Date(e.target.value).getTime(); if (Number.isFinite(v) && v >= selected[0] && v <= max) onChange([selected[0], v]); }} /></label>{range && <button onClick={() => onChange(null)}>Reset range</button>}</div>
  </div>;
}
