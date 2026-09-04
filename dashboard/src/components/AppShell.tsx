import { useEffect, useRef, useState, type RefObject } from "react";

import { AppLink, RouteView, useRouter } from "../app/router";
import { useDashboardState } from "../state/DashboardStateProvider";
import { Icon, type IconName } from "./Icon";

interface NavItem { label: string; to: string; icon: IconName; routeIds: string[] }

const primaryNavigation: NavItem[] = [
  { label: "Board", to: "/board", icon: "board", routeIds: ["board", "work", "run", "readiness"] },
  { label: "Checkpoints", to: "/checkpoints", icon: "checkpoints", routeIds: ["checkpoints"] },
  { label: "Agents", to: "/agents", icon: "agents", routeIds: ["agents"] },
  { label: "Workflows", to: "/workflows", icon: "workflow", routeIds: ["workflows"] },
];

export function AppShell() {
  const { route } = useRouter();
  const { state } = useDashboardState();
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const paletteRef = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const openPalette = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        paletteRef.current?.showModal();
      }
    };
    window.addEventListener("keydown", openPalette);
    return () => window.removeEventListener("keydown", openPalette);
  }, []);

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">Skip to content</a>
      <button className="sidebar-scrim" aria-label="Close navigation" data-visible={sidebarOpen} onClick={() => setSidebarOpen(false)} />
      <aside className="sidebar" data-open={sidebarOpen} aria-label="Application navigation">
        <div className="sidebar__brand-row">
          <AppLink className="brand" to="/board" onNavigate={() => setSidebarOpen(false)}>
            <span className="brand__mark" aria-hidden="true"><span /></span>
            <span>DARKSTAR</span>
          </AppLink>
          <button className="icon-button sidebar__close" type="button" aria-label="Close navigation" onClick={() => setSidebarOpen(false)}><Icon name="x" /></button>
        </div>

        <div className="workspace-switcher" aria-label="Current workspace">
          <span className="workspace-switcher__avatar">SF</span>
          <span className="workspace-switcher__copy"><strong>Software Factory</strong><small>Local workspace</small></span>
          <Icon name="chevron-down" />
        </div>

        <nav className="primary-nav" aria-label="Primary">
          <p className="nav-label">Workspace</p>
          {primaryNavigation.map((item) => {
            const active = item.routeIds.includes(route.id);
            return (
              <AppLink key={item.to} to={item.to} className="nav-item" ariaCurrent={active ? "page" : undefined} onNavigate={() => setSidebarOpen(false)}>
                <Icon name={item.icon} /><span>{item.label}</span>
              </AppLink>
            );
          })}
        </nav>

        <div className="sidebar__footer">
          <AppLink to="/settings" className="nav-item" ariaCurrent={route.id === "settings" ? "page" : undefined} onNavigate={() => setSidebarOpen(false)}>
            <Icon name="settings" /><span>Settings & Health</span>
          </AppLink>
          <div className="control-plane" data-connection={state.connection} aria-live="polite">
            <span className="control-plane__indicator" aria-hidden="true" />
            <span><strong>Local control plane</strong><small>{connectionLabel(state.connection, state.hydration)}</small></span>
          </div>
        </div>
      </aside>

      <section className="workspace-frame">
        <header className="topbar">
          <div className="topbar__location">
            <button className="icon-button menu-button" type="button" aria-label="Open navigation" onClick={() => setSidebarOpen(true)}><Icon name="menu" /></button>
            <span className="topbar__section">{route.section}</span><span className="topbar__separator" aria-hidden="true">/</span><strong>{route.title}</strong>
          </div>
          <div className="topbar__actions">
            <button className="command-button" type="button" onClick={() => paletteRef.current?.showModal()}>
              <Icon name="search" /><span>Search or jump to…</span><kbd>Ctrl K</kbd>
            </button>
            <AppLink className="button button--primary button--compact" to="/board?create=1"><Icon name="create" /><span>Create work</span></AppLink>
          </div>
        </header>
        <main id="main-content" className="main-content" tabIndex={-1}><RouteView /></main>
      </section>

      <CommandPalette dialogRef={paletteRef} />
    </div>
  );
}

function connectionLabel(connection: string, hydration: string) {
  if (hydration === "loading") return "Loading authoritative state";
  if (hydration === "error") return "State sync unavailable";
  if (connection === "live") return "Live · event stream connected";
  if (connection === "reconnecting") return "Reconnecting from cursor";
  if (connection === "offline") return "Updates paused";
  return "Connecting to event stream";
}

function CommandPalette({ dialogRef }: { dialogRef: RefObject<HTMLDialogElement | null> }) {
  const [query, setQuery] = useState("");
  const links = [
    ...primaryNavigation,
    { label: "Settings & Health", to: "/settings", icon: "settings" as const, routeIds: ["settings"] },
  ].filter((item) => item.label.toLowerCase().includes(query.trim().toLowerCase()));

  return (
    <dialog className="command-palette" ref={dialogRef} onClose={() => setQuery("")}>
      <div className="command-palette__search">
        <Icon name="search" />
        <label className="sr-only" htmlFor="command-search">Search dashboard</label>
        <input id="command-search" autoFocus value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Where do you want to go?" />
        <button type="button" className="key-button" onClick={() => dialogRef.current?.close()}>Esc</button>
      </div>
      <div className="command-palette__results">
        <p className="nav-label">Navigate</p>
        {links.length === 0 ? <p className="command-palette__empty">No destinations match “{query}”.</p> : links.map((item) => (
          <AppLink key={item.to} to={item.to} className="command-result" onNavigate={() => dialogRef.current?.close()}>
            <span className="command-result__icon"><Icon name={item.icon} /></span><span>{item.label}</span><Icon name="arrow-right" />
          </AppLink>
        ))}
      </div>
      <div className="command-palette__footer"><span><kbd>↑</kbd><kbd>↓</kbd> to navigate</span><span><kbd>↵</kbd> to open</span></div>
    </dialog>
  );
}
