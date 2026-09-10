import type { EventKind } from "./transcriptModel";
import { eventColors, eventKindLabel } from "./theme";

export interface TranscriptToolsProps {
  filter: string;
  onFilter(value: string): void;
  kind: EventKind | "";
  onKind(value: EventKind | ""): void;
  following: boolean;
  onFollowLive(): void;
  raw: boolean;
  onRaw(value: boolean): void;
  onExport(): void;
  exportDisabled: boolean;
}

export function TranscriptTools({ filter, onFilter, kind, onKind, following, onFollowLive, raw, onRaw, onExport, exportDisabled }: TranscriptToolsProps) {
  return <div className="run-transcript-tools">
    <input aria-label="Search transcript" placeholder="Search transcript…" value={filter} onChange={(event) => onFilter(event.target.value)} />
    <select aria-label="Filter event type" value={kind} onChange={(event) => onKind(event.target.value as EventKind | "")}>
      <option value="">All event types</option>
      {Object.keys(eventColors).map((key) => <option key={key} value={key}>{eventKindLabel(key)}</option>)}
    </select>
    <button onClick={onFollowLive} aria-pressed={following}>Follow live</button>
    <label>Messages <select aria-label="Transcript format" value={raw ? "raw" : "rendered"} onChange={(event) => onRaw(event.target.value === "raw")}>
      <option value="rendered">Rendered</option>
      <option value="raw">Raw</option>
    </select></label>
    <button onClick={onExport} disabled={exportDisabled}>Export history</button>
  </div>;
}

export type RunViewMode = "terminal" | "turns" | "artifacts";

export interface RunViewTabsProps {
  mode: RunViewMode;
  turnCount: number;
  usage: { input?: number; output?: number; reasoning?: number };
  onSelect(mode: RunViewMode): void;
}

// Turn counting is a projection of saved events, never a provider boundary.
const turnsTitle = "Agent messages and tool calls; streamed fragments and tool results count once. Provider inference boundaries are not exposed.";

export function RunViewTabs({ mode, turnCount, usage, onSelect }: RunViewTabsProps) {
  const tabs = [["terminal", "Terminal"], ["turns", `Turns (${turnCount})`], ["artifacts", "Artifacts"]] as const;
  const counts = [["input", "Input"], ["output", "Output"], ["reasoning", "Reasoning"]] as const;
  return <div className="run-live-toolbar">
    <nav aria-label="Run view">{tabs.map(([id, label]) =>
      <button key={id} aria-pressed={mode === id} onClick={() => onSelect(id)} title={id === "turns" ? turnsTitle : undefined}>{label}</button>)}
    </nav>
    <div className="run-token-counts">{counts.map(([key, label]) =>
      <span key={key} title={usage[key] === undefined ? "Not reported by the provider" : `${label} tokens reported by the provider`}>
        <strong>{usage[key]?.toLocaleString() ?? "—"}</strong> {label}
      </span>)}
    </div>
  </div>;
}
