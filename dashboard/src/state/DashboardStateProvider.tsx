import { createContext, useCallback, useContext, useEffect, useMemo, useReducer, useRef, type ReactNode } from "react";

import { apiClient, type DarkstarApiClient } from "../api/client";
import { getDashboardAuthorization } from "../api/bootstrap";
import { dashboardStateReducer, hydrateDashboard, initialDashboardState, type DashboardState } from "./dashboardState";
import { EventStreamHttpError, openEventStream, parseDomainEvent, parseServerSentEvents, reconnectDelay, replayRecovery } from "./events";

interface DashboardStateValue {
  state: DashboardState;
  refresh(): Promise<void>;
}

interface DashboardStateProviderProps {
  children: ReactNode;
  client?: DarkstarApiClient;
  fetcher?: typeof globalThis.fetch;
}

const DashboardStateContext = createContext<DashboardStateValue | undefined>(undefined);

export function DashboardStateProvider({ children, client = apiClient, fetcher }: DashboardStateProviderProps) {
  const [state, dispatch] = useReducer(dashboardStateReducer, initialDashboardState);
  const cursor = useRef(0);
  const controller = useRef<AbortController | undefined>(undefined);
  const refreshInFlight = useRef<Promise<void> | undefined>(undefined);
  const refreshAgain = useRef(false);

  const refresh = useCallback(async () => {
    if (refreshInFlight.current) {
      refreshAgain.current = true;
      return refreshInFlight.current;
    }
    const signal = controller.current?.signal;
    const task = (async () => {
      do {
        refreshAgain.current = false;
        dispatch({ type: "hydrate-started" });
        try {
          const snapshot = await hydrateDashboard(client, signal);
          if (!signal?.aborted) dispatch({ type: "hydrated", snapshot, synchronizedAt: new Date().toISOString() });
        } catch (error) {
          if (signal?.aborted) return;
          dispatch({ type: "hydrate-failed", message: safeHydrationMessage(error) });
          throw error;
        }
      } while (refreshAgain.current && !signal?.aborted);
    })();
    refreshInFlight.current = task;
    try {
      await task;
    } finally {
      if (refreshInFlight.current === task) refreshInFlight.current = undefined;
    }
  }, [client]);

  useEffect(() => {
    const abort = new AbortController();
    controller.current = abort;
    let reconnectAttempt = 0;

    const run = async () => {
      try { await refresh(); } catch { /* The stream can reconnect while projections recover. */ }
      while (!abort.signal.aborted) {
        dispatch({ type: "connection", status: reconnectAttempt === 0 ? "connecting" : "reconnecting" });
        try {
          const response = await openEventStream({
            fetcher,
            authorization: getDashboardAuthorization(),
            cursor: cursor.current,
            signal: abort.signal,
          });
          dispatch({ type: "connection", status: "live" });
          for await (const message of parseServerSentEvents(response.body!)) {
            const event = parseDomainEvent(message);
            if (event.globalPosition <= cursor.current) continue;
            if (event.globalPosition !== cursor.current + 1) throw new Error("The event stream is not contiguous.");
            cursor.current = event.globalPosition;
            reconnectAttempt = 0;
            dispatch({ type: "event", event });
            // Coalesced refresh: the event invalidates projections but never mutates one locally.
            void refresh().catch(() => undefined);
          }
          if (!abort.signal.aborted) throw new EventStreamHttpError(200, "EVENT_STREAM_ENDED", "The event stream ended.", true, []);
        } catch (error) {
          if (abort.signal.aborted) break;
          const recovery = replayRecovery(error);
          if (recovery) {
            let synchronized = false;
            try { await refresh(); synchronized = true; } catch { /* Never rebase a stale snapshot. */ }
            if (synchronized) {
              cursor.current = recovery.cursor;
              dispatch({ type: "reset-cursor", cursor: recovery.cursor });
            }
          } else if (isNewerCursor(error)) {
            let synchronized = false;
            try { await refresh(); synchronized = true; } catch { /* Never attach a stale snapshot to a replacement log. */ }
            if (synchronized) {
              cursor.current = 0;
              dispatch({ type: "reset-cursor", cursor: 0 });
            }
          }
          dispatch({ type: "connection", status: "reconnecting", message: safeConnectionMessage(error) });
          await abortableDelay(reconnectDelay(reconnectAttempt++), abort.signal);
        }
      }
      dispatch({ type: "connection", status: "offline" });
    };

    void run();
    return () => {
      abort.abort();
      controller.current = undefined;
      refreshInFlight.current = undefined;
      refreshAgain.current = false;
    };
  }, [fetcher, refresh]);

  const value = useMemo(() => ({ state, refresh }), [refresh, state]);
  return <DashboardStateContext.Provider value={value}>{children}</DashboardStateContext.Provider>;
}

export function useDashboardState() {
  const value = useContext(DashboardStateContext);
  if (!value) throw new Error("useDashboardState must be used inside DashboardStateProvider");
  return value;
}

function isNewerCursor(error: unknown) {
  return error instanceof EventStreamHttpError && error.status === 400 && error.code === "EVENT_CURSOR_INVALID";
}

function safeHydrationMessage(error: unknown) {
  if (error instanceof Error && error.name === "AbortError") return "Synchronization stopped.";
  return "Authoritative dashboard data is temporarily unavailable.";
}

function safeConnectionMessage(error: unknown) {
  if (error instanceof EventStreamHttpError && error.retryable) return "Live updates interrupted; reconnecting.";
  return "Live updates unavailable; retrying safely.";
}

function abortableDelay(milliseconds: number, signal: AbortSignal) {
  return new Promise<void>((resolve) => {
    if (signal.aborted) return resolve();
    const timer = window.setTimeout(resolve, milliseconds);
    signal.addEventListener("abort", () => {
      window.clearTimeout(timer);
      resolve();
    }, { once: true });
  });
}
