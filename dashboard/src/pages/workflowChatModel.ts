import type { components } from "../api/schema.generated";

type Schemas = components["schemas"];
export type ChatTarget = Schemas["WorkflowChatTarget"];
export type ChatMessage = Schemas["WorkflowChatMessage"];
export type ChatEvent = Schemas["WorkflowChatEvent"];
export type ChatModel = Schemas["WorkflowChatModel"];
export type ChatGeneration = Schemas["WorkflowChatGeneration"];

// A response can split UTF-8 code points and NDJSON records at any byte boundary.
export async function readChatStream(body: ReadableStream<Uint8Array>, receive: (event: ChatEvent) => void) {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let pending = "", terminal = false;
  const line = (value: string) => {
    if (!value.trim()) return;
    const event = JSON.parse(value) as ChatEvent;
    if (!["draft", "validation", "text", "question", "conflict", "status", "error", "done"].includes(event.kind)) throw new Error("Unsupported workflow chat event.");
    if (terminal) throw new Error("Unexpected event after chat completion.");
    terminal = event.kind === "done" || event.kind === "error";
    receive(event);
  };
  try {
    for (;;) {
      const { value, done } = await reader.read();
      pending += decoder.decode(value, { stream: !done });
      if (pending.length > 4 * 1024 * 1024) throw new Error("Workflow chat event is too large.");
      let end: number;
      while ((end = pending.indexOf("\n")) >= 0) { line(pending.slice(0, end)); pending = pending.slice(end + 1); }
      if (done) break;
    }
    line(pending);
    if (!terminal) throw new Error("Chat disconnected. Saved draft changes are retained; review the canvas before retrying.");
  } finally { await reader.cancel().catch(() => {}); reader.releaseLock(); }
}
