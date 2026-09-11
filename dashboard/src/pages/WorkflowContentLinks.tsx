import { useEffect, useState } from "react";
import { AppLink } from "../app/router";
import { Markdown } from "../components/Markdown";
import { contentLibraryApi } from "./contentLibraryApi";
import { contentReference, sameReference, type ContentDocument, type ContentItem, type ContentReference, type PromptPreview } from "./contentLibraryModel";
import type { JsonObject } from "./workflowEditorModel";
import { record } from "./workflowResourceModel";
import { contextSources, linkOutputTemplate, setNodePrompt, setOptionalContext } from "./workflowContentModel";

export function useContentLibrary() {
  const [state, setState] = useState<{ kind: "loading" } | { kind: "ready"; items: ContentItem[] } | { kind: "error"; message: string }>({ kind: "loading" });
  useEffect(() => {
    const abort = new AbortController();
    void contentLibraryApi.list(abort.signal).then((result) => {
      setState({ kind: "ready", items: result.items ?? [] });
    }).catch((cause) => {
      if (!abort.signal.aborted) {
        setState({ kind: "error", message: cause.message });
      }
    });
    return () => {
      abort.abort();
    };
  }, []);
  return state;
}

export function ContentVersionPicker({ label, kind, value, items, disabled = false, allowClear = true, onChange }: {
  label: string;
  kind: ContentDocument["kind"];
  value?: ContentReference;
  items: ContentItem[];
  disabled?: boolean;
  allowClear?: boolean;
  onChange(value?: ContentReference): void;
}) {
  const options = items.filter((item) => item.kind === kind).flatMap((item) => item.versions.map((version) => ({ item, version })));
  const selected = options.find((option) => sameReference(value, option.version.reference));
  const currentItem = items.find((item) => item.id === value?.id);
  const key = (reference: ContentReference) => JSON.stringify(reference);
  const newer = currentItem?.versions.some((version) => version.reference.version !== value?.version && new Date(version.createdAt) > new Date(selected?.version.createdAt ?? 0));
  return <div className="content-version-picker">
    <label className="field"><span>{label}</span><select value={value ? key(value) : ""} disabled={disabled} onChange={(event) => {
      onChange(event.target.value ? JSON.parse(event.target.value) as ContentReference : undefined);
    }}><option value="" disabled={!allowClear}>{allowClear ? `No linked ${kind}` : `Choose a published ${kind}`}</option>{value && !selected && <option value={key(value)}>Unavailable: {value.id} · {value.version}</option>}{options.filter((option) => !option.item.archivedAt || sameReference(value, option.version.reference)).map(({ item, version }) => <option key={key(version.reference)} value={key(version.reference)}>{item.name} · {version.reference.version}{item.archivedAt ? " (archived)" : ""}</option>)}</select></label>
    {value && <AppLink to={`/templates?id=${encodeURIComponent(value.id)}`}>View {kind} and versions</AppLink>}
    {newer && <p>Update available. Choose a newer version to adopt it in this draft.</p>}
    {value && !selected && <p role="alert">This exact published version could not be resolved. Its link is preserved.</p>}
  </div>;
}

export function PromptPreviewPanel({ document, linkedInputs, revision = false }: { document: ContentDocument; linkedInputs: string[]; revision?: boolean }) {
  const [state, setState] = useState<{ kind: "loading" } | { kind: "ready"; preview: PromptPreview } | { kind: "error"; message: string }>({ kind: "loading" });
  const key = JSON.stringify({ document, linkedInputs, revision });
  useEffect(() => {
    const abort = new AbortController();
    setState({ kind: "loading" });
    const timer = window.setTimeout(() => {
      void contentLibraryApi.preview(document, linkedInputs, revision, abort.signal).then((preview) => {
        setState({ kind: "ready", preview });
      }).catch((cause) => {
        if (!abort.signal.aborted) {
          setState({ kind: "error", message: cause.message });
        }
      });
    }, 250);
    return () => {
      window.clearTimeout(timer);
      abort.abort();
    };
  }, [key]);
  if (state.kind === "loading") {
    return <p role="status">Building preview…</p>;
  }
  if (state.kind === "error") {
    return <p role="alert">Prompt preview unavailable: {state.message}</p>;
  }
  return <div className="prompt-preview"><p>Estimated instruction tokens: {state.preview.estimatedTokens.toLocaleString()}. Excludes input content, output contracts, skills, and provider overhead.</p><ul>{state.preview.sections.map((section) => <li key={section.id}><strong>{section.id}: {section.included ? "included" : "omitted"}</strong> — {section.reason}</li>)}</ul><Markdown text={state.preview.instructions} /></div>;
}

