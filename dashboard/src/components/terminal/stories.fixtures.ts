import type { TranscriptEntry, TranscriptEvent } from "./transcriptModel";

// Fixed times keep the catalog deterministic for visual review. Nothing in the
// application imports this module.
const base = Date.parse("2026-09-09T14:32:00Z");
export const start = base;
export const end = base + 214_000;

const rawEvent = (position: number, time: number): TranscriptEvent => ({
  position, time: new Date(time).toISOString(), kind: "attempt.provider_event",
  subject: "attempt-7f3c", data: { payload: { providerMethod: "item/agentMessage/delta", params: { delta: "…" } } },
} as unknown as TranscriptEvent);

let seq = 0;
function make(partial: Partial<TranscriptEntry> & Pick<TranscriptEntry, "kind" | "title">): TranscriptEntry {
  const position = ++seq;
  const time = base + position * 17_000;
  return {
    id: `e${position}`, time, end: time + 2_000, position, text: "", output: "",
    turn: "", attempt: "attempt-7f3c", raw: [rawEvent(position, time)], ...partial,
  };
}

export const entries: TranscriptEntry[] = [
  make({ kind: "lifecycle", title: "visit started", text: "prepare-workspace" }),
  make({ kind: "user", title: "Agent input", text: "Add a toolchain verification step to the Windows CI workflow." }),
  make({
    kind: "reasoning", title: "Reasoning summary",
    text: "The workflow already pins Go and Node. The missing piece is asserting the pinned versions before the build runs, so a stale PATH entry cannot select a different toolchain.",
  }),
  make({
    kind: "tool", title: "Terminal", status: "exit 0",
    text: "pwsh ./scripts/Assert-Toolchain.ps1",
    output: "Go 1.24.0 (pinned)\nNode 22.12.0 (pinned)\nnpm 10.9.0 (pinned)\nAll pinned toolchain versions verified.",
  }),
  make({
    kind: "message", title: "Assistant",
    text: "I added an **Assert-Toolchain** step ahead of the build.\n\n- verifies Go, Node, and npm against the pinned versions\n- fails fast with the offending path\n\nThe reproducible-build check is unaffected.",
  }),
  make({
    kind: "tool", title: "File changes", status: "failed",
    text: ".github/workflows/windows-ci.yml",
    output: "error: the workflow already defines a step named \"Verify pinned toolchain\"",
  }),
  make({ kind: "error", title: "Agent error", text: "Tool call failed after 2 attempts; the run will retry with a fresh workspace." }),
  make({ kind: "decision", title: "Review needed", text: "Review the output from implement-ci to continue." }),
  make({ kind: "decision", title: "Human decision · request revisions", text: "Reuse the existing step instead of adding a second one." }),
  make({ kind: "lifecycle", title: "run completed", text: "deliver" }),
];

export const usage = { input: 48_312, output: 6_074, reasoning: 3_200 };

export const turnNumbers = new Map(entries.filter((entry) => entry.kind === "tool" || entry.title === "Assistant").map((entry, index) => [entry.id, index + 1]));
