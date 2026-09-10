import type { ReactNode } from "react";

export interface ContextUsage {
  used: number;
  window: number;
}

export interface TerminalStatusBarProps {
  /** The node the attempt is running, shown like a branch in a shell prompt. */
  node?: string;
  scope?: string;
  provider?: string;
  model?: string;
  effort?: string;
  context?: ContextUsage;
  hint?: ReactNode;
}

const compact = (value: number) => value >= 1000 ? `${(value / 1000).toFixed(value >= 10_000 ? 0 : 1)}k` : String(value);

// Context pressure uses the window the provider reported with its usage. When a
// provider never reports one, the meter is omitted rather than guessed.
export function ContextMeter({ used, window: size }: ContextUsage) {
  const percent = size > 0 ? Math.min(100, Math.round((used / size) * 100)) : 0;
  return <span className="run-context-meter" title={`${used.toLocaleString()} of ${size.toLocaleString()} tokens`}>
    <span className="run-context-meter__rail" role="progressbar" aria-label="Context usage" aria-valuenow={percent} aria-valuemin={0} aria-valuemax={100} aria-valuetext={`${percent}% of the ${compact(size)} token context window`}>
      <span className="run-context-meter__fill" style={{ width: `${percent}%` }} />
    </span>
    <span className="run-context-meter__value">{percent}%</span>
  </span>;
}

export function TerminalStatusBar({ node, scope, provider, model, effort, context, hint }: TerminalStatusBarProps) {
  const segments: ReactNode[] = [];
  if (node) segments.push(<span key="node" className="run-status-bar__node">[{node}]</span>);
  if (scope) segments.push(<span key="scope" className="run-status-bar__scope">({scope})</span>);
  if (provider) segments.push(<span key="provider">{provider}</span>);
  if (model) segments.push(<span key="model" className="run-status-bar__model">{model}</span>);
  if (effort) segments.push(<span key="effort">effort:{effort}</span>);
  if (context && context.window > 0) segments.push(<ContextMeter key="context" {...context} />);
  if (!segments.length && !hint) return null;
  return <div className="run-status-bar">
    <div className="run-status-bar__facts">
      {segments.map((segment, index) => <span key={index} className="run-status-bar__cell">
        {index > 0 && <span className="run-status-bar__sep" aria-hidden="true">|</span>}{segment}
      </span>)}
    </div>
    {hint && <p className="run-status-bar__hint">{hint}</p>}
  </div>;
}
