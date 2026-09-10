import type { CSSProperties } from "react";

import { ReadableResponse } from "../document/ReadableResponse";
import type { TranscriptEntry } from "./transcriptModel";
import { clock, eventColors } from "./theme";

/** Rendered prose is only safe for the kinds the daemon records as readable. */
const readable = (entry: TranscriptEntry) =>
  entry.kind === "message" || entry.kind === "reasoning" || entry.kind === "user" || entry.title === "submit_output";

// Human-readable activity stays compact. Saved events are unchanged and remain
// available in full through Export history.
export function TranscriptRow({ entry, raw }: { entry: TranscriptEntry; raw: boolean }) {
  return <article className={`run-transcript-entry run-transcript-entry--${entry.kind}`} style={{ "--event-color": eventColors[entry.kind] } as CSSProperties}>
    <header><time>{clock(entry.time)}</time><i aria-hidden="true" /><strong>{entry.title}</strong>{entry.status && <span>{entry.status}</span>}</header>
    {entry.text && (!raw && readable(entry) ? <ReadableResponse text={entry.text} /> : <pre>{entry.text}</pre>)}{entry.output && ((raw || !["read_input", "submit_output"].includes(entry.title)) ? <pre className="run-tool-output">{entry.output}</pre> : <ReadableResponse text={entry.output} />)}
  </article>;
}

export function TurnHeading({ number, time }: { number: number | undefined; time: number }) {
  return <h3 className="run-turn-heading">Turn {number} <small>{clock(time)}</small></h3>;
}
