import { useState } from "react";
import { AppLink } from "../app/router";
import { contentLibraryApi } from "./contentLibraryApi";
import { contentReference } from "./contentLibraryModel";
import type { JsonObject } from "./workflowEditorModel";
import { ContentVersionPicker, useContentLibrary } from "./WorkflowContentLinks";

export function TemplateResourceLink({ resource, name, readOnly, onChange }: { resource: JsonObject; name: string; readOnly: boolean; onChange(resource: JsonObject): void }) {
  const library = useContentLibrary();
  const [extraction, setExtraction] = useState<{ kind: "idle" } | { kind: "saving" } | { kind: "created"; id: string } | { kind: "error"; message: string }>({ kind: "idle" });

  async function extract() {
    setExtraction({ kind: "saving" });
    try {
      const item = await contentLibraryApi.create(name, "Extracted from an embedded workflow template.", { kind: "template", content: String(resource.content ?? ""), requiredHeadings: Array.isArray(resource.requiredHeadings) ? resource.requiredHeadings as string[] : [] });
      setExtraction({ kind: "created", id: item.id });
    } catch (cause) {
      setExtraction({ kind: "error", message: cause instanceof Error ? cause.message : "Template extraction failed." });
    }
  }

  return <section><h3>Library template</h3>
    {library.kind === "loading" && <p>Loading library…</p>}
    {library.kind === "error" && <p role="alert">Library unavailable: {library.message}</p>}
    {library.kind === "ready" && <ContentVersionPicker label="Published template version" kind="template" value={contentReference(resource.reference)} items={library.items} disabled={readOnly} allowClear={false} onChange={(reference) => {
      if (reference) {
        onChange({ kind: "template_reference", reference });
      }
    }} />}
    {resource.kind === "template" && <><p>This template is embedded in the workflow. Extract a library draft to edit and version it independently.</p><button type="button" disabled={readOnly || extraction.kind === "saving" || extraction.kind === "created"} onClick={() => {
      void extract();
    }}>Extract to template library</button></>}
    {extraction.kind === "created" && <p role="status">Library draft created. <AppLink to={`/templates?id=${encodeURIComponent(extraction.id)}`}>Review and publish the template</AppLink>, then choose its published version here and apply the link.</p>}
    {extraction.kind === "error" && <p role="alert">{extraction.message}</p>}
  </section>;
}
