import { useEffect, useRef, useState, type FormEvent } from "react";
import { getDashboardAuthorization } from "../api/bootstrap";
import { apiClient } from "../api/client";
import { readChatStream, type ChatEvent, type ChatMessage, type ChatTarget, type ChatModel, type ChatGeneration } from "./workflowChatModel";

// Host-scoped so the preference survives the daemon choosing a new local port.
const preferenceKey = "darkstar_workflow_chat_generation";
function readGeneration(): ChatGeneration | undefined {
  try {
    const raw = document.cookie.split("; ").find(value => value.startsWith(preferenceKey + "="));
    const value = raw && JSON.parse(decodeURIComponent(raw.slice(preferenceKey.length + 1)));
    return value && typeof value.model === "string" && typeof value.effort === "string" ? value : undefined;
  } catch { return undefined; }
}

export function WorkflowChat({ target, disabled, onEvent, onBusy }: {
  target: ChatTarget; disabled: boolean; onEvent(event: ChatEvent): void; onBusy(value: boolean): void;
}) {
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState("");
  const [state, setState] = useState<{ kind: "idle" } | { kind: "running" } | { kind: "error"; message: string }>({ kind: "idle" });
  const [options, setOptions] = useState<string[]>([]);
  const [models, setModels] = useState<{ kind: "loading" } | { kind: "ready"; values: ChatModel[] } | { kind: "error" }>({ kind: "loading" });
  const [generation, setGeneration] = useState<ChatGeneration | undefined>(readGeneration);
  const [modelRefresh, setModelRefresh] = useState(0);
  const [status, setStatus] = useState("");
  useEffect(() => {
    if (models.kind !== "ready" || !generation) return;
    try { document.cookie = `${preferenceKey}=${encodeURIComponent(JSON.stringify(generation))}; Path=/; Max-Age=31536000; SameSite=Strict`; } catch { /* Preference storage is optional in restricted browsers. */ }
  }, [generation, models.kind]);
  const abort = useRef<AbortController | undefined>(undefined);
  const events = useRef(onEvent); events.current = onEvent;
  const busyCallback = useRef(onBusy); busyCallback.current = onBusy;
  const log = useRef<HTMLDivElement>(null);
  useEffect(() => () => { abort.current?.abort(); busyCallback.current(false); }, []);
  useEffect(() => { log.current?.scrollTo({ top: log.current.scrollHeight }); }, [messages, status]);
  useEffect(() => {
    const controller = new AbortController(); setModels({ kind: "loading" });
    void apiClient.operation("listWorkflowChatModels", { signal: controller.signal }).then(values => {
      if (controller.signal.aborted) return;
      setModels({ kind: "ready", values });
      setGeneration(current => {
        if (current && values.some(model => model.id === current.model && model.efforts.includes(current.effort))) return current;
        const preferred = values.find(model => model.isDefault);
        return preferred ? { model: preferred.id, effort: preferred.defaultEffort } : undefined;
      });
    }).catch(() => { if (!controller.signal.aborted) { setModels({ kind: "error" }); setGeneration(undefined); } });
    return () => controller.abort();
  }, [modelRefresh]);
  const choices = models.kind === "ready" ? models.values : [];
  const selectedModel = choices.find(model => model.id === generation?.model);

  async function submit(event: FormEvent) {
    event.preventDefault();
    await send(input);
  }

  async function send(message: string) {
    if (!message.trim() || disabled || abort.current || state.kind === "running") return;
    const history: ChatMessage[] = [...messages, { role: "user", text: message.trim() }];
    if (history.length > 100) { setState({ kind: "error", message: "This conversation is full. Start a fresh chat to continue." }); return; }
    setMessages(history); setInput(""); setOptions([]); setStatus("Thinking…"); setState({ kind: "running" }); onBusy(true);
    const controller = new AbortController(); abort.current = controller;
    let reply = "", separateNextText = false;
    const append = (text: string) => { reply += text; setMessages([...history, { role: "assistant", text: reply }]); };
    try {
      const authorization = getDashboardAuthorization();
      const response = await fetch("/api/v1/workflows/chat", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", Accept: "application/x-ndjson", ...(authorization ? { Authorization: authorization } : {}) }, body: JSON.stringify({ target, messages: history, ...(generation ? { generation } : {}) }), signal: controller.signal });
      if (!response.ok) { const error = await response.json(); throw new Error(error.message ?? "Workflow chat is unavailable."); }
      if (!response.body) throw new Error("Workflow chat stream is unavailable.");
      await readChatStream(response.body, (value) => {
        if (controller.signal.aborted) return;
        switch (value.kind) {
          case "text": append(`${separateNextText ? "\n\n" : ""}${value.payload.text}`); separateNextText = false; break;
          case "question": append(`${reply ? "\n\n" : ""}${value.payload.question}`); separateNextText = true; setOptions(value.payload.options); setStatus("Waiting for your reply"); break;
          case "conflict": append(`\n${value.payload.message}`); break;
          case "draft": setStatus(`Saved revision ${value.payload.revision}`); break;
          case "validation": setStatus(value.payload.findings.length ? `${value.payload.findings.length} validation findings` : "Validation passed"); break;
          case "status": setStatus(value.payload.message); break;
          case "error": throw new Error(value.payload.message);
          case "done": setStatus(current => ["Inspecting workflow…", "Thinking…"].includes(current) ? "Ready" : current); break;
        }
        events.current(value);
      });
      setState({ kind: "idle" });
    } catch (cause) {
      setState({ kind: "error", message: controller.signal.aborted ? "Stopped. Saved changes remain in the draft." : cause instanceof Error ? cause.message : "Workflow chat failed." });
    } finally { abort.current = undefined; onBusy(false); }
  }

  return <aside className="workflow-chat" aria-label="Workflow chat">
    <header><strong>Edit with chat</strong><button className="button button--compact" disabled={state.kind === "running"} onClick={() => { setMessages([]); setOptions([]); setStatus(""); setState({ kind: "idle" }); }}>Clear chat</button></header>
    <p>Describe a change or build a whole workflow. Saved edits appear live. You control publishing.</p>
    <div ref={log} className="workflow-chat-log" role="log" aria-live="polite">{messages.length === 0 && <p>Try “Add a human review before deployment” or “Build a workflow for investigating and fixing a bug.”</p>}{messages.map((message, index) => <div key={index} className={`workflow-chat-message workflow-chat-message--${message.role}`}><strong>{message.role === "user" ? "You" : "Assistant"}</strong><p>{message.text}</p></div>)}</div>
    {options.length > 0 && <div className="workflow-chat-options">{options.map((option, index) => <button key={index} className="button" disabled={disabled || state.kind === "running"} onClick={() => void send(option)}>{option}</button>)}</div>}
    {status && <small role="status">{status}</small>}
    {state.kind === "error" && <p role="alert">{state.message}</p>}
    <form className="workflow-chat-composer" onSubmit={event => void submit(event)}>
      <label className="sr-only" htmlFor="workflow-chat-message">Message</label>
      <textarea id="workflow-chat-message" value={input} maxLength={32000} onChange={event => setInput(event.target.value)} onKeyDown={event => { if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} placeholder="Describe a workflow or ask for a change…" disabled={state.kind === "running"} rows={3} />
      <footer>
        <div className="workflow-chat-generation">
          <select aria-label="Chat model" title="Model" value={generation?.model ?? ""} disabled={state.kind === "running" || models.kind !== "ready"} onChange={event => { const model = choices.find(value => value.id === event.target.value); setGeneration(model ? { model: model.id, effort: model.defaultEffort } : undefined); }}><option value="">{models.kind === "loading" ? "Loading models…" : "Provider default"}</option>{choices.map(model => <option key={model.id} value={model.id}>{model.name || model.id}</option>)}</select>
          <select aria-label="Chat effort" title="Reasoning effort" value={generation?.effort ?? ""} disabled={state.kind === "running" || !selectedModel} onChange={event => { if (selectedModel) setGeneration({ model: selectedModel.id, effort: event.target.value }); }}>{!selectedModel && <option value="">Default effort</option>}{selectedModel?.efforts.map(effort => <option key={effort} value={effort}>{effort[0].toUpperCase() + effort.slice(1)}</option>)}</select>
        </div>
        {state.kind === "running" ? <button className="workflow-chat-send" aria-label="Stop" title="Stop" type="button" onClick={() => abort.current?.abort()}>■</button> : <button className="workflow-chat-send" aria-label="Send" title="Send (Enter)" disabled={disabled || !input.trim()} type="submit">↑</button>}
      </footer>
    </form>
    {models.kind === "error" && <small>Model list unavailable; using provider defaults. <button className="workflow-chat-retry" disabled={state.kind === "running"} onClick={() => setModelRefresh(value => value + 1)}>Retry</button></small>}
    {disabled && <small>Wait for the current save or resolve the editor conflict before sending.</small>}
  </aside>;
}
