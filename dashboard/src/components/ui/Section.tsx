import type { ReactNode } from "react";

// `.detail-section` is the repeated card surface across detail and run views.
export function DetailSection({ children, className = "", label }: { children: ReactNode; className?: string; label?: string }) {
  return <section className={`detail-section ${className}`.trim()} aria-label={label}>{children}</section>;
}

// A definition list rendered the way diagnostics and receipts already expect.
export function DescriptionList({ entries }: { entries: readonly { term: ReactNode; detail: ReactNode }[] }) {
  return <dl>{entries.map((entry, index) => <div key={index}><dt>{entry.term}</dt><dd>{entry.detail}</dd></div>)}</dl>;
}

export function ScopeBadge({ children, tone }: { children: ReactNode; tone?: string }) {
  return <span className={tone ? `scope-badge scope-badge--${tone}` : "scope-badge"}>{children}</span>;
}

export function VisuallyHidden({ children }: { children: ReactNode }) {
  return <span className="sr-only">{children}</span>;
}
