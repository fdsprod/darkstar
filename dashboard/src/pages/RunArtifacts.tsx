import { ArtifactReviewWorkspace } from "./ArtifactReviewPage";
import { useRouter } from "../app/router";
import { Markdown } from "../components/Markdown";
import { useEffect, useMemo, useState } from "react";
import { apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { decodeArtifactViews, type DecodedArtifactView } from "./artifactModel";
import type { TranscriptEvent } from "./runTranscriptModel";

type S = components["schemas"];
type Record = S["RunArtifactRecord"];

export function RunArtifacts({ runId, attempts, events }: { runId: string; attempts: S["Attempt"][]; events:TranscriptEvent[] }) {
  const hasReviews = events.some(event => event.kind === "approval.requested" && (event.data as {class?:string}).class === "workflow_checkpoint");
  const [records, setRecords] = useState<Record[]>([]), [error, setError] = useState("");
  const [selected, setSelected] = useState("");
  const [revision, setRevision] = useState<number>();
  useEffect(() => {
    const abort = new AbortController(); let cursor = 0, timer: ReturnType<typeof setTimeout>;
    setRecords([]);
    async function poll() { try { let more: boolean; do {
      const page = await apiClient.operation("getRunArtifacts", { path: { runId }, query: { after: cursor, limit: 500 }, signal: abort.signal }); if (abort.signal.aborted) return;
      if (page.records.length) setRecords(current => [...current, ...page.records]); more = page.hasMore && page.next > cursor; cursor = page.next;
    } while (more); setError(""); } catch (cause) { if (!abort.signal.aborted) setError(cause instanceof Error ? cause.message : "Unable to load artifacts"); } if (!abort.signal.aborted) timer = setTimeout(poll, 1500); }
    void poll(); return () => { abort.abort(); clearTimeout(timer); };
  }, [runId]);
  const groups = useMemo(() => {
    const values = new Map<string, { id: string; title: string; kind: "markdown" | "decision" | "open_item" | "result"; records: Record[] }>();
    const humanRecords:Record[]=events.filter(e => { if(e.kind === "approval.decided") return true; if(e.kind !== "approval.cancelled") return false; const request=events.find(r=>r.subject===e.subject&&r.kind==="approval.requested"); const data=request?.data as {class?:string;visitId?:string}|undefined; return !(data?.class==="workflow_control" && events.some(r=>r.kind==="approval.requested"&&(r.data as {class?:string}).class==="workflow_checkpoint"&&(r.data as {visitId?:string}).visitId===data.visitId)); }).map(event=>{
      const data=event.data as {action?:string;comment?:string};const requested=events.find(e=>e.subject===event.subject&&e.kind==="approval.requested");const node=(requested?.data as {nodeId?:string}|undefined)?.nodeId;
      const action=event.kind==="approval.cancelled"?"Cancelled":data.action==="approve"?"Approved":data.action==="request_changes"?"Requested changes":"Declined";
      return {sequence:-event.position,attemptId:"",resource:"human_decisions",entryId:event.subject,operation:"record",content:`${action}${node?` · ${node}`:""}\n\n${new Date(event.time).toLocaleString()}${data.comment?`\n\n${data.comment}`:""}`};
    });
    for (const record of [...records,...humanRecords]) {
      const output = record.resource.startsWith("output:");
      const id = output ? record.resource : `${record.resource}:${record.entryId}`;
      let group = values.get(id);
      if (!group) {
        let kind: "markdown" | "decision" | "open_item" | "result" = record.operation === "record" ? "decision" : "open_item";
        if (output) { try { kind = typeof JSON.parse(record.content) === "string" ? "markdown" : "result"; } catch { kind = "result"; } }
        const node = attempts.find(a => a.id === record.attemptId)?.nodeId ?? "";
        group = { id, title: output ? `${record.entryId}${kind === "markdown" && !record.entryId.endsWith(".md") ? ".md" : ""}${node ? ` · ${node}` : ""}` : record.content.split("\n")[0].replace(/^#+\s*/, "").slice(0, 120), kind, records: [] };
        values.set(id, group);
      }
      group.records.push(record);
    }
    const reviewedNodes = new Set(events.filter(e => e.kind === "approval.requested" && (e.data as {class?:string}).class === "workflow_checkpoint").map(e => (e.data as {nodeId?:string}).nodeId));
    return [...values.values()].filter(group => !(group.kind === "markdown" && !group.id.startsWith("output:workspace:") && group.records.some(record => reviewedNodes.has(attempts.find(a => a.id === record.attemptId)?.nodeId))));
  }, [records, attempts, events]);
  const active = groups.find(group => group.id === selected) ?? groups[0];
  const current = active?.records.find(record => record.sequence === revision) ?? active?.records.at(-1);
  let content = current?.content ?? "";
  if (active?.kind === "markdown") { try { content = JSON.parse(content); } catch { /* Retain recorded text if it is not JSON. */ } }
  function download() { const url = URL.createObjectURL(new Blob([content], { type: active?.kind === "markdown" ? "text/markdown" : "text/plain" })); const a = document.createElement("a"); a.href = url; a.download = active?.title.split(" · ")[0] ?? "artifact.txt"; a.click(); setTimeout(() => URL.revokeObjectURL(url), 1000); }
  return <><RunArtifactReviews events={events} /><div className="run-artifacts">
    {error && <p role="alert">{error}</p>}
    {!groups.length ? hasReviews ? null : <div className="run-artifacts-empty"><h2>No artifacts yet</h2><p>Saved plans, Markdown outputs, decisions, and open items will appear here as the agent creates them.</p></div> : <>
      <aside aria-label="Run artifacts">{([['markdown', 'Documents'], ['decision', 'Decisions'], ['open_item', 'Open items'], ['result', 'Results']] as const).map(([kind, label]) => { const items = groups.filter(group => group.kind === kind); return items.length > 0 && <section key={kind}><h3>{label} <small>{items.length}</small></h3>{items.map(group => <button key={group.id} aria-current={active?.id === group.id} onClick={() => { setSelected(group.id); setRevision(undefined); }}><strong>{group.title}</strong><small>{group.kind === "open_item" ? group.records.at(-1)?.operation === "resolve" ? "Resolved" : "Open" : `${group.records.length} revision${group.records.length === 1 ? "" : "s"}`}</small></button>)}</section>; })}</aside>
      <article className="run-artifact-document"><header><h2>{active?.title}</h2><select aria-label="Artifact revision" value={current?.sequence} onChange={e => setRevision(Number(e.target.value))}>{active?.records.map((record, index) => <option key={record.sequence} value={record.sequence}>Revision {index + 1}{index === active.records.length - 1 ? " · latest" : ""}</option>)}</select><button onClick={download}>Download</button></header>
        {active?.kind === "markdown" ? <ReadableMarkdown text={content} /> : active?.kind === "result" ? <ReadableResult content={content} /> : <><ReadableMarkdown text={active?.records[0].content ?? ""} />{active && active.records.length > 1 && <section className="run-journal-history"><h3>Decision and resolution history</h3>{active.records.slice(1).filter(r => !current || r.sequence <= current.sequence).map(record => <div key={record.sequence}><strong>{record.operation}</strong><ReadableMarkdown text={record.content} /></div>)}</section>}</>}
      </article>
    </>}
  </div><RegisteredRunArtifacts runId={runId} nodeIds={[...new Set(attempts.map(a=>a.nodeId).filter((id):id is string=>Boolean(id)))]} /></>;
}

function RegisteredRunArtifacts({runId,nodeIds}:{runId:string;nodeIds:string[]}){
 const [values,setValues]=useState<DecodedArtifactView[]>([]),[error,setError]=useState("");
 const identity=nodeIds.join("\0");
 useEffect(()=>{const abort=new AbortController();let timer:ReturnType<typeof setTimeout>;
  async function load(){try{const pages=await Promise.all([apiClient.listArtifacts("run",runId,abort.signal),...nodeIds.map(node=>apiClient.listArtifacts("node",`${runId}/${node}`,abort.signal))]);const unique=new Map<string,DecodedArtifactView>();for(const page of pages)for(const view of decodeArtifactViews(page))unique.set(`${view.artifact.artifactId}:${view.artifact.version}`,view);if(!abort.signal.aborted){setValues([...unique.values()]);setError("")}}catch{if(!abort.signal.aborted)setError("Attached artifacts could not be loaded.")}if(!abort.signal.aborted)timer=setTimeout(load,2500)}
  void load();return()=>{abort.abort();clearTimeout(timer)};
 },[runId,identity]);
 if(!values.length&&!error)return null;
 return <section className="run-registered-artifacts"><h3>Attached artifacts</h3>{error&&<p role="alert">{error}</p>}{values.map(value=><details key={`${value.artifact.artifactId}:${value.artifact.version}`}><summary>{value.artifact.sourceName} · revision {value.artifact.version}</summary><SavedArtifactDocument id={value.artifact.artifactId} version={value.artifact.version} /></details>)}</section>;
}

export function SavedArtifactDocument({id,version}:{id:string;version:number}){
 const [state,setState]=useState<{kind:"loading"}|{kind:"text";text:string}|{kind:"error";message:string}>({kind:"loading"});
 useEffect(()=>{const abort=new AbortController();setState({kind:"loading"});void apiClient.readArtifactContent(id,version,abort.signal).then(async value=>{if(!value.mediaType.startsWith("text/")&&!value.mediaType.includes("json")){setState({kind:"error",message:"This artifact is not a text document."});return}const text=await value.blob.text();if(!abort.signal.aborted)setState({kind:"text",text})}).catch(cause=>{if(!abort.signal.aborted)setState({kind:"error",message:cause instanceof Error?cause.message:"Unable to read artifact"})});return()=>abort.abort()},[id,version]);
 return state.kind==="text"?<ReadableMarkdown text={state.text}/>:<p>{state.kind==="loading"?"Reading saved document…":state.message}</p>;
}

function ReadableResult({ content }: { content: string }) {
  let value: unknown; try { value = JSON.parse(content); } catch { return <pre>{content}</pre>; }
  if (!value || typeof value !== "object" || Array.isArray(value)) return <pre>{content}</pre>;
  return <dl className="run-readable-result">{Object.entries(value).map(([key, item]) => <div key={key}><dt>{key.replaceAll("_", " ")}</dt><dd>{typeof item === "string" ? <ReadableMarkdown text={item} /> : <pre>{JSON.stringify(item, null, 2)}</pre>}</dd></div>)}</dl>;
}

// A deliberately small, inert Markdown reader. Raw HTML never becomes DOM and
// file contents cannot execute scripts or load remote resources.
export function ReadableMarkdown({ text }: { text: string }) { return <Markdown text={text} />; }
function RunArtifactReviews({events}:{events:TranscriptEvent[]}) {
 const {search,navigate,route}=useRouter();
 const reviews=new Map<string,{id:string;node:string}>();
 for(const event of events){const data=event.data as {class?:string;checkpointId?:string;nodeId?:string};if(event.kind==="approval.requested"&&data.class==="workflow_checkpoint"&&data.checkpointId)reviews.set(data.checkpointId,{id:event.subject,node:data.nodeId??"Document"});}
 const values=[...reviews.values()];const selected=new URLSearchParams(search).get("review");const active=values.find(v=>v.id===selected)??values[0];if(!active)return null;
 return <section className="run-artifact-review" aria-label="Artifact review"><nav aria-label="Documents to review">{values.map(value=><button key={value.id} aria-pressed={active.id===value.id} onClick={()=>{const q=new URLSearchParams(search);q.set("review",value.id);navigate(`/work/${encodeURIComponent(route.params.workId)}/run/${encodeURIComponent(route.params.runId)}?${q}`);}}>{value.node} · review</button>)}</nav><ArtifactReviewWorkspace key={active.id} approvalId={active.id} embedded /></section>;
}
