import { useEffect, useState } from "react";
import { contentLibraryApi } from "./contentLibraryApi";
import { AppLink } from "../app/router";
import type { ContentReference } from "./contentLibraryModel";

interface ContentUsage {
  kind: "draft" | "published";
  workflowName: string;
  workflowVersion: string;
  draftId?: string;
  nodeId?: string;
  inputId?: string;
  reference: ContentReference;
}

export function ContentUsagePanel({ contentId }: { contentId: string }) {
  const [state, setState] = useState<{ kind: "loading" } | { kind: "ready"; usages: ContentUsage[] } | { kind: "error"; message: string }>({ kind: "loading" });
  useEffect(() => {
    const abort = new AbortController();
    void contentLibraryApi.usages(contentId, abort.signal).then((body) => {
      setState({ kind: "ready", usages: body.usages ?? [] });
    }).catch((cause) => {
      if (!abort.signal.aborted) {
        setState({ kind: "error", message: cause.message });
      }
    });
    return () => {
      abort.abort();
    };
  }, [contentId]);
  return <section><h3>Used by</h3>
    {state.kind === "loading" && <p>Loading workflow references…</p>}
    {state.kind === "error" && <p role="alert">{state.message}</p>}
    {state.kind === "ready" && (state.usages.length === 0 ? <p>No workflow references.</p> : <ul>{state.usages.map((usage, index) => <li key={index}><AppLink to={usage.draftId ? `/workflows?item=${encodeURIComponent(`draft:${usage.draftId}`)}` : "/workflows"}>{usage.workflowName} · {usage.kind === "draft" ? "draft" : usage.workflowVersion}</AppLink> — {usage.nodeId ?? usage.inputId ?? "workflow"} · linked version {usage.reference.version}</li>)}</ul>)}
  </section>;
}
