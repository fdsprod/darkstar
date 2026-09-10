import { useEffect, useMemo, useRef, useState } from "react";

import { apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { useRouter } from "../app/router";
import { MessageComposer } from "../components/terminal/MessageComposer";
import { RunTimeline } from "../components/terminal/RunTimeline";
import { HistoryNotice, ShowMoreHistory, TerminalFrame, TranscriptEmpty, TranscriptScroll } from "../components/terminal/TerminalFrame";
import { TranscriptRow, TurnHeading } from "../components/terminal/TranscriptRow";
import { RunViewTabs, TranscriptTools } from "../components/terminal/TranscriptTools";
import { selectTimelineRange, timelineRange, type TimelineSelection } from "../components/terminal/timelineModel";
import { buildTranscript, inTimeRange, type EventKind, type TranscriptEvent } from "../components/terminal/transcriptModel";
import { RunArtifacts } from "./RunArtifacts";
import { RunAttention } from "./RunAttention";

type S = components["schemas"];

export function RunLive({ view, refresh }: { view: S["RunView"]; refresh(): Promise<void> }) {
  const run = view.run;
  const {search,navigate,route}=useRouter();
  const [events, setEvents] = useState<TranscriptEvent[]>([]);
  const [history, setHistory] = useState<"loading" | "live" | "error">("loading");
  const [error, setError] = useState("");
  const tab=new URLSearchParams(search).get("tab");
  const mode=tab==="artifacts"||tab==="evidence" ? "artifacts" : tab==="turns" ? "turns" : "terminal";
  const setMode=(value:"terminal"|"turns"|"artifacts")=>{const query=new URLSearchParams(search);query.set("tab",value);navigate(`/work/${encodeURIComponent(route.params.workId)}/run/${encodeURIComponent(run.id)}?${query}`);};
  const [selection, setSelection] = useState<TimelineSelection>({kind:"all"});
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
  const timelineStart = Date.parse(run.createdAt);
  const timelineEnd = Math.max(timelineStart + 1, transcript.entries.reduce((latest, entry) => Math.max(latest, entry.end), Date.parse(run.updatedAt)));
  const range = useMemo(() => timelineRange(selection, timelineEnd), [selection, timelineEnd]);
  const liveRange = selection.kind !== "fixed";
  const filtered = useMemo(() => transcript.entries.filter(entry => inTimeRange(entry, range) && (!kind || entry.kind === kind) && (!filter || `${entry.title}\n${entry.text}\n${entry.output}`.toLowerCase().includes(filter.toLowerCase()))), [transcript, range, filter, kind]);
  const visible = liveRange ? filtered.slice(-visibleCount) : filtered.slice(0, visibleCount);
  const turnNumbers = useMemo(() => new Map(transcript.turns.map((id, i) => [id, i + 1])), [transcript.turns]);
  useEffect(() => { if (follow && liveRange && mode !== "artifacts") scroll.current?.scrollTo({ top: scroll.current.scrollHeight }); }, [events, follow, liveRange, mode]);
  const chooseRange = (next: [number, number] | null) => { const chosen = selectTimelineRange(next, timelineStart, timelineEnd); setSelection(chosen); setFollow(chosen.kind !== "fixed"); setVisibleCount(200); };
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
  }  return <section className="run-live" aria-label="Live run">
    <RunViewTabs mode={mode} turnCount={transcript.turns.length} usage={transcript.usage} onSelect={setMode} />
    <RunAttention runId={run.id} refreshRun={refresh} />
    {mode === "artifacts" ? <RunArtifacts runId={run.id} attempts={view.attempts} events={events} /> : <TerminalFrame
      title={`${view.attempts.find(a => a.status === "running")?.nodeId ?? run.workflowId} Â· ${mode === "turns" ? "turn history" : "agent transcript"}`}
      state={history === "loading" ? "Loading historyâ¦" : history === "error" ? "Reconnectingâ¦" : follow && run.status === "running" ? "â Live" : `${filtered.length} events`}>
      <RunTimeline entries={transcript.entries} start={timelineStart} end={timelineEnd} range={range} onChange={chooseRange} />
      <TranscriptTools
        filter={filter} onFilter={value => { setFilter(value); setVisibleCount(200); }}
        kind={kind} onKind={setKind}
        following={follow && liveRange} onFollowLive={() => { chooseRange(null); setFollow(true); }}
        raw={raw} onRaw={setRaw}
        onExport={download} exportDisabled={history === "loading"} />
      {error && <HistoryNotice tone="error">{error}. Reconnecting from the last saved event.</HistoryNotice>}
      {transcript.gaps > 0 && <HistoryNotice>{transcript.gaps} older observations have no recoverable content. Available history is shown below.</HistoryNotice>}
      {transcript.missingToolResults > 0 && <HistoryNotice>This older run saved {transcript.missingToolResults} tool requests without their responses. New runs save both.</HistoryNotice>}
      <TranscriptScroll turns={mode === "turns"} scrollRef={scroll} onScroll={() => { const el = scroll.current; if (el && el.scrollHeight - el.scrollTop - el.clientHeight > 100) setFollow(false); }}>
        {!visible.length && <TranscriptEmpty>{history === "loading" ? "Reading the saved transcriptâ¦" : filter || range || kind ? "No events match this view. Adjust the timeline or filters." : "Waiting for the agentâs first message. Its output and tool calls will appear here."}</TranscriptEmpty>}
        {visible.map((entry, i) => <div key={entry.id}>{mode === "turns" && entry.turn && entry.turn !== visible[i - 1]?.turn && <TurnHeading number={turnNumbers.get(entry.turn)} time={entry.time} />}<TranscriptRow entry={entry} raw={raw} /></div>)}
        {filtered.length > visible.length && <ShowMoreHistory hidden={filtered.length - visible.length} onShowMore={() => { setFollow(false); setVisibleCount(value => value + 200); }} />}
      </TranscriptScroll>
    </TerminalFrame>}
    <MessageComposer value={message} onChange={setMessage} onSend={() => void send()} sending={sending}
      disabled={run.status !== "running"}
      placeholder={run.status === "running" ? "Interject or give the agent guidanceâ¦" : run.status === "waiting" ? "Resolve the question or review above to continue." : "Messages are available while an agent is running."} />
    {notice && <p role="status">{notice}</p>}
  </section>;
}
