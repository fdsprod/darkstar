import { useEffect, useState } from "react";
import { useRouter } from "../app/router";
import { PageHeader } from "../components/PageStructure";
import { ContentDocumentEditor } from "./ContentDocumentEditor";
import { contentLibraryApi } from "./contentLibraryApi";
import { documentText, filterContent, type ContentDocument, type ContentItem } from "./contentLibraryModel";
import { PromptPreviewPanel } from "./WorkflowContentLinks";
import { ContentUsagePanel } from "./ContentUsagePanel";

export function TemplatesPage() {
  const { search, navigate } = useRouter();
  const selectedId = new URLSearchParams(search).get("id");
  const [items, setItems] = useState<ContentItem[]>([]);
  const [kind, setKind] = useState<ContentDocument["kind"]>("template");
  const [query, setQuery] = useState("");
  const [archived, setArchived] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");

  useEffect(() => {
    const abort = new AbortController();
    void contentLibraryApi.list(abort.signal).then((result) => {
      setItems(result.items ?? []);
      setLoading(false);
    }).catch((cause) => {
      if (!abort.signal.aborted) {
        setError(String(cause.message));
        setLoading(false);
      }
    });
    return () => {
      abort.abort();
    };
  }, []);

  function accept(item: ContentItem) {
    setItems((current) => [...current.filter((candidate) => candidate.id !== item.id), item]);
    setKind(item.kind);
    navigate(`/templates?id=${encodeURIComponent(item.id)}`);
  }

  async function create() {
    setError("");
    setCreating(true);
    try {
      accept(await contentLibraryApi.create(newName, "", kind === "template" ? { kind, content: "# Document\n\n## Overview\n", requiredHeadings: ["Overview"] } : { kind, instructions: "Perform one scoped task using the declared inputs.", sections: [] }));
      setNewName("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Could not create draft.");
    } finally {
      setCreating(false);
    }
  }

  const selected = items.find((item) => item.id === selectedId);
  return <div className="templates-page">
    <PageHeader eyebrow="Authoring library" title="Templates" description="Reusable document templates and conditional prompts. Workflows link to exact published versions." />
    {error && <p role="alert">{error}</p>}
    <div className="content-library-layout">
      <aside aria-label="Template and prompt library">
        <label className="field"><span>Library</span><select value={kind} onChange={(event) => {
          setKind(event.target.value as ContentDocument["kind"]);
        }}><option value="template">Artifact templates</option><option value="prompt">Prompt definitions</option></select></label>
        <label className="field"><span>Search</span><input type="search" value={query} onChange={(event) => {
          setQuery(event.target.value);
        }} /></label>
        <label><input type="checkbox" checked={archived} onChange={(event) => {
          setArchived(event.target.checked);
        }} /> Include archived</label>
        {loading ? <p role="status">Loading library…</p> : <ul className="content-library-list">{filterContent(items, query, kind, archived).map((item) => <li key={item.id}><button type="button" aria-pressed={selectedId === item.id} onClick={() => {
          navigate(`/templates?id=${encodeURIComponent(item.id)}`);
        }}><strong>{item.name}</strong><small>{item.archivedAt ? "Archived · " : ""}{item.versions.length} published versions</small></button></li>)}</ul>}
        {!loading && filterContent(items, query, kind, archived).length === 0 && <p>No matching {kind === "prompt" ? "prompts" : "templates"}.</p>}
        <form onSubmit={(event) => {
          event.preventDefault();
          void create();
        }}><label className="field"><span>New {kind} name</span><input required value={newName} onChange={(event) => {
          setNewName(event.target.value);
        }} /></label><button className="button" disabled={creating || !newName.trim()}>Create draft</button></form>
      </aside>
      {selected ? <LibraryItemEditor key={`${selected.id}:${selected.draft.revision}:${selected.versions.length}:${selected.archivedAt ?? ""}`} item={selected} onSaved={accept} /> : <section><h2>Select a template or prompt</h2><p>Edit a draft, publish a version, and explicitly link it from a workflow draft. Publishing does not update existing workflow links.</p></section>}
    </div>
  </div>;
}