export function WorkflowContentLinks({ document, nodeId, onChange }: {
  document: JsonObject;
  nodeId: string;
  onChange(document: JsonObject, message: string): void;
}) {
  const library = useContentLibrary();
  const node = record(record(record(document.spec).nodes)[nodeId]);
  const prompt = contentReference(node.prompt);
  const bindings = record(node.inputs);
  const sources = contextSources(document, nodeId);
  const linkedInputs = Object.keys(bindings);
  const [revision, setRevision] = useState(false);
  const version = library.kind === "ready" ? library.items.find((item) => item.id === prompt?.id)?.versions.find((candidate) => prompt && sameReference(prompt, candidate.reference)) : undefined;
  const invalidLinks = Object.entries(bindings).filter(([, raw]) => {
    const from = String(record(raw).from);
    if (from.startsWith("run.input.")) {
      return !Object.hasOwn(record(record(document.spec).inputs), from.slice(10));
    }
    const parts = from.split(".");
    return parts.length !== 4 || !Object.hasOwn(record(record(record(record(document.spec).nodes)[parts[1]]).outputs), parts[3]);
  }).map(([name]) => name);
  return <section className="inspector-section"><h3>Linked prompts and templates</h3>
    {library.kind === "loading" && <p>Loading published library versions…</p>}
    {library.kind === "error" && <p role="alert">Library unavailable: {library.message}. Existing links are preserved.</p>}
    {library.kind === "ready" && <>
      {["reasoning", "implementation"].includes(String(node.type)) && <ContentVersionPicker label="Prompt definition" kind="prompt" value={prompt} items={library.items} onChange={(reference) => {
        onChange(setNodePrompt(document, nodeId, reference), "Updated linked prompt version.");
      }} />}
      {Object.entries(record(node.outputs)).filter(([, output]) => record(output).type === "markdown").map(([id, raw]) => {
        const templateInput = String(record(record(raw).artifact).templateInput ?? "");
        const from = String(record(bindings[templateInput]).from ?? "");
        const resource = record(record(record(record(document.spec).inputs)[from.slice(10)]).resource);
        return <div key={id}><ContentVersionPicker label={`Template for ${id}`} kind="template" value={contentReference(resource.reference)} items={library.items} onChange={(reference) => {
          onChange(linkOutputTemplate(document, nodeId, id, reference), `Updated template for ${id}.`);
        }} />{resource.kind === "template" && <p>This output currently uses the embedded template input “{templateInput}”. Selecting a library version replaces its output link.</p>}</div>;
      })}
    </>}
    <h4>Optional work context</h4>
    {(["open_items", "deferred_work"] as const).map((input) => {
      const from = String(record(bindings[input]).from ?? "");
      return <label className="field" key={input}><span>{input === "open_items" ? "Open items" : "Deferred work"}</span><select value={from} onChange={(event) => {
        onChange(setOptionalContext(document, nodeId, input, event.target.value), `Updated ${input.replaceAll("_", " ")} link.`);
      }}><option value="">Not linked</option>{from && !sources.some((source) => source.from === from) && <option value={from}>Configured source: {from}</option>}{sources.map((source) => <option key={source.from} value={source.from}>{source.label}</option>)}</select></label>;
    })}
    <p className="inspector-help">An unlinked input does not mean there are no open items. Deferred work remains outside implementation scope unless explicitly selected.</p>
    {invalidLinks.length > 0 && <p role="alert">Linked inputs have missing sources: {invalidLinks.join(", ")}. Repair these links before running.</p>}
    {version?.document.kind === "prompt" && <details><summary>Assembled prompt preview</summary><p>The linked prompt provides reusable process instructions. Additional node instructions remain active for this task.</p><label><input type="checkbox" checked={revision} onChange={(event) => {
      setRevision(event.target.checked);
    }} /> Preview artifact revision instructions</label><p>Connections determine these sections. Actual linked content is checked during run preparation.</p><PromptPreviewPanel document={version.document} linkedInputs={linkedInputs} revision={revision} /></details>}
  </section>;
}
