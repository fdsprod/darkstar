import { useEffect, useState, type FormEvent } from "react";

import { apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { AsyncPanel } from "../components/InteractionPatterns";

type Schemas = components["schemas"];
type Destination = Schemas["TrackerDestinations"]["destinations"][number];

export type SourceSelection = { kind: "built_in" } | {
  kind: "external";
  connectionId: string;
  connectionRevision: string;
  destination: Destination;
};

export function TrackerSourcePicker({ pending, onSelect }: { pending: boolean; onSelect(source: SourceSelection): Promise<void> }) {
  const [connections, setConnections] = useState<Schemas["TrackerConnection"][]>([]);
  const [connectionKey, setConnectionKey] = useState("");
  const [destinations, setDestinations] = useState<Destination[]>([]);
  const [destinationKey, setDestinationKey] = useState("");
  const [cursor, setCursor] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const connection = connections.find((item) => `${item.connectionId}/${item.revision}` === connectionKey);
  const destination = destinations.find((item) => `${item.tenantId}/${item.scopeId}/${item.containerId}` === destinationKey);

  useEffect(() => {
    const abort = new AbortController();
    void apiClient.operation("listTrackerConnections", { signal: abort.signal }).then((value) => {
      if (!abort.signal.aborted) {
        setConnections(value.connections);
      }
    }).catch(() => {
      if (!abort.signal.aborted) {
        setError("Connections could not be loaded. Close and reopen source settings to retry.");
      }
    });
    return () => abort.abort();
  }, []);

  async function discover(nextCursor = "") {
    if (!connection || loading) {
      return;
    }
    setLoading(true);
    setError("");
    try {
      const value = await apiClient.operation("getTrackerDestinations", {
        path: { connectionId: connection.connectionId, revision: connection.revision },
        query: { cursor: nextCursor, pageSize: 50 },
      });
      setDestinations((previous) => nextCursor ? [...previous, ...value.destinations.filter((item) => !previous.some((old) => old.tenantId === item.tenantId && old.scopeId === item.scopeId && old.containerId === item.containerId))] : value.destinations);
      setCursor(value.nextCursor);
    } catch {
      setError("Source scopes could not be discovered. Check this connection's credentials and account access.");
    } finally {
      setLoading(false);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    if (pending || loading) {
      return;
    }
    if (connectionKey === "built_in") {
      void onSelect({ kind: "built_in" });
    } else if (connection && destination) {
      void onSelect({ kind: "external", connectionId: connection.connectionId, connectionRevision: connection.revision, destination });
    }
  }

  return <form className="ticket-source-settings" onSubmit={submit}>
    <p>Select the business tracker for this project's future intake. Existing work and retained ticket history keep their original source.</p>
    <fieldset disabled={pending || loading}>
      <label>Source connection
        <select value={connectionKey} onChange={(event) => {
          setConnectionKey(event.target.value);
          setDestinations([]);
          setDestinationKey("");
          setCursor("");
          setError("");
        }}>
          <option value="">Choose a source</option>
          <option value="built_in">Built-in tickets</option>
          {connections.map((item) => <option key={`${item.connectionId}/${item.revision}`} value={`${item.connectionId}/${item.revision}`}>{item.kind === "linear" ? "Linear" : "GitHub Issues"} · {item.accountName} · {item.connectionId} ({item.revision})</option>)}
        </select>
      </label>
      {connection && <>
        <button className="button" type="button" onClick={() => void discover()}>Discover source scopes</button>
        <label>Source scope
          <select value={destinationKey} onChange={(event) => setDestinationKey(event.target.value)}>
            <option value="">Choose a team or repository</option>
            {destinations.map((item) => <option key={`${item.tenantId}/${item.scopeId}/${item.containerId}`} value={`${item.tenantId}/${item.scopeId}/${item.containerId}`}>{item.name}</option>)}
          </select>
        </label>
        {cursor && <button className="button" type="button" onClick={() => void discover(cursor)}>More source scopes</button>}
      </>}
      <button className="button button--primary" type="submit" disabled={connectionKey !== "built_in" && !destination}>Use selected source</button>
    </fieldset>
    {loading && <p role="status">Checking accessible source scopes…</p>}
    {error && <AsyncPanel compact state="error" title="Source settings unavailable" message={error} />}
    <p>Register Linear or GitHub connections with the tracker connection commands. Credentials remain in protected local storage.</p>
  </form>;
}
