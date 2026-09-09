import { useState } from "react";
import { apiClient, ApiRequestError } from "../api/client";
import type { components } from "../api/schema.generated";

type Work = components["schemas"]["WorkItem"];
export function DeleteWorkButton({ work, onChanged }: { work: Work; onChanged(): Promise<void> }) {
 const [busy, setBusy] = useState(false);
 const [error, setError] = useState("");
 async function remove() {
  if (!window.confirm(`Delete “${work.title}”? Active runs will be stopped and this item removed from the board. History and artifacts are retained. Files, branches, and pull requests are kept.`)) return;
  setBusy(true); setError("");
  try { await apiClient.deleteWorkItem(work.id, work.resourceVersion, `delete-work-${crypto.randomUUID()}`); await onChanged(); }
  catch (cause) { setError(cause instanceof ApiRequestError && cause.status === 412 ? "This item changed. Review its refreshed state and try again." : "Deletion could not be requested. Try again."); await onChanged().catch(() => undefined); }
  finally { setBusy(false); }
 }
 if (work.deletion === "deleted") return <span className="work-deletion-status">Deleted · history retained</span>;
 return <div className="work-delete"><button className="card-action" type="button" disabled={busy} onClick={() => void remove()}>{busy ? "Deleting…" : work.deletion === "deleting" ? "Retry deletion" : "Delete work item"}</button>{work.deletion === "deleting" && <p role="status">Deleting… Waiting for execution to stop. If this persists, check the run’s cancellation status.</p>}{error && <p role="alert">{error}</p>}</div>;
}
