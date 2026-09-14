import { useRef } from "react";
import type { components } from "../api/schema.generated";
import type { RepositorySettingsDraft } from "../pages/projectRepositoriesModel";
import { AsyncPanel, EmptyState } from "./InteractionPatterns";
import { ModalDialog } from "./ui/Dialog";

type Schemas = components["schemas"];
export type ProjectRepositories = Schemas["ProjectRepositoriesView"];
export type ProjectRepository = Schemas["ProjectRepository"];
export type RepositoryRole = ProjectRepository["membership"]["role"];

export type RepositoryEditorDraft =
  | { kind: "create"; name: string }
  | { kind: "attach"; project: ProjectRepositories; path: string; label: string; role: RepositoryRole; settings: RepositorySettingsDraft }
  | { kind: "edit"; project: ProjectRepositories; member: ProjectRepository; label: string; role: RepositoryRole; settings: RepositorySettingsDraft }
  | { kind: "defaults"; project: ProjectRepositories; settings: RepositorySettingsDraft }
  | { kind: "detach"; project: ProjectRepositories; member: ProjectRepository };

export type RepositoryEditorState =
  | { state: "closed" }
  | { state: "editing" | "saving" | "reloading"; draft: RepositoryEditorDraft }
  | { state: "failed" | "stale"; draft: RepositoryEditorDraft; message: string };

export type ProjectRepositoryLoad =
  | { state: "loading" }
  | { state: "ready"; projects: ProjectRepositories[] }
  | { state: "unavailable"; message: string };

export interface ProjectRepositoriesViewProps {
  resource: ProjectRepositoryLoad;
  selectedProjectId?: string;
  editor: RepositoryEditorState;
  notice?: string;
  onSelect(projectId: string): void;
  onCreate(): void;
  onRegister(): void;
  onRetry(): void;
  onAttach(project: ProjectRepositories): void;
  onEdit(project: ProjectRepositories, member: ProjectRepository): void;
  onDetach(project: ProjectRepositories, member: ProjectRepository): void;
  onDefaults(project: ProjectRepositories): void;
  onDraft(draft: RepositoryEditorDraft): void;
  onSubmit(): void;
  onClose(): void;
  onReload(): void;
}

export function ProjectRepositoriesView(props: ProjectRepositoriesViewProps) {
  const { resource, selectedProjectId } = props;
  const selected = resource.state === "ready" ? resource.projects.find((entry) => entry.project.id === selectedProjectId) : undefined;
  return <section className="repository-management" aria-labelledby="project-repositories-title">
    <header className="settings-section-heading">
      <div><p className="eyebrow">Projects & code</p><h2 id="project-repositories-title">Projects and repositories</h2></div>
      <div className="action-bar">
        <button type="button" className="button" onClick={props.onRegister}>Register existing repository</button>
        <button type="button" className="button button--primary" onClick={props.onCreate}>Create project</button>
      </div>
    </header>
    <p className="settings-boundary">Start planning with a project, then connect the repositories it needs. Each project keeps its own repository settings.</p>
    {props.notice && <p role="status" className="repository-notice">{props.notice}</p>}
    {resource.state === "loading" && <AsyncPanel state="loading" title="Loading projects" message="Reading projects and their repository memberships." />}
    {resource.state === "unavailable" && <AsyncPanel state="error" title="Projects unavailable" message={resource.message} action={<button type="button" className="button" onClick={props.onRetry}>Retry projects</button>} />}
    {resource.state === "ready" && resource.projects.length === 0 && <EmptyState kind="empty" title="Create your first project" message="A project can hold plans and work before any code exists. You can attach a repository later." action={<button type="button" className="button" onClick={props.onCreate}>Create a project without code</button>} />}
    {resource.state === "ready" && resource.projects.length > 0 && <div className="repository-workspace">
      <nav className="repository-projects" aria-label="Projects">
        {resource.projects.map((entry) => <button type="button" key={entry.project.id} className="repository-project" aria-current={entry.project.id === selectedProjectId ? "true" : undefined} onClick={() => props.onSelect(entry.project.id)}>
          <strong>{entry.project.name}</strong><span>{entry.repositories.filter((value) => value.membership.status === "active").length} repositories{entry.project.status === "archived" ? " · Archived" : ""}</span>
        </button>)}
      </nav>
      {selected ? <RepositoryList project={selected} onAttach={props.onAttach} onEdit={props.onEdit} onDetach={props.onDetach} onDefaults={props.onDefaults} /> : <EmptyState kind="awaiting" title="Choose a project" message="Select a project to manage its repositories and defaults." />}
    </div>}
    <RepositoryEditor state={props.editor} onDraft={props.onDraft} onSubmit={props.onSubmit} onClose={props.onClose} onReload={props.onReload} />
  </section>;
}

