import type { EventKind } from "./transcriptModel";

/** One colour per event kind, shared by the timeline ticks and entry headers. */
export const eventColors: Record<EventKind, string> = {
  message: "#61c486", reasoning: "#8ac5e8", tool: "#ba9aef", error: "#f07d70",
  decision: "#f0b45d", user: "#71b5ed", lifecycle: "#81929f",
};

export const clock = (time: number) => new Date(time).toLocaleTimeString([], { hour12: false });

export function localDate(time: number) {
  const date = new Date(time);
  return new Date(time - date.getTimezoneOffset() * 60000).toISOString().slice(0, 19);
}

export const eventKindLabel = (key: string) => key === "reasoning" ? "Reasoning summaries" : key[0].toUpperCase() + key.slice(1);
