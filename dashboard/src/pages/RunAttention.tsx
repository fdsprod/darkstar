import { useRouter } from "../app/router";
import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { PreparationQuestions, isPreparationAttention } from "./CheckpointsPage";


type S = components["schemas"];
type Item = S["AttentionCheckpointV2"];

export function RunAttention({ runId, refreshRun }: { runId: string; refreshRun(): Promise<void> }) {
  const [items, setItems] = useState<Item[]>([]), [error, setError] = useState("");
  const load = useCallback(async (signal?: AbortSignal) => {
    try { const values: Item[] = []; let cursor: string | undefined;
      do { const page = await apiClient.listAttention({ runId, limit: 100, ...(cursor ? { cursor } : {}) }, signal); values.push(...page.items); cursor = page.nextCursor; } while (cursor);
      if (!signal?.aborted) { setItems(values); setError(""); }
    } catch (cause) { if (!signal?.aborted) setError(cause instanceof Error ? cause.message : "Unable to load questions"); }
  }, [runId]);
  useEffect(() => { const abort = new AbortController(); void load(abort.signal); const timer = setInterval(() => void load(abort.signal), 2000); return () => { abort.abort(); clearInterval(timer); }; }, [load]);
  const refresh = async () => { await load(); await refreshRun(); };
  if (!items.length && !error) return null;
  return <section className="run-attention" aria-label="Needs your attention">{error && <p role="alert">Questions could not be refreshed: {error}</p>}{items.map(item => <InlineDecision key={item.id} item={item} refresh={refresh} />)}</section>;
}

function InlineDecision({ item, refresh }: { item: Item; refresh(): Promise<void> }) {
  const {route,search,navigate}=useRouter();
  const [note, setNote] = useState(""); const [answers, setAnswers] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false), [error, setError] = useState(""); const lock = useRef(false);
  if (isPreparationAttention(item)) return <PreparationQuestions item={item} refresh={refresh} />;
  const request = item.kind === "input_required" ? item.subject.request as { questions?: Array<{ id: string; question?: string; prompt?: string; options?: Array<{ label: string; description?: string }> }> } : undefined;
  const questions = request?.questions ?? [];
  async function perform(action: string, singleAnswer?: { id: string; value: string }) {
    if (lock.current) return; lock.current = true; setBusy(true); setError("");
    try {
      const key = `inline-decision-${crypto.randomUUID()}`;
      if (item.kind === "workflow_control" || item.kind === "external_delivery") await apiClient.decideAttention(item.kind, item.id, item.resourceVersion, key, { action: action as "approve" | "deny" | "cancel", scopeDigest: item.subject.scopeDigest, policyDigest: item.subject.policyDigest, ...(note.trim() ? { comment: note.trim() } : {}) });
      else if (item.kind === "workflow_checkpoint") {
        if(action!=="approve"&&!note.trim())throw new Error("Add a note explaining the requested changes or rejection.");
        await apiClient.decideApproval(item.id, item.resourceVersion, key, { action: action as "approve" | "request_changes" | "reject", scopeDigest: item.subject.scopeDigest, policyDigest: item.subject.policyDigest, ...(note.trim() ? { comment: note.trim() } : {}) } as S["ArtifactCheckpointDecisionRequest"]);
      }
      else if (item.kind === "provider_permission") {
        if (action === "retry_delivery") await apiClient.retryProviderPermissionDelivery(item.id, item.resourceVersion);
        else await apiClient.decideProviderPermission(item.id, item.resourceVersion, key, { decision: action as "allow_once" | "deny" | "cancel", scopeDigest: item.subject.scopeDigest });
      } else if (item.kind === "input_required" && !isPreparationAttention(item)) {
        if (action === "retry_delivery") await apiClient.retryInputDelivery(item.id, item.resourceVersion);
        else {
          const values = singleAnswer ? { ...answers, [singleAnswer.id]: singleAnswer.value } : answers;
          if (!questions.length) throw new Error("This question format is unavailable. The original request is shown below.");
          if (questions.some(q => !values[q.id]?.trim())) throw new Error("Answer each question before sending.");
          await apiClient.answerInputRequest(item.id, item.resourceVersion, key, { scopeDigest: item.subject.scopeDigest, answer: { answers: Object.fromEntries(questions.map(q => [q.id, { answers: values[q.id].trim() }])) } });
        }
      }
      await refresh();
    } catch (cause) { setError(cause instanceof Error ? cause.message : "The decision could not be saved"); }
    finally { lock.current = false; setBusy(false); }
  }
  const title = item.kind === "workflow_control" && item.subject.nodeId ? `Review ${item.subject.nodeId}` : item.kind === "input_required" ? "The agent needs your answer" : item.kind === "provider_permission" ? "Permission needed" : "Your decision is needed";
  if (item.kind === "workflow_checkpoint") { if (new URLSearchParams(search).get("tab") === "artifacts") return null; return <article><h3>Document review needed</h3><button onClick={()=>{const q=new URLSearchParams(search);q.set("tab","artifacts");q.set("review",item.id);navigate(`/work/${encodeURIComponent(route.params.workId)}/run/${encodeURIComponent(route.params.runId)}?${q}`);}}>Review and annotate</button></article>; }
  return <article><header><span className="run-attention-dot" /><h2>{title}</h2></header>
    {item.kind === "workflow_control" && <p>Read the saved output in Artifacts, then approve to continue the workflow. Denying or cancelling stops this run.</p>}
    {item.kind === "provider_permission" && <><p>{item.subject.evidence.summary}</p><pre>{item.subject.scope.subject}</pre></>}
    {questions.map(question => <div className="run-inline-question" key={question.id}><label>{question.question ?? question.prompt}<textarea value={answers[question.id] ?? ""} onChange={e => setAnswers(values => ({ ...values, [question.id]: e.target.value }))} disabled={busy} /></label>{question.options?.map(option => <button type="button" key={option.label} title={option.description} disabled={busy} onClick={() => { if (questions.length === 1 || questions.every(q => q.id === question.id || answers[q.id]?.trim())) void perform("answer", { id: question.id, value: option.label }); else setAnswers(values => ({ ...values, [question.id]: option.label })); }}>{option.label}</button>)}</div>)}
    {item.kind === "input_required" && !questions.length && <pre>{JSON.stringify(request, null, 2)}</pre>}
    {item.kind !== "input_required" && item.kind !== "provider_permission" && <textarea aria-label="Decision note" placeholder="Optional note for the decision history…" value={note} maxLength={4096} onChange={e => setNote(e.target.value)} disabled={busy} />}
    <div className="run-inline-actions">{item.allowedActions.map(action => <button key={action} disabled={busy} onClick={() => void perform(action)}>{busy ? "Saving…" : action === "approve" && item.kind === "workflow_control" ? "Approve and continue" : action === "answer" ? "Send answers" : action.replaceAll("_", " ").replace(/^./, v => v.toUpperCase())}</button>)}</div>
    {error && <p role="alert">{error}</p>}
  </article>;
}
