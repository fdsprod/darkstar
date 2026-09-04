import {
  createContext,
  type MouseEvent,
  type ReactNode,
  type Ref,
  useContext,
  useEffect,
  useMemo,
  useSyncExternalStore,
} from "react";

import { BoardPage } from "../pages/BoardPage";
import { ArtifactPage } from "../pages/ArtifactPage";
import { ArtifactsPage } from "../pages/ArtifactsPage";
import { AgentsPage } from "../pages/AgentsPage";
import { CheckpointsPage } from "../pages/CheckpointsPage";
import { PlaceholderPage } from "../pages/PlaceholderPage";
import { RunDetailPage } from "../pages/RunDetailPage";
import { RunReadinessPage } from "../pages/RunReadinessPage";
import { SettingsPage } from "../pages/SettingsPage";
import { WorkDetailPage } from "../pages/WorkDetailPage";
import { WorkflowsPage } from "../pages/WorkflowsPage";

export type AppRouteId = "board" | "work" | "run" | "readiness" | "checkpoints" | "agents" | "workflows" | "settings" | "artifacts" | "artifact" | "not-found";

export interface AppRoute {
  id: AppRouteId;
  path: string;
  title: string;
  section: string;
  params: Readonly<Record<string, string>>;
}

const routePatterns = [
  { id: "board", path: "/board", title: "Board", section: "Work" },
  { id: "readiness", path: "/work/:workId/run/:runId/readiness", title: "Readiness", section: "Run" },
  { id: "run", path: "/work/:workId/run/:runId", title: "Run", section: "Work" },
  { id: "work", path: "/work/:workId", title: "Work item", section: "Work" },
  { id: "checkpoints", path: "/checkpoints", title: "Checkpoints", section: "Operations" },
  { id: "agents", path: "/agents", title: "Agents", section: "Operations" },
  { id: "workflows", path: "/workflows", title: "Workflows", section: "Library" },
  { id: "settings", path: "/settings", title: "Settings & Health", section: "System" },
  { id: "artifacts", path: "/artifacts", title: "Artifacts", section: "Library" },
  { id: "artifact", path: "/artifacts/:artifactId", title: "Artifact", section: "Artifacts" },
] as const;

interface RouterValue {
  route: AppRoute;
  search: string;
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

function getLocation() { return `${window.location.pathname}${window.location.search}`; }

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
  const location = useSyncExternalStore(subscribe, getLocation, () => "/board");
  const [pathname, query = ""] = location.split("?", 2);
  const route = useMemo(() => matchRoute(pathname), [pathname]);
  const search = query ? `?${query}` : "";

  const navigate = (to: string, options?: { replace?: boolean }) => {
    const current = `${window.location.pathname}${window.location.search}`;
    if (current === to) return;
    window.history[options?.replace ? "replaceState" : "pushState"]({}, "", to);
    window.dispatchEvent(new Event("darkstar:navigate"));
    window.scrollTo({ top: 0, behavior: "instant" });
  };

  useEffect(() => { document.title = `${route.title} · DARKSTAR`; }, [route.title]);
  const value = useMemo(() => ({ route, search, navigate }), [route, search]);
  return <RouterContext.Provider value={value}>{children}</RouterContext.Provider>;
}

export function useRouter() {
  const router = useContext(RouterContext);
  if (!router) throw new Error("useRouter must be used inside RouterProvider");
  return router;
}

export function AppLink({ to, children, className, ariaCurrent, onNavigate, anchorRef }: {
  to: string;
  children: ReactNode;
  className?: string;
  ariaCurrent?: "page";
  onNavigate?: () => void;
  anchorRef?: Ref<HTMLAnchorElement>;
}) {
  const { navigate } = useRouter();
  const handleClick = (event: MouseEvent<HTMLAnchorElement>) => {
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    event.preventDefault();
    navigate(to);
    onNavigate?.();
  };
  return <a ref={anchorRef} href={to} className={className} aria-current={ariaCurrent} onClick={handleClick}>{children}</a>;
}

export function RouteView() {
  const { route } = useRouter();
  switch (route.id) {
    case "board": return <BoardPage />;
    case "checkpoints": return <CheckpointsPage />;
    case "agents": return <AgentsPage />;
    case "workflows": return <WorkflowsPage />;
    case "artifacts": return <ArtifactsPage />;
    case "settings": return <SettingsPage />;
    case "work": return <WorkDetailPage />;
    case "run": return <RunDetailPage />;
    case "readiness": return <RunReadinessPage />;
    case "artifact": return <ArtifactPage />;
    default: return <PlaceholderPage eyebrow="404" title="Page not found" description="The requested dashboard location does not exist." action={{ label: "Return to board", to: "/board" }} />;
  }
}

function shortIdentifier(value = "") {
  return value.length <= 18 ? value : `${value.slice(0, 10)}…${value.slice(-5)}`;
}
