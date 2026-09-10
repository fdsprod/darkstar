import type { components } from "../../api/schema.generated";

export type TranscriptEvent = components["schemas"]["RunTranscriptEvent"];
export type EventKind = "message" | "reasoning" | "tool" | "error" | "decision" | "user" | "lifecycle";
export interface TranscriptEntry { id: string; time: number; end: number; position: number; kind: EventKind; title: string; text: string; output: string; turn: string; attempt: string; status?: string; raw: TranscriptEvent[] }
export interface Usage { input?: number; output?: number; reasoning?: number; cached?: number }
type Obj = Record<string, any>;
const object = (value: unknown): Obj => value && typeof value === "object" && !Array.isArray(value) ? value as Obj : {};
const text = (value: unknown): string => typeof value === "string" ? value : value == null ? "" : JSON.stringify(value, null, 2);
const words = (value: string) => value.replaceAll(/[._]/g, " ");

// Deltas update the owning item. Completion snapshots replace matching fields,
// so command output and assistant messages are never repeated on replay.
export function buildTranscript(events: readonly TranscriptEvent[]) {
  const entries: TranscriptEntry[] = [], items = new Map<string, TranscriptEntry>(), turns = new Set<string>();
  const activeTurns = new Map<string, string>(), usageByThread = new Map<string, Usage>();
  let gaps = 0;
  for (const event of events) {
    const data = object(event.data), payload = object(data.payload), params = object(payload.params), item = object(params.item);
    const method = text(payload.providerMethod || payload.method), time = Date.parse(event.time);
    const nativeTurn = text(params.turnId || object(params.turn).id || data.providerTurnId);
    if (nativeTurn) { activeTurns.set(event.subject, `${event.subject}:${nativeTurn}`); turns.add(`${event.subject}:${nativeTurn}`); }
    const turn = activeTurns.get(event.subject) ?? "";
    const add = (kind: EventKind, title: string, body = "", id = String(event.position)) => {
      const entry: TranscriptEntry = { id, time, end: time, position: event.position, kind, title, text: body, output: "", turn, attempt: event.subject, raw: [event] };
      entries.push(entry); return entry;
    };
    if (event.kind === "attempt.provider_event") {
      if (data.historyGap) { gaps++; continue; }
      if (method === "thread/tokenUsage/updated") {
        const total = object(object(params.tokenUsage).total);
        const usage: Usage = {};
        for (const [field, native] of [["input", "inputTokens"], ["output", "outputTokens"], ["reasoning", "reasoningOutputTokens"], ["cached", "cachedInputTokens"]] as const) if (typeof total[native] === "number") usage[field] = total[native];
        usageByThread.set(text(params.threadId || data.providerThreadId || event.subject), usage); continue;
      }
      if (method === "turn/started" || method === "turn/completed") continue;
      if (method === "error") { add("error", "Agent error", text(params.message || object(params.error).message || params)); continue; }
      if (method === "turn/plan/updated") { add("message", "Plan", Array.isArray(params.plan) ? params.plan.map((p: Obj) => `${p.status === "completed" ? "✓" : "○"} ${p.step}`).join("\n") : text(params.explanation)); continue; }
      const itemId = text(item.id || params.itemId || params.callId || data.providerItemId);
      const itemType = text(item.type || (method === "item/tool/call" ? "dynamicToolCall" : ""));
      const isMessage = itemType === "agentMessage" || method === "item/agentMessage/delta";
      const isUser = itemType === "userMessage";
      const isReasoning = itemType === "reasoning" || method.includes("reasoning/summary");
      const isTool = ["commandExecution", "mcpToolCall", "dynamicToolCall", "fileChange", "webSearch", "imageView"].includes(itemType) || method === "item/commandExecution/outputDelta" || method === "item/fileChange/outputDelta";
      if (!(isMessage || isReasoning || isTool || isUser) || !itemId) continue;
      const key = `${event.subject}:${itemId}`;
      let entry = items.get(key);
      if (!entry) { entry = add(isMessage ? "message" : isUser ? "user" : isReasoning ? "reasoning" : "tool", isMessage ? "Assistant" : isUser ? "Agent input" : isReasoning ? "Reasoning summary" : "Tool", "", key); items.set(key, entry); }
      else entry.raw.push(event);
      entry.end = time;
      if (turn) entry.turn = turn;
      if (method.endsWith("/delta") || method.endsWith("/outputDelta") || method.endsWith("/summaryTextDelta")) {
        if (isTool) entry.output += text(params.delta); else entry.text += text(params.delta);
      } else if (method.endsWith("/summaryPartAdded")) { if (entry.text) entry.text += "\n\n"; }
      else if (isMessage) { if (typeof item.text === "string") entry.text = item.text; }
      else if (isUser) { entry.text = Array.isArray(item.content) ? item.content.map((c: Obj) => c.text ?? "").join("\n") : text(item.text); }
      else if (isReasoning) { if (Array.isArray(item.summary) && item.summary.length) entry.text = item.summary.map((part: unknown) => typeof part === "string" ? part : text(object(part).text)).join("\n\n"); }
      else if (isTool) {
        entry.title = itemType === "commandExecution" ? "Terminal" : itemType === "fileChange" ? "File changes" : text(item.tool || item.name || itemType) || entry.title;
        if (item.command) entry.text = text(item.command);
        else if (item.arguments != null) entry.text = text(item.arguments);
        else if (item.changes) entry.text = text(item.changes);
        else if (item.query) entry.text = text(item.query);
        if (typeof item.aggregatedOutput === "string") entry.output = item.aggregatedOutput;
        else if (item.result != null) entry.output = text(item.result);
        else if (item.content != null) entry.output = text(item.content);
        else if (item.output != null) { const output = object(item.output); entry.output = Array.isArray(output.contentItems) ? output.contentItems.map((part: Obj) => text(part.text)).join("\n") : text(item.output); }
        if (method === "item/tool/call") { entry.title = text(params.tool); entry.text = text(params.arguments); if(data.kind === "tool.completed")entry.status="Result was not recorded by the previous version"; }
        if (item.error) { entry.output += "\n" + text(item.error); entry.status = "failed"; }
        else if (item.exitCode != null) entry.status = `exit ${item.exitCode}`;
        else if (item.status) entry.status = text(item.status);
      }
      continue;
    }
    if (event.kind === "run.guidance_requested") { const e = add("user", "You · saved, awaiting delivery confirmation", text(data.message), text(data.id)); items.set(text(data.id), e); continue; }
    if (event.kind === "run.guidance_delivery") { const e = items.get(text(data.id)); if (e) { e.title = data.status === "accepted" ? "You · delivered to agent" : "You · delivery unconfirmed"; e.raw.push(event); } continue; }
    if (event.kind === "approval.requested") { add("decision", "Review needed", data.nodeId ? `Review the output from ${data.nodeId} to continue.` : "A human decision is needed."); continue; }
    if (event.kind === "approval.decided" || event.kind === "approval.cancelled") { add("decision", event.kind === "approval.cancelled" ? "Review cancelled" : `Human decision · ${words(text(data.action))}`, text(data.comment)); continue; }
    if (event.kind.endsWith(".failed") || event.kind.endsWith(".admission_failed")) { add("error", words(event.kind), text(data.message || object(data.failure).message || object(data.issue).message || data)); continue; }
    if (["run.completed", "run.cancelled", "run.paused", "run.resumed", "visit.started", "visit.succeeded", "visit.waiting_checkpoint"].includes(event.kind)) add("lifecycle", words(event.kind), text(data.nodeId));
  }
  const usage: Usage = {};
  for (const value of usageByThread.values()) for (const field of ["input", "output", "reasoning", "cached"] as const) if (value[field] !== undefined) usage[field] = (usage[field] ?? 0) + value[field]!;
  // Provider turn IDs encompass an entire user-to-agent run. Count observable
  // model actions instead: one message or tool invocation, never its deltas/results.
  const modelActions = entries.filter(e => e.kind === "tool" || (e.kind === "message" && e.title === "Assistant"));
  for (const entry of entries) entry.turn = "";
  for (const entry of modelActions) entry.turn = entry.id;
  for (const entry of entries) if (entry.kind === "reasoning") entry.turn = modelActions.find(action => action.attempt === entry.attempt && action.position >= entry.position)?.id ?? "";
  return { entries: entries.filter(e => e.kind !== "reasoning" || e.text), turns: modelActions.map(e => e.id), providerTurns: [...turns], usage, gaps, missingToolResults: entries.filter(e=>e.status==="Result was not recorded by the previous version").length };
}

export function inTimeRange(entry: TranscriptEntry, range: [number, number] | null) { return !range || (entry.end >= range[0] && entry.time <= range[1]); }
