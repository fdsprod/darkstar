import { ReadableResponse } from "../components/document/ReadableResponse";
import { useCallback, useEffect, useMemo, useRef, useState, type PointerEvent } from "react";
import { apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { buildTranscript, inTimeRange, type EventKind, type TranscriptEntry, type TranscriptEvent } from "./runTranscriptModel";
import { RunArtifacts } from "./RunArtifacts";
import { RunAttention } from "./RunAttention";
import { useRouter } from "../app/router";

type S = components["schemas"];
const colors: Record<EventKind, string> = { message: "#61c486", reasoning: "#8ac5e8", tool: "#ba9aef", error: "#f07d70", decision: "#f0b45d", user: "#71b5ed", lifecycle: "#81929f" };
const clock = (time: number) => new Date(time).toLocaleTimeString([], { hour12: false });

export function RunLive({ view, refresh }: { view: S["RunView"]; refresh(): Promise<void> }) {
  const run = view.run;
  const {search,navigate,route}=useRouter();
  const [events, setEvents] = useState<TranscriptEvent[]>([]);
  const [history, setHistory] = useState<"loading" | "live" | "error">("loading");
  const [error, setError] = useState("");
  const tab=new URLSearchParams(search).get("tab");
  const mode=tab==="artifacts"||tab==="evidence" ? "artifacts" : tab==="turns" ? "turns" : "terminal";
  const setMode=(value:"terminal"|"turns"|"artifacts")=>{const query=new URLSearchParams(search);query.set("tab",value);navigate(`/work/${encodeURIComponent(route.params.workId)}/run/${encodeURIComponent(run.id)}?${query}`);};
  const [range, setRange] = useState<[number, number] | null>(null);
  const [follow, setFollow] = useState(true);
  const raw = new URLSearchParams(search).get("format") === "raw";
  const setRaw = (value: boolean) => { const query = new URLSearchParams(search); query.set("format", value ? "raw" : "rendered"); navigate(`/work/${encodeURIComponent(route.params.workId)}/run/${encodeURIComponent(run.id)}?${query}`); };
  const [filter, setFilter] = useState("");
  const [kind, setKind] = useState<EventKind | "">("");
  const [visibleCount, setVisibleCount] = useState(200);
  const [message, setMessage] = useState("");
  const [sending, setSending] = useState(false);
  const [notice, setNotice] = useState("");
  const scroll = useRef<HTMLDivElement>(null);
  const sendingRef = useRef(false);
  useEffect(() => {
    const abort = new AbortController(); let cursor = 0, timer: ReturnType<typeof setTimeout>;
    setEvents([]); setHistory("loading");
    async function poll() {
      try {
        let more: boolean;
        do {
          const page = await apiClient.operation("getRunTranscript", { path: { runId: run.id }, query: { after: cursor, limit: 1000 }, signal: abort.signal });
          if (abort.signal.aborted) return;
          if (page.events.length) setEvents(current => [...current, ...page.events.filter(e => !current.length || e.position > current[current.length - 1].position)]);
          more = page.hasMore && page.next > cursor; cursor = page.next;
        } while (more && !abort.signal.aborted);
        setHistory("live"); setError("");
      } catch (cause) { if (!abort.signal.aborted) { setHistory("error"); setError(cause instanceof Error ? cause.message : "Unable to load history"); } }
      if (!abort.signal.aborted) timer = setTimeout(poll, 1000);
    }
    void poll(); return () => { abort.abort(); clearTimeout(timer); };
  }, [run.id]);
  const transcript = useMemo(() => buildTranscript(events), [events]);
  const filtered = useMemo(() => transcript.entries.filter(entry => inTimeRange(entry, range) && (!kind || entry.kind === kind) && (!filter || `${entry.title}\n${entry.text}\n${entry.output}`.toLowerCase().includes(filter.toLowerCase()))), [transcript, range, filter, kind]);
  const visible = !range ? filtered.slice(-visibleCount) : filtered.slice(0, visibleCount);
  const turnNumbers = useMemo(() => new Map(transcript.turns.map((id, i) => [id, i + 1])), [transcript.turns]);
  useEffect(() => { if (follow && !range && mode !== "artifacts") scroll.current?.scrollTo({ top: scroll.current.scrollHeight }); }, [events, follow, range, mode]);
  const chooseRange = (next: [number, number] | null) => { setRange(next); setFollow(!next); setVisibleCount(200); };
  async function send() {
    if (!message.trim() || sendingRef.current) return;
    sendingRef.current = true; setSending(true); setNotice("");
    try {
      const current = await apiClient.getRun(run.id);
      const result = await apiClient.operation("sendRunMessage", { path: { runId: run.id }, resourceVersion: current.run.resourceVersion, idempotencyKey: `run-message-${crypto.randomUUID()}`, body: { message: message.trim() } });
      setMessage(""); setNotice(result.status === "accepted" ? "Delivered to the active agent." : `Saved. Delivery is unconfirmed: ${result.message ?? "the agent did not acknowledge it"}`); await refresh();
    } catch (cause) { setNotice(cause instanceof Error ? cause.message : "Message could not be sent."); }
    finally { sendingRef.current = false; setSending(false); }
  }
  function download() {
    const blob = new Blob([events.map(event => JSON.stringify(event)).join("\n")], { type: "application/x-ndjson" });
    const url = URL.createObjectURL(blob), a = document.createElement("a"); a.href = url; a.download = `${run.id}-transcript.ndjson`; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  return <section className="run-live" aria-label="Live run">
    <div className="run-live-toolbar"><nav aria-label="Run view">{([['terminal', 'Terminal'], ['turns', `Turns (${transcript.turns.length})`], ['artifacts', 'Artifacts']] as const).map(([id, label]) => <button key={id} aria-pressed={mode === id} onClick={() => setMode(id)} title={id === "turns" ? "Agent messages and tool calls; streamed fragments and tool results count once. Provider inference boundaries are not exposed." : undefined}>{label}</button>)}</nav><div className="run-token-counts">{([['input', 'Input'], ['output', 'Output'], ['reasoning', 'Reasoning']] as const).map(([key, label]) => <span key={key} title={transcript.usage[key] === undefined ? "Not reported by the provider" : `${label} tokens reported by the provider`}><strong>{transcript.usage[key]?.toLocaleString() ?? "—"}</strong> {label}</span>)}</div></div>
    <RunAttention runId={run.id} refreshRun={refresh} />
    {mode === "artifacts" ? <RunArtifacts runId={run.id} attempts={view.attempts} events={events} /> : <div className="run-terminal">
      <header className="run-terminal-title"><span className="terminal-lights" aria-hidden="true"><i /><i /><i /></span><span>{view.attempts.find(a => a.status === "running")?.nodeId ?? run.workflowId} · {mode === "turns" ? "turn history" : "agent transcript"}</span><span className="run-terminal-state">{history === "loading" ? "Loading history…" : history === "error" ? "Reconnecting…" : follow && run.status === "running" ? "● Live" : `${filtered.length} events`}</span></header>
      <RunTimeline entries={transcript.entries} start={Date.parse(run.createdAt)} end={transcript.entries.reduce((latest,e) => Math.max(latest,e.end),Date.parse(run.updatedAt))} range={range} onChange={chooseRange} />
      <div className="run-transcript-tools"><input aria-label="Search transcript" placeholder="Search transcript…" value={filter} onChange={e => { setFilter(e.target.value); setVisibleCount(200); }} /><select aria-label="Filter event type" value={kind} onChange={e => setKind(e.target.value as EventKind | "")}><option value="">All event types</option>{Object.keys(colors).map(key => <option key={key} value={key}>{key === "reasoning" ? "Reasoning summaries" : key[0].toUpperCase() + key.slice(1)}</option>)}</select><button onClick={() => { chooseRange(null); setFollow(true); }} aria-pressed={follow && !range}>Follow live</button><label>Messages <select aria-label="Transcript format" value={raw ? "raw" : "rendered"} onChange={e => setRaw(e.target.value === "raw")}><option value="rendered">Rendered</option><option value="raw">Raw</option></select></label><button onClick={download} disabled={history === "loading"}>Export history</button></div>
      {error && <p className="run-inline-error" role="alert">{error}. Reconnecting from the last saved event.</p>}
      {transcript.gaps > 0 && <p className="run-history-gap">{transcript.gaps} older observations have no recoverable content. Available history is shown below.</p>}
      {transcript.missingToolResults > 0 && <p className="run-history-gap">This older run saved {transcript.missingToolResults} tool requests without their responses. New runs save both.</p>}
      <div className={`run-transcript-scroll ${mode === "turns" ? "run-transcript-turns" : ""}`} ref={scroll} onScroll={() => { const el = scroll.current; if (el && el.scrollHeight - el.scrollTop - el.clientHeight > 100) setFollow(false); }}>
        {!visible.length && <p className="run-transcript-empty">{history === "loading" ? "Reading the saved transcript…" : filter || range || kind ? "No events match this view. Adjust the timeline or filters." : "Waiting for the agent’s first message. Its output and tool calls will appear here."}</p>}
        {visible.map((entry, i) => <div key={entry.id}>{mode === "turns" && entry.turn && entry.turn !== visible[i - 1]?.turn && <h3 className="run-turn-heading">Turn {turnNumbers.get(entry.turn)} <small>{clock(entry.time)}</small></h3>}<TranscriptRow entry={entry} raw={raw} /></div>)}
        {filtered.length > visible.length && <button className="run-more-history" onClick={() => { setFollow(false); setVisibleCount(value => value + 200); }}>Show more ({filtered.length - visible.length} events)</button>}
      </div>
    </div>}
    <form className="run-message-composer" onSubmit={e => { e.preventDefault(); void send(); }}><label className="sr-only" htmlFor="run-guidance">Message the agent</label><textarea id="run-guidance" value={message} onChange={e => setMessage(e.target.value)} maxLength={32000} disabled={sending || run.status !== "running"} placeholder={run.status === "running" ? "Interject or give the agent guidance…" : run.status === "waiting" ? "Resolve the question or review above to continue." : "Messages are available while an agent is running."} rows={2} onKeyDown={e => { if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) { e.preventDefault(); void send(); } }} /><button aria-label="Send message to agent" disabled={sending || run.status !== "running" || !message.trim()} type="submit">{sending ? "…" : "↑"}</button></form>
    {notice && <p role="status">{notice}</p>}
  </section>;
}

function TranscriptRow({ entry, raw }: { entry: TranscriptEntry; raw: boolean }) {
  return <article className={`run-transcript-entry run-transcript-entry--${entry.kind}`} style={{ "--event-color": colors[entry.kind] } as React.CSSProperties}>
    <header><time>{clock(entry.time)}</time><i aria-hidden="true" /><strong>{entry.title}</strong>{entry.status && <span>{entry.status}</span>}</header>
    {entry.text && (!raw && (entry.kind === "message" || entry.kind === "reasoning" || entry.kind === "user" || entry.title === "submit_output") ? <ReadableResponse text={entry.text} /> : <pre>{entry.text}</pre>)}{entry.output && ((raw || !["read_input","submit_output"].includes(entry.title)) ? <pre className="run-tool-output">{entry.output}</pre> : <ReadableResponse text={entry.output}/>)}
    <RawEvents events={entry.raw}/>
  </article>;
}

function RawEvents({events}:{events:TranscriptEvent[]}){const [open,setOpen]=useState(false);return <details className="run-raw-event" onToggle={e=>setOpen(e.currentTarget.open)}><summary>Raw events ({events.length})</summary>{open&&<pre>{JSON.stringify(events,null,2)}</pre>}</details>}

function RunTimeline({ entries, start, end, range, onChange }: { entries: TranscriptEntry[]; start: number; end: number; range: [number, number] | null; onChange(value: [number, number] | null): void }) {
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
function localDate(time: number) { const date = new Date(time); return new Date(time - date.getTimezoneOffset() * 60000).toISOString().slice(0, 19); }
