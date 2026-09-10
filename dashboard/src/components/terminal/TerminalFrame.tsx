import type { ReactNode, RefObject, UIEvent } from "react";

/**
 * The terminal chrome: window lights, subject line, and connection state. The
 * dock holds the composer and status bar inside the same window, so input reads
 * as part of the session rather than a control floating beneath it.
 */
export function TerminalFrame({ title, state, children, dock }: { title: ReactNode; state: ReactNode; children?: ReactNode; dock?: ReactNode }) {
  return <div className="run-terminal">
    <header className="run-terminal-title">
      <span className="terminal-lights" aria-hidden="true"><i /><i /><i /></span>
      <span>{title}</span>
      <span className="run-terminal-state">{state}</span>
    </header>
    {children}
    {dock && <div className="run-terminal__dock">{dock}</div>}
  </div>;
}

export function TranscriptScroll({ turns, scrollRef, onScroll, children }: {
  turns: boolean;
  scrollRef: RefObject<HTMLDivElement | null>;
  onScroll(event: UIEvent<HTMLDivElement>): void;
  children: ReactNode;
}) {
  return <div className={`run-transcript-scroll ${turns ? "run-transcript-turns" : ""}`} ref={scrollRef} onScroll={onScroll}>{children}</div>;
}

export function TranscriptEmpty({ children }: { children: ReactNode }) {
  return <p className="run-transcript-empty">{children}</p>;
}

export function HistoryNotice({ children, tone = "gap" }: { children: ReactNode; tone?: "gap" | "error" }) {
  return tone === "error"
    ? <p className="run-inline-error" role="alert">{children}</p>
    : <p className="run-history-gap">{children}</p>;
}

export function ShowMoreHistory({ hidden, onShowMore }: { hidden: number; onShowMore(): void }) {
  return <button className="run-more-history" onClick={onShowMore}>Show more ({hidden} events)</button>;
}

/** The dock on its own, for views that replace the transcript but keep input. */
export function ComposerDock({ children }: { children: ReactNode }) {
  return <div className="run-terminal run-terminal--dock"><div className="run-terminal__dock">{children}</div></div>;
}