export function RepositoryList({ project, onAttach, onEdit, onDetach, onDefaults }: Pick<ProjectRepositoriesViewProps, "onAttach" | "onEdit" | "onDetach" | "onDefaults"> & { project: ProjectRepositories }) {
  const active = project.repositories.filter((entry) => entry.membership.status === "active");
  const removed = project.repositories.filter((entry) => entry.membership.status === "removed");
  const editable = project.project.status === "active";
  return <div className="repository-detail">
    <header className="repository-detail-heading"><div><h3>{project.project.name}</h3><p>{active.length} active {active.length === 1 ? "repository" : "repositories"}</p></div><div className="action-bar">
      <button type="button" className="button" disabled={!editable} onClick={() => onDefaults(project)}>Repository defaults</button>
      <button type="button" className="button button--primary" disabled={!editable} onClick={() => onAttach(project)}>Attach repository</button>
    </div></header>
    {project.migration.state === "legacy_unresolved" && <AsyncPanel state="validation" title="Verify the original repository" message={<>{project.migration.reason} Attach the original local repository to verify its identity.</>} />}
    {!editable && <p className="repository-notice">This project is archived. Its repository history remains available.</p>}
    {active.length === 0 && <EmptyState kind="empty" title="No code repositories yet" message="You can create work, plan, and review documents now. Attach a repository when code context is needed." />}
    <div className="repository-cards">{active.map((member) => <article className="repository-card" key={member.repository.id}>
      <header><div><h4>{member.membership.label}</h4><p>{member.membership.role === "implementation" ? "Implementation" : "Read-only research"}</p></div><span className="repository-role" data-role={member.membership.role}>{member.membership.role === "implementation" ? "Code changes allowed in selected runs" : "Read-only"}</span></header>
      <p className="repository-location">{member.repository.root}</p>
      <p className="repository-inheritance">{member.membership.settings.baseRef ? `Base ref: ${member.membership.settings.baseRef}` : "Base ref inherits project defaults"}</p>
      <footer><button type="button" className="button" disabled={!editable} onClick={() => onEdit(project, member)} aria-label={`Edit ${member.membership.label}`}>Edit repository</button><button type="button" className="button" disabled={!editable} onClick={() => onDetach(project, member)} aria-label={`Detach ${member.membership.label}`}>Detach</button></footer>
      <details><summary>Repository details</summary><dl><dt>Repository ID</dt><dd>{member.repository.id}</dd><dt>Membership revision</dt><dd>{member.membership.revision}</dd><dt>Shared Git directory</dt><dd>{member.repository.commonGitDir}</dd></dl></details>
    </article>)}</div>
    {removed.length > 0 && <details className="repository-history"><summary>Detached repositories ({removed.length})</summary><p>Repository files and prior run history are preserved.</p>{removed.map((member) => <article key={member.repository.id}><div><strong>{member.membership.label}</strong><p>{member.repository.root}</p></div><button type="button" className="button" disabled={!editable} onClick={() => onEdit(project, member)} aria-label={`Reattach ${member.membership.label}`}>Reattach</button></article>)}</details>}
    <details className="repository-history"><summary>Project details</summary><dl><dt>Project ID</dt><dd>{project.project.id}</dd><dt>Source fingerprint</dt><dd>{project.project.sourceHash}</dd><dt>Resource version</dt><dd>{project.project.resourceVersion}</dd><dt>Created</dt><dd>{project.project.createdAt}</dd><dt>Last updated</dt><dd>{project.project.updatedAt}</dd></dl></details>
  </div>;
}

