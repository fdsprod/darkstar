import type { ReactNode } from "react";

export type AsyncState = "loading" | "success" | "error" | "stale" | "cancelled" | "validation";
export type EmptyStateKind = "empty" | "filtered" | "awaiting" | "unavailable";

export function ActionBar({ children, label = "Available actions" }: { children: ReactNode; label?: string }) {
  return <div className="action-bar" role="group" aria-label={label}>{children}</div>;
}

export function SectionHeader({ eyebrow, title, meta, actions }: { eyebrow: string; title: ReactNode; meta?: ReactNode; actions?: ReactNode }) {
  return <div className="section-heading"><div><p className="eyebrow">{eyebrow}</p><h2>{title}</h2></div>{meta}{actions && <ActionBar label={`${typeof title === "string" ? title : "Section"} actions`}>{actions}</ActionBar>}</div>;
}

export function StatusBadge({ children, tone = "neutral", label }: { children: ReactNode; tone?: string; label?: string }) {
  return <span className={`status-badge status-badge--${tone}`} aria-label={label}>{children}</span>;
}

export function AsyncPanel({ state, title, message, action, compact = false }: { state: AsyncState; title: string; message: ReactNode; action?: ReactNode; compact?: boolean }) {
  const busy = state === "loading";
  const failure = state === "error" || state === "stale" || state === "validation";
  return <section className={`async-panel async-panel--${state}${compact ? " async-panel--compact" : ""}`} aria-busy={busy} aria-live={failure ? "assertive" : "polite"} role={failure ? "alert" : "status"}>
    <span className="async-panel__indicator" aria-hidden="true" />
    <div><strong>{title}</strong><p>{message}</p></div>
    {action}
  </section>;
}

export function EmptyState({ kind, title, message, action, compact = false }: { kind: EmptyStateKind; title: string; message: ReactNode; action?: ReactNode; compact?: boolean }) {
  return <section className={`empty-state empty-state--${kind}${compact ? " empty-state--compact" : ""}`} data-empty-kind={kind}>
    <span className="empty-state__icon" aria-hidden="true">◇</span>
    <div><h2>{title}</h2><p>{message}</p></div>
    {action}
  </section>;
}

export function ActionGuidance({ id, children }: { id?: string; children: ReactNode }) {
  return <p id={id} className="action-guidance"><span aria-hidden="true">ⓘ</span>{children}</p>;
}
