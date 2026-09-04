import {
  createContext,
  type MouseEvent,
  type ReactNode,
  useContext,
  useEffect,
  useMemo,
  useSyncExternalStore,
} from "react";

import { BoardPage } from "../pages/BoardPage";
import { PlaceholderPage } from "../pages/PlaceholderPage";

export type AppRouteId = "board" | "work" | "run" | "checkpoints" | "agents" | "workflows" | "settings" | "artifact" | "not-found";

export interface AppRoute {
  id: AppRouteId;
  path: string;
  title: string;
  section: string;
  params: Readonly<Record<string, string>>;
}

const routePatterns = [
  { id: "board", path: "/board", title: "Board", section: "Workspace" },
  { id: "run", path: "/work/:workId/run/:runId", title: "Run", section: "Work" },
  { id: "work", path: "/work/:workId", title: "Work item", section: "Work" },
  { id: "checkpoints", path: "/checkpoints", title: "Checkpoints", section: "Workspace" },
  { id: "agents", path: "/agents", title: "Agents", section: "Workspace" },
  { id: "workflows", path: "/workflows", title: "Workflows", section: "Workspace" },
  { id: "settings", path: "/settings", title: "Settings & Health", section: "System" },
  { id: "artifact", path: "/artifacts/:artifactId", title: "Artifact", section: "Artifacts" },
] as const;

interface RouterValue {
  route: AppRoute;
  navigate: (to: string, options?: { replace?: boolean }) => void;
}

const RouterContext = createContext<RouterValue | null>(null);

function subscribe(listener: () => void) {
  window.addEventListener("popstate", listener);
  window.addEventListener("darkstar:navigate", listener);
  return () => {
    window.removeEventListener("popstate", listener);
    window.removeEventListener("darkstar:navigate", listener);
  };
}

function getPathname() { return window.location.pathname; }

function matchRoute(pathname: string): AppRoute {
  const normalized = pathname !== "/" && pathname.endsWith("/") ? pathname.slice(0, -1) : pathname;
  if (normalized === "/") return { ...routePatterns[0], params: {} };

  for (const candidate of routePatterns) {
    const keys: string[] = [];
    const pattern = candidate.path.split("/").map((segment) => {
      if (!segment.startsWith(":")) return segment;
      keys.push(segment.slice(1));
      return "([^/]+)";
    }).join("/");
    const match = normalized.match(new RegExp(`^${pattern}$`));
    if (!match) continue;
    const params = Object.fromEntries(keys.map((key, index) => [key, decodeURIComponent(match[index + 1])]));
    return { ...candidate, params };
  }

  return { id: "not-found", path: normalized, title: "Page not found", section: "DARKSTAR", params: {} };
}

export function RouterProvider({ children }: { children: ReactNode }) {
  const pathname = useSyncExternalStore(subscribe, getPathname, () => "/board");
  const route = useMemo(() => matchRoute(pathname), [pathname]);

  const navigate = (to: string, options?: { replace?: boolean }) => {
    const current = `${window.location.pathname}${window.location.search}`;
    if (current === to) return;
    window.history[options?.replace ? "replaceState" : "pushState"]({}, "", to);
    window.dispatchEvent(new Event("darkstar:navigate"));
    window.scrollTo({ top: 0, behavior: "instant" });
  };

  useEffect(() => { document.title = `${route.title} · DARKSTAR`; }, [route.title]);
  const value = useMemo(() => ({ route, navigate }), [route]);
  return <RouterContext.Provider value={value}>{children}</RouterContext.Provider>;
}

export function useRouter() {
  const router = useContext(RouterContext);
  if (!router) throw new Error("useRouter must be used inside RouterProvider");
  return router;
}

export function AppLink({ to, children, className, ariaCurrent, onNavigate }: {
  to: string;
  children: ReactNode;
  className?: string;
  ariaCurrent?: "page";
  onNavigate?: () => void;
}) {
  const { navigate } = useRouter();
  const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    navigate(to);
    onNavigate?.();
  };
  return <a href={to} className={className} aria-current={ariaCurrent} onClick={handleClick}>{children}</a>;
}

export function RouteView() {
  const { route } = useRouter();
  switch (route.id) {
    case "board": return <BoardPage />;
    case "checkpoints": return <PlaceholderPage eyebrow="Attention queue" title="Checkpoints" description="Review workflow decisions, provider permissions, external delivery, and requested input from one durable inbox." />;
    case "agents": return <PlaceholderPage eyebrow="Execution" title="Agents" description="Inspect queued and active attempts, their bounded authority, workspaces, and live logs." />;
    case "workflows": return <PlaceholderPage eyebrow="Configuration" title="Workflows" description="Inspect installed workflow versions, route profiles, validation findings, and execution boundaries." />;
    case "settings": return <PlaceholderPage eyebrow="System" title="Settings & Health" description="Understand effective configuration and the readiness of the daemon, repositories, providers, and delivery integrations." />;
    case "work": return <PlaceholderPage eyebrow="Work item" title={shortIdentifier(route.params.workId)} description="The work record keeps its runs, selected route, artifacts, and audit history together." />;
    case "run": return <PlaceholderPage eyebrow={`Run · ${shortIdentifier(route.params.runId)}`} title={shortIdentifier(route.params.workId)} description="Follow durable node visits and attempts without losing the history of retries or interruptions." />;
    case "artifact": return <PlaceholderPage eyebrow="Artifact" title={shortIdentifier(route.params.artifactId)} description="Inspect immutable revisions, representations, provenance, bindings, and freshness." />;
    default: return <PlaceholderPage eyebrow="404" title="Page not found" description="The requested dashboard location does not exist." action={{ label: "Return to board", to: "/board" }} />;
  }
}

function shortIdentifier(value = "") {
  return value.length <= 18 ? value : `${value.slice(0, 10)}…${value.slice(-5)}`;
}
