import { useRef, type KeyboardEvent } from "react";

import { tabKeyTarget } from "../accessibility/keyboard";

export interface ContextTab<T extends string> { id: T; label: string; count?: number }

export function ContextTabs<T extends string>({ tabs, active, onSelect, label = "Work context" }: { tabs: readonly ContextTab<T>[]; active: T; onSelect(tab: T): void; label?: string }) {
  const refs = useRef<Array<HTMLButtonElement | null>>([]);
  function onKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const target = tabKeyTarget(index, event.key, tabs.length);
    if (target === undefined) return;
    event.preventDefault();
    refs.current[target]?.focus();
    onSelect(tabs[target].id);
  }
  return <div className="context-tabs" role="tablist" aria-label={label}>{tabs.map((tab, index) => <button ref={(value) => { refs.current[index] = value; }} type="button" role="tab" key={tab.id} tabIndex={active === tab.id ? 0 : -1} aria-selected={active === tab.id} aria-controls={`context-panel-${tab.id}`} onKeyDown={(event) => onKeyDown(event, index)} onClick={() => onSelect(tab.id)}>{tab.label}{tab.count !== undefined && <span>{tab.count}</span>}</button>)}</div>;
}

export function ContextPanel({ id, active, children }: { id: string; active: boolean; children: React.ReactNode }) {
  return <div id={`context-panel-${id}`} role="tabpanel" tabIndex={active ? 0 : -1} hidden={!active}>{children}</div>;
}
