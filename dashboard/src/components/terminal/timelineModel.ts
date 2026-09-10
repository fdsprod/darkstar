export type TimelineSelection =
  | { kind: "all" }
  | { kind: "live"; start: number }
  | { kind: "fixed"; start: number; end: number };

// The right edge is an intent to include future events, not a saved timestamp.
export function selectTimelineRange(range: [number, number] | null, start: number, end: number): TimelineSelection {
  if (!range) return { kind: "all" };
  const from = Math.max(start, Math.min(range[0], end));
  const to = Math.max(from, Math.min(range[1], end));
  if (to >= end) return from <= start ? { kind: "all" } : { kind: "live", start: from };
  return { kind: "fixed", start: from, end: to };
}

export function timelineRange(selection: TimelineSelection, end: number): [number, number] | null {
  switch (selection.kind) {
    case "all": return null;
    case "live": return [selection.start, Math.max(selection.start, end)];
    case "fixed": return [selection.start, selection.end];
  }
}