export function RepositoryEditor({ state, onDraft, onSubmit, onClose, onReload }: { state: RepositoryEditorState; onDraft(draft: RepositoryEditorDraft): void; onSubmit(): void; onClose(): void; onReload(): void }) {
  const initialFocusRef = useRef<HTMLElement | null>(null);
  if (state.state === "closed") {
    return null;
  }
  const { draft } = state;
  const busy = state.state === "saving" || state.state === "reloading";
  const title = draft.kind === "create" ? "Create project" : draft.kind === "attach" ? "Attach repository" : draft.kind === "detach" ? "Detach repository" : draft.kind === "defaults" ? "Repository defaults" : draft.member.membership.status === "removed" ? "Reattach repository" : "Edit repository";
  const submit = draft.kind === "create" ? "Create project" : draft.kind === "attach" ? "Attach repository" : draft.kind === "detach" ? "Detach repository" : draft.kind === "edit" && draft.member.membership.status === "removed" ? "Reattach repository" : "Save changes";
  return <ModalDialog open labelledBy="repository-editor-title" describedBy="repository-editor-description" className="repository-editor" busy={busy} initialFocusRef={initialFocusRef} onClose={onClose}>
    <form aria-busy={busy} onSubmit={(event) => { event.preventDefault(); onSubmit(); }}>
      <header className="work-dialog__header"><h2 id="repository-editor-title">{title}</h2><button type="button" className="icon-button" disabled={busy} aria-label="Close repository editor" onClick={onClose}>×</button></header>
      <p id="repository-editor-description" className="work-dialog__intro">{draft.kind === "create" ? "Create a planning project without code. Repositories can be attached whenever you need them." : draft.kind === "detach" ? `Detach ${draft.member.membership.label} from ${draft.project.project.name}? New runs cannot select it. Existing runs, repository files, and history are preserved.` : draft.kind === "defaults" ? `Set repository defaults for ${draft.project.project.name}. Individual repositories can override these values.` : "Choose how this repository contributes to the project. Roles determine eligibility; each run still requires an authorized repository selection."}</p>
      <fieldset disabled={busy || state.state === "stale"} className="repository-editor-fields">
        {draft.kind === "create" && <label className="field"><span>Project name</span><input aria-label="Project name" ref={(node) => { initialFocusRef.current = node; }} autoFocus required value={draft.name} onChange={(event) => onDraft({ ...draft, name: event.target.value })} /></label>}
        {draft.kind === "attach" && <label className="field"><span>Local repository path</span><input aria-label="Local repository path" aria-describedby="repository-path-help" ref={(node) => { initialFocusRef.current = node; }} autoFocus required placeholder="C:\Projects\my-repository" value={draft.path} onChange={(event) => onDraft({ ...draft, path: event.target.value })} /><small id="repository-path-help">Use an existing absolute Git repository path on this computer.</small></label>}
        {(draft.kind === "edit" || draft.kind === "attach") && <>
          <label className="field"><span>Repository label</span><input aria-label="Repository label" aria-describedby="repository-label-help" ref={draft.kind === "edit" ? (node) => { initialFocusRef.current = node; } : undefined} autoFocus={draft.kind === "edit"} required value={draft.label} onChange={(event) => onDraft({ ...draft, label: event.target.value })} /><small id="repository-label-help">A unique name within this project.</small></label>
          <label className="field"><span>Repository role</span><select aria-label="Repository role" aria-describedby="repository-role-help" value={draft.role} onChange={(event) => onDraft({ ...draft, role: event.target.value as RepositoryRole })}><option value="read_only">Read-only research</option><option value="implementation">Implementation</option></select><small id="repository-role-help">{draft.role === "read_only" ? "Available for investigation without code changes." : "Eligible as the single code-change target of an authorized run."}</small></label>
        </>}
        {"settings" in draft && <RepositorySettingsFields value={draft.settings} onChange={(settings) => onDraft({ ...draft, settings })} />}
      </fieldset>
      {(state.state === "failed" || state.state === "stale") && <AsyncPanel state={state.state === "stale" ? "stale" : "validation"} title={state.state === "stale" ? "Project changed while you were editing" : "Changes were not saved"} message={state.message} action={state.state === "stale" ? <button type="button" className="button" onClick={onReload}>Reload and discard this draft</button> : undefined} />}
      <footer className="work-dialog__footer"><button type="button" className="button" ref={draft.kind === "detach" || draft.kind === "defaults" ? (node) => { initialFocusRef.current = node; } : undefined} autoFocus={draft.kind === "detach"} disabled={busy} onClick={onClose}>Cancel</button><button type="submit" className={`button ${draft.kind === "detach" ? "button--danger" : "button--primary"}`} disabled={busy || state.state === "stale"}>{state.state === "reloading" ? "Loading latest…" : busy ? "Saving…" : submit}</button></footer>
    </form>
  </ModalDialog>;
}