function LibraryItemEditor({ item, onSaved }: { item: ContentItem; onSaved(item: ContentItem): void }) {
  const [name, setName] = useState(item.name);
  const [description, setDescription] = useState(item.description);
  const [document, setDocument] = useState(item.draft.document);
  const [version, setVersion] = useState("");
  const [view, setView] = useState("draft");
  const [compare, setCompare] = useState("");
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [linkedInputs, setLinkedInputs] = useState<string[]>([]);
  const [revision, setRevision] = useState(false);
  const published = item.versions.find((candidate) => candidate.reference.version === view);
  const visible = published?.document ?? document;
  const dirty = name !== item.name || description !== item.description || JSON.stringify(document) !== JSON.stringify(item.draft.document);
  const baseline = item.versions.find((candidate) => candidate.reference.version === compare);

  async function mutate(action: () => Promise<ContentItem>) {
    setPending(true);
    setError("");
    try {
      onSaved(await action());
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Library change failed.");
    } finally {
      setPending(false);
    }
  }

  return <section aria-label={`${item.name} editor`} className="content-library-editor">
    <h2>{item.name}{item.archivedAt ? " (archived)" : ""}</h2>
    <p>Draft revision {item.draft.revision}. Published versions are immutable.</p>
    {error && <p role="alert">{error}</p>}
    <fieldset disabled={pending}>
      <legend>Version and identity</legend>
      <label className="field"><span>Viewing version</span><select value={view} onChange={(event) => {
        setView(event.target.value);
      }}><option value="draft">Editable draft{dirty ? " · unsaved" : ""}</option>{item.versions.map((candidate) => <option key={candidate.reference.version} value={candidate.reference.version}>Published {candidate.reference.version}</option>)}</select></label>
      <label className="field"><span>Name</span><input value={name} disabled={Boolean(published) || Boolean(item.archivedAt)} onChange={(event) => {
        setName(event.target.value);
      }} /></label>
      <label className="field"><span>Description</span><textarea value={description} disabled={Boolean(published) || Boolean(item.archivedAt)} onChange={(event) => {
        setDescription(event.target.value);
      }} /></label>
      {published && <><p>Published {new Date(published.createdAt).toLocaleString()}</p><details><summary>Exact reference</summary><code>{published.reference.digest}</code></details><button type="button" disabled={Boolean(item.archivedAt)} onClick={() => {
        setDocument(structuredClone(published.document));
        setView("draft");
      }}>Use this version in draft</button></>}
    </fieldset>
    <ContentDocumentEditor document={visible} readOnly={pending || Boolean(published) || Boolean(item.archivedAt)} onChange={setDocument} />
    {visible.kind === "prompt" && <section><h3>Prompt builder preview</h3><p>Choose sample connections to inspect the assembled instructions.</p>{["open_items", "deferred_work"].map((input) => <label key={input}><input type="checkbox" checked={linkedInputs.includes(input)} onChange={(event) => {
      setLinkedInputs(event.target.checked ? [...linkedInputs, input] : linkedInputs.filter((candidate) => candidate !== input));
    }} /> {input.replaceAll("_", " ")} linked </label>)}<label><input type="checkbox" checked={revision} onChange={(event) => {
      setRevision(event.target.checked);
    }} /> Revising an artifact</label><PromptPreviewPanel document={visible} linkedInputs={linkedInputs} revision={revision} /></section>}
    <section><h3>Compare versions</h3><label className="field"><span>Compare current view with</span><select value={compare} onChange={(event) => {
      setCompare(event.target.value);
    }}><option value="">Choose a published version</option>{item.versions.map((candidate) => <option key={candidate.reference.version}>{candidate.reference.version}</option>)}</select></label>{baseline && <div className="content-version-comparison"><section><h4>Published {compare}</h4><pre>{documentText(baseline.document)}</pre></section><section><h4>{published ? `Published ${view}` : "Current draft"}</h4><pre>{documentText(visible)}</pre></section></div>}</section>
    <ContentUsagePanel contentId={item.id} />
    <div className="content-library-actions">
      <button className="button button--primary" type="button" disabled={pending || !dirty || Boolean(published) || Boolean(item.archivedAt)} onClick={() => {
        void mutate(() => contentLibraryApi.save(item, name, description, document));
      }}>Save draft</button>
      <label className="field"><span>New version</span><input placeholder="1.0.0" value={version} onChange={(event) => {
        setVersion(event.target.value);
      }} /></label>
      <button className="button" type="button" disabled={pending || dirty || !version.trim() || Boolean(published) || Boolean(item.archivedAt)} onClick={() => {
        void mutate(() => contentLibraryApi.publish(item, version));
      }}>Publish saved draft</button>
      <button className="button" type="button" disabled={pending || dirty} onClick={() => {
        void mutate(() => contentLibraryApi.duplicate(item, `${item.name} copy`));
      }}>Duplicate</button>
      <button className="button" type="button" disabled={pending || dirty} onClick={() => {
        void mutate(() => item.archivedAt ? contentLibraryApi.restore(item) : contentLibraryApi.archive(item));
      }}>{item.archivedAt ? "Restore to library" : "Archive"}</button>
    </div>
    {dirty && <p role="status">Unsaved changes. Save the draft before publishing or duplicating.</p>}
  </section>;
}