export function RepositorySettingsFields({ value, onChange }: { value: RepositorySettingsDraft; onChange(value: RepositorySettingsDraft): void }) {
  return <details className="repository-settings-fields" open><summary>Repository configuration</summary><p className="repository-inheritance">Leave a field blank to inherit. Membership settings override project defaults for future runs.</p>
    <div className="repository-settings-grid">{([
      ["baseRef", "Base ref", "main"], ["worktreeBase", "Worktree directory", ".darkstar/worktrees"], ["configurationRoot", "Configuration root", "Absolute path for relative settings"], ["remote", "Delivery remote", "origin"], ["targetBranch", "Delivery target branch", "main"],
    ] as const).map(([key, label, placeholder]) => <label className="field" key={key}><span>{label}</span><input aria-label={label} value={value[key]} placeholder={placeholder} onChange={(event) => onChange({ ...value, [key]: event.target.value })} /></label>)}</div>
    <label className="field"><span>Path scope</span><select aria-label="Path scope" value={value.scopeMode} onChange={(event) => onChange({ ...value, scopeMode: event.target.value as "inherit" | "custom" })}><option value="inherit">Inherit path scope</option><option value="custom">Set path scope</option></select></label>
    {value.scopeMode === "custom" && <label className="field"><span>Allowed paths, one per line</span><textarea aria-label="Allowed paths, one per line" rows={3} value={value.scopeLines} onChange={(event) => onChange({ ...value, scopeLines: event.target.value })} /><small>An empty list explicitly selects no paths.</small></label>}
    <label className="field"><span>Validation profiles</span><select aria-label="Validation profiles" value={value.validationMode} onChange={(event) => onChange({ ...value, validationMode: event.target.value as "inherit" | "custom" })}><option value="inherit">Inherit validation profiles</option><option value="custom">Set validation profiles</option></select></label>
    {value.validationMode === "custom" && <label className="field"><span>Profiles and commands (JSON)</span><textarea aria-label="Profiles and commands (JSON)" rows={4} spellCheck={false} value={value.validationText} placeholder={'{"test":["npm test"]}'} onChange={(event) => onChange({ ...value, validationText: event.target.value })} /><small>Map profile names to command lists. An empty object preserves an explicit empty map.</small></label>}
  </details>;
}
