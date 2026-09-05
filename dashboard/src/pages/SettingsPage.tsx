import { useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent, type RefObject } from "react";

import { tabKeyTarget } from "../accessibility/keyboard";
import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { useRouter } from "../app/router";
import { AsyncPanel } from "../components/InteractionPatterns";
import { PageHeader } from "../components/PageStructure";
import { useDashboardState } from "../state/DashboardStateProvider";
import { DetailFailure, DetailLoading, formatDate } from "./WorkDetailPage";
import {
  CONFIGURATION_GROUPS, configurationScope, configurationScopesEqual, decodeDoctorReport, draftForSetting, effectiveSetting,
  groupConfigurationSettings, mutationFromDraft, normalizeProjectRegistration, parseSettingDeepLink,
  parseConfigurationScope, parseSettingsTab, scopeProjectId, settingAnchor, settingMatchesSearch, sortProjects, typedValueText,
  type ConfigurationCatalog, type ConfigurationDescriptor, type ConfigurationDraft,
  type ConfigurationEditorState, type ConfigurationNotice, type ConfigurationPreview,
  type ConfigurationScope, type ConfigurationState, type DoctorReport, type HealthCheck,
  type Loadable, type ProviderDetails, type SettingsTab,
} from "./settingsModel";

type Schemas = components["schemas"];
type Project = Schemas["Project"];
type Health = Schemas["Health"];

const tabs: Array<{ id: SettingsTab; label: string }> = [
  { id: "health", label: "System health" }, { id: "provider", label: "Provider" },
  { id: "projects", label: "Projects" }, { id: "configuration", label: "Configuration" },
];
const subsystemLabels: Record<HealthCheck["subsystem"], string> = {
  database: "Database", daemon: "Daemon", paths: "Application paths", git: "Git repository",
  codex: "Codex installation", github: "GitHub delivery", configuration: "Configuration", provider: "Selected provider",
};

export function SettingsPage() {
  const { search, navigate } = useRouter();
  const { state: dashboard } = useDashboardState();
  const params = useMemo(() => new URLSearchParams(search), [search]);
  const tab = parseSettingsTab(params.get("tab"));
  const linkedSetting = parseSettingDeepLink(params.get("setting"));
  const scope = parseConfigurationScope(params.get("scope"), params.get("projectId"));
  const [health, setHealth] = useState<Loadable<Health>>({ state: "loading" });
  const [doctor, setDoctor] = useState<Loadable<DoctorReport>>({ state: "loading" });
  const [projects, setProjects] = useState<Loadable<Project[]>>({ state: "loading" });
  const [catalog, setCatalog] = useState<Loadable<ConfigurationCatalog>>({ state: "loading" });
  const [configuration, setConfiguration] = useState<Loadable<ConfigurationState>>({ state: "loading" });
  const [editor, setEditor] = useState<ConfigurationEditorState>({ state: "closed" });
  const [notice, setNotice] = useState<ConfigurationNotice>({ state: "none" });
  const [secretOpen, setSecretOpen] = useState(false);
  const [searchDraft, setSearchDraft] = useState(params.get("q") ?? "");
  const [refreshVersion, setRefreshVersion] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const registerDialog = useRef<HTMLDialogElement>(null);
  const openedDeepLink = useRef("");
  const pendingSettingValue = useRef<{ key: string; value: string } | undefined>(undefined);
  const hasDraft = editor.state !== "closed" || secretOpen;

  useEffect(() => setSearchDraft(params.get("q") ?? ""), [search]);
  useEffect(() => {
    if (!hasDraft) return;
    const protect = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    const protectLink = (event: MouseEvent) => {
      const target = event.target instanceof Element ? event.target.closest("a[href]") : null;
      if (target && !window.confirm("Discard unsaved Settings changes?")) { event.preventDefault(); event.stopImmediatePropagation(); }
    };
    window.addEventListener("beforeunload", protect);
    document.addEventListener("click", protectLink, true);
    return () => { window.removeEventListener("beforeunload", protect); document.removeEventListener("click", protectLink, true); };
  }, [hasDraft]);
  useEffect(() => {
    const abort = new AbortController();
    setRefreshing(true);
    const requests = [
      apiClient.getHealth(abort.signal).then((value) => setIfActive(abort, setHealth, { state: "ready", value })).catch((cause) => setIfActive(abort, setHealth, loadFailure(cause, "Daemon liveness"))),
      apiClient.getDoctorReport(undefined, abort.signal).then((value) => setIfActive(abort, setDoctor, { state: "ready", value: decodeDoctorReport(value) })).catch((cause) => setIfActive(abort, setDoctor, loadFailure(cause, "Subsystem diagnostics"))),
      apiClient.listProjects(abort.signal).then((value) => setIfActive(abort, setProjects, { state: "ready", value: sortProjects(value) })).catch((cause) => setIfActive(abort, setProjects, loadFailure(cause, "Project registry"))),
      apiClient.getConfigurationCatalog(abort.signal).then((value) => setIfActive(abort, setCatalog, { state: "ready", value })).catch((cause) => setIfActive(abort, setCatalog, loadFailure(cause, "Configuration catalog"))),
      apiClient.getConfigurationState(scopeProjectId(scope), abort.signal).then((value) => setIfActive(abort, setConfiguration, { state: "ready", value })).catch((cause) => setIfActive(abort, setConfiguration, loadFailure(cause, "Configuration state"))),
    ];
    void Promise.allSettled(requests).then(() => { if (!abort.signal.aborted) setRefreshing(false); });
    return () => abort.abort();
  }, [scope.type, scopeProjectId(scope), refreshVersion]);
  useEffect(() => {
    const linkIdentity = `${scope.type}:${scopeProjectId(scope) ?? ""}:${linkedSetting}`;
    if (!linkedSetting || openedDeepLink.current === linkIdentity || editor.state !== "closed" || catalog.state !== "ready" || configuration.state !== "ready" || !configurationScopesEqual(configuration.value.scope, scope)) return;
    const descriptor = catalog.value.settings.find((candidate) => candidate.key === linkedSetting);
    openedDeepLink.current = linkIdentity;
    if (!descriptor) { setNotice({ state: "failed", message: `Setting ${linkedSetting} is not published by the current configuration catalog.` }); return; }
    if (!descriptor.allowedScopes.includes(scope.type) || !descriptor.actions.includes("preview") || !descriptor.actions.includes("apply")) { setNotice({ state: "failed", message: `${descriptor.title} is read-only or unsupported at ${scope.type} scope.` }); requestAnimationFrame(() => document.getElementById(settingAnchor(descriptor.key))?.scrollIntoView({ block: "center" })); return; }
    const pending = pendingSettingValue.current?.key === descriptor.key ? pendingSettingValue.current.value : undefined;
    pendingSettingValue.current = undefined;
    setEditor({ state: "editing", key: descriptor.key, draft: pending === undefined ? draftForSetting(descriptor, configuration.value) : draftWithText(descriptor, pending) });
    requestAnimationFrame(() => document.getElementById(settingAnchor(descriptor.key))?.scrollIntoView({ block: "center" }));
  }, [linkedSetting, editor.state, catalog, configuration]);

  function navigateProtected(destination: string) {
    if (hasDraft && !window.confirm("Discard the unsaved configuration draft?")) return;
    setEditor({ state: "closed" });
    setSecretOpen(false);
    navigate(destination);
  }
  function updateParams(update: (next: URLSearchParams) => void, protect = false) {
    const next = new URLSearchParams(params); update(next);
    const destination = `/settings${next.size ? `?${next}` : ""}`;
    protect ? navigateProtected(destination) : navigate(destination);
  }
  function setTab(nextTab: SettingsTab) { updateParams((next) => next.set("tab", nextTab), nextTab !== tab); }
  function onTabKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const target = tabKeyTarget(index, event.key, tabs.length); if (target === undefined) return;
    event.preventDefault(); tabRefs.current[target]?.focus(); setTab(tabs[target].id);
  }
  function selectScope(nextScope: ConfigurationScope) {
    updateParams((next) => {
      next.set("tab", "configuration"); next.set("scope", nextScope.type);
      if (nextScope.type === "project") next.set("projectId", nextScope.projectId); else next.delete("projectId");
      next.delete("setting");
    }, true);
  }
  function selectSetting(key: string, value?: string) {
    if (editor.state !== "closed" && editor.key !== key && !window.confirm("Discard the current configuration draft?")) return;
    if (secretOpen && !window.confirm("Close and clear the unsaved secret form?")) return;
    setSecretOpen(false);
    if (catalog.state !== "ready" || configuration.state !== "ready") return;
    const descriptor = catalog.value.settings.find((candidate) => candidate.key === key); if (!descriptor) return;
    if (!descriptor.allowedScopes.includes(scope.type) && value !== undefined && descriptor.allowedScopes.includes("user")) {
      pendingSettingValue.current = { key, value };
      updateParams((next) => { next.set("tab", "configuration"); next.set("scope", "user"); next.delete("projectId"); next.set("setting", key); }, true);
      return;
    }
    updateParams((next) => { next.set("tab", "configuration"); next.set("setting", key); });
    openedDeepLink.current = `${scope.type}:${scopeProjectId(scope) ?? ""}:${key}`;
    setEditor({ state: "editing", key, draft: value === undefined ? draftForSetting(descriptor, configuration.value) : draftWithText(descriptor, value) });
  }
  async function refetchConfiguration(expectedScope = scope) {
    const value = await apiClient.getConfigurationState(scopeProjectId(expectedScope)); setConfiguration({ state: "ready", value }); return value;
  }

  return <div className="page settings-page">
    <PageHeader className="settings-header" eyebrow="Local control plane" title="Settings & Health" description="Edit supported user and registered-project settings through the public configuration API, and inspect diagnostic guidance separately." breadcrumbs={[{ label: "Settings & Health" }]} actions={<button type="button" className="button button--primary" disabled={refreshing} onClick={() => { setNotice({ state: "none" }); setRefreshVersion((value) => value + 1); }}>{refreshing ? "Refreshing…" : "Refresh authoritative state"}</button>} />
    <Notice notice={notice} />
    <SettingsSummary health={health} doctor={doctor} connection={dashboard.connection} />
    <div className="checkpoint-tabs settings-tabs" role="tablist" aria-label="Settings workspace">{tabs.map((item, index) => <button key={item.id} ref={(value) => { tabRefs.current[index] = value; }} id={`settings-tab-${item.id}`} type="button" role="tab" tabIndex={tab === item.id ? 0 : -1} aria-controls={`settings-panel-${item.id}`} aria-selected={tab === item.id} onKeyDown={(event) => onTabKeyDown(event, index)} onClick={() => setTab(item.id)}>{item.label}</button>)}</div>
    {tabs.map((item) => <div key={item.id} id={`settings-panel-${item.id}`} className="settings-panel" role="tabpanel" tabIndex={item.id === tab ? 0 : -1} aria-labelledby={`settings-tab-${item.id}`} aria-busy={refreshing} hidden={item.id !== tab}>
      {item.id === "health" && <HealthPanel resource={doctor} />}
      {item.id === "provider" && <ProviderPanel resource={doctor} onSelectExecutable={(value) => selectSetting("provider.codex.executable", value)} />}
      {item.id === "projects" && <ProjectsPanel resource={projects} onRegister={() => registerDialog.current?.showModal()} onConfigure={(projectId) => selectScope(configurationScope("project", projectId))} />}
      {item.id === "configuration" && <ConfigurationPanel catalog={catalog} state={configuration} projects={projects} scope={scope} query={searchDraft} editor={editor} secretOpen={secretOpen} onQuery={(query) => { setSearchDraft(query); updateParams((next) => query.trim() ? next.set("q", query) : next.delete("q")); }} onScope={selectScope} onEdit={selectSetting} onEditor={setEditor} onNotice={setNotice} onSecretOpen={setSecretOpen} refetch={refetchConfiguration} />}
    </div>)}
    <RegisterProjectDialog refValue={registerDialog} onRegistered={() => { setNotice({ state: "applied", message: "Project registration was durably accepted.", restart: "none", replayed: false }); setRefreshVersion((value) => value + 1); }} />
  </div>;
}

function ConfigurationPanel({ catalog, state, projects, scope, query, editor, secretOpen, onQuery, onScope, onEdit, onEditor, onNotice, onSecretOpen, refetch }: {
  catalog: Loadable<ConfigurationCatalog>; state: Loadable<ConfigurationState>; projects: Loadable<Project[]>; scope: ConfigurationScope; query: string;
  editor: ConfigurationEditorState; secretOpen: boolean; onQuery(query: string): void; onScope(scope: ConfigurationScope): void; onEdit(key: string): void;
  onEditor(state: ConfigurationEditorState): void; onNotice(notice: ConfigurationNotice): void; onSecretOpen(open: boolean): void; refetch(scope?: ConfigurationScope): Promise<ConfigurationState>;
}) {
  const restoreKey = useRef(idempotencyKey("restore"));
  if (catalog.state === "loading" || state.state === "loading") return <DetailLoading label="Loading the supported settings catalog and authoritative state" />;
  if (catalog.state !== "ready") return <DetailFailure title="Configuration catalog unavailable" message={catalog.message} />;
  if (state.state !== "ready") return <DetailFailure title="Configuration state unavailable" message={state.message} />;
  if (!configurationScopesEqual(state.value.scope, scope)) return <DetailLoading label="Loading authoritative state for the selected scope" />;
  const readyState = state.value;
  const grouped = groupConfigurationSettings(catalog.value.settings.filter((setting) => settingMatchesSearch(setting, query)));
  const projectOptions = projects.state === "ready" ? projects.value.filter((project) => project.status === "active") : [];
  const activeDescriptor = editor.state === "closed" ? undefined : catalog.value.settings.find((item) => item.key === editor.key);
  const canRestore = catalog.value.settings.some((item) => item.allowedScopes.includes(scope.type) && item.actions.includes("restore"));
  async function restore() {
    if (!canRestore || editor.state !== "closed" || !window.confirm(`Restore the previous ${scope.type} configuration revision?`)) return;
    onNotice({ state: "none" });
    try {
      const result = await apiClient.restoreConfiguration({ scope, expectedRevision: readyState.revision }, restoreKey.current);
      try { await refetch(scope); } catch { onNotice({ state: "failed", message: "The restore receipt was returned, but authoritative state could not be refreshed. Refresh before issuing another command." }); return; }
      restoreKey.current = idempotencyKey("restore"); onEditor({ state: "closed" });
      onNotice({ state: "restored", message: `Previous ${scope.type} configuration restored.`, restart: result.restart, replayed: result.replayed });
    } catch (cause) {
      onNotice({ state: "failed", message: mutationError(cause, "Restore") });
      if (cause instanceof ApiRequestError && cause.status === 409) await safeRefetch(refetch, scope);
    }
  }
  return <section aria-labelledby="configuration-heading">
    <div className="settings-section-heading"><div><p className="eyebrow">Catalog-driven editor</p><h2 id="configuration-heading">Supported configuration</h2></div><code>{scope.type === "project" ? scope.projectId : "user"}</code></div>
    <div className="settings-boundary"><strong>Winning precedence</strong><span>Command line → run/work → project → user → shipped defaults</span><p>Each card shows the effective winner reported by the server. Writes compare the displayed revision, then refetch authoritative state.</p></div>
    <div className="configuration-toolbar">
      <fieldset><legend>Configuration scope</legend><label><input type="radio" name="configuration-scope" checked={scope.type === "user"} onChange={() => onScope(configurationScope("user"))} /> User</label><label><input type="radio" name="configuration-scope" checked={scope.type === "project"} disabled={!projectOptions.length} onChange={() => { const project = projectOptions[0]; if (project) onScope(configurationScope("project", project.id)); }} /> Project</label></fieldset>
      {scope.type === "project" && <label className="field configuration-project"><span>Registered project</span><select aria-label="Registered project identity" value={scope.projectId} onChange={(event) => onScope(configurationScope("project", event.target.value))}>{projectOptions.map((project) => <option key={project.id} value={project.id}>{project.name} · {project.id}</option>)}</select></label>}
      <label className="field configuration-search"><span>Search settings</span><input type="search" value={query} onChange={(event) => onQuery(event.target.value)} placeholder="Title, key, description, or group" /></label>
      <button type="button" className="button" disabled={!canRestore || editor.state !== "closed" || secretOpen} onClick={() => void restore()}>Restore previous revision</button>
    </div>
    {scope.type === "project" && !projectOptions.length && <AsyncPanel compact state="validation" title="Project scope unavailable" message="Register an active project first. Project settings require its public project identity; arbitrary filesystem roots are never accepted here." />}
    <p className="configuration-revision">Authoritative revision <code>{state.value.revision}</code></p>
    <div className="configuration-groups">{CONFIGURATION_GROUPS.map((group) => { const settings = grouped.get(group)!; const heading = `configuration-group-${group.replaceAll(" ", "-").toLowerCase()}`; return <section key={group} className="configuration-group" aria-labelledby={heading}><header><h3 id={heading}>{group}</h3><span>{settings.length} {settings.length === 1 ? "setting" : "settings"}</span></header>{settings.length ? <div className="configuration-setting-list">{settings.map((descriptor) => <SettingCard key={descriptor.key} descriptor={descriptor} state={state.value} scope={scope} active={editor.state !== "closed" && editor.key === descriptor.key} onEdit={() => onEdit(descriptor.key)} />)}</div> : <p className="settings-empty">{query.trim() ? "No matching settings in this group." : "No settings are published in this group by the current catalog."}</p>}</section>; })}</div>
    {activeDescriptor && <ConfigurationEditor descriptor={activeDescriptor} state={state.value} scope={scope} editor={editor as Exclude<ConfigurationEditorState, { state: "closed" }>} onEditor={onEditor} onNotice={onNotice} refetch={refetch} />}
    {scope.type === "user" && <SecretWriter open={secretOpen} disabled={editor.state !== "closed"} secretRevision={state.value.secretRevision} onNotice={onNotice} onOpen={onSecretOpen} refetch={refetch} />}
    <aside className="configuration-recovery"><strong>Write and recovery contract</strong><p>Preview validates without writing. Apply and restore are idempotent, compare revisions, and report restart impact. If a command remains pending after interruption, refresh authoritative state and inspect its receipt or daemon recovery status before retrying.</p></aside>
  </section>;
}

function SettingCard({ descriptor, state, scope, active, onEdit }: { descriptor: ConfigurationDescriptor; state: ConfigurationState; scope: ConfigurationScope; active: boolean; onEdit(): void }) {
  const effective = effectiveSetting(state, descriptor.key); const configured = state.configured.some((entry) => entry.key === descriptor.key);
  const scopeAllowed = descriptor.allowedScopes.includes(scope.type); const editable = scopeAllowed && descriptor.actions.includes("preview") && descriptor.actions.includes("apply");
  return <article id={settingAnchor(descriptor.key)} className="configuration-setting" data-active={active}><header><div><h4>{descriptor.title}</h4><code>{descriptor.key}</code></div><span>{descriptor.type.replaceAll("_", " ")}</span></header><p>{descriptor.description}</p><dl><Fact label="Effective value" value={effective ? typedValueText(effective.value) : "Not reported"} mono /><Fact label="Winning source" value={effective ? `${humanize(effective.source.scope)} · ${effective.source.reference}` : "Not reported"} /><Fact label="Current scope" value={configured ? "Configured override" : "Inherited"} /><Fact label="Restart" value={descriptor.restart === "daemon" ? "Daemon restart" : "None"} /></dl><footer><span className={editable ? "setting-supported" : "setting-readonly"}>{editable ? "Editable through preview + apply" : scopeAllowed ? "Unsupported by this server action set" : `Read-only at ${scope.type} scope`}</span><button type="button" className="button" disabled={!editable} aria-expanded={active} aria-controls="configuration-editor" onClick={onEdit}>Edit</button></footer></article>;
}

function ConfigurationEditor({ descriptor, state, scope, editor, onEditor, onNotice, refetch }: { descriptor: ConfigurationDescriptor; state: ConfigurationState; scope: ConfigurationScope; editor: Exclude<ConfigurationEditorState, { state: "closed" }>; onEditor(state: ConfigurationEditorState): void; onNotice(notice: ConfigurationNotice): void; refetch(scope?: ConfigurationScope): Promise<ConfigurationState> }) {
  const applyKey = useRef(idempotencyKey("apply"));
  const busy = editor.state === "previewing" || editor.state === "applying";
  const preview = editor.state === "previewed" || editor.state === "applying" ? editor.preview : undefined;
  function updateDraft(draft: ConfigurationDraft) { applyKey.current = idempotencyKey("apply"); onEditor({ state: "editing", key: descriptor.key, draft }); }
  async function previewDraft(event: FormEvent) {
    event.preventDefault(); onNotice({ state: "none" }); let body: Schemas["ConfigurationMutationRequest"];
    try { body = mutationFromDraft(scope, descriptor, editor.draft, state.revision); } catch (cause) { onEditor({ state: "failed", key: descriptor.key, draft: editor.draft, message: errorMessage(cause) }); return; }
    onEditor({ state: "previewing", key: descriptor.key, draft: editor.draft });
    try { onEditor({ state: "previewed", key: descriptor.key, draft: editor.draft, preview: await apiClient.previewConfigurationMutation(body) }); }
    catch (cause) { if (cause instanceof ApiRequestError && cause.status === 409) { await safeRefetch(refetch, scope); onEditor({ state: "stale", key: descriptor.key, draft: editor.draft, message: "Configuration changed before preview. Your draft is preserved; review refreshed values and preview again." }); } else onEditor({ state: "failed", key: descriptor.key, draft: editor.draft, message: mutationError(cause, "Preview") }); }
  }
  async function apply() {
    if (!preview?.valid) return;
    if (state.revision !== preview.before.revision) { onEditor({ state: "stale", key: descriptor.key, draft: editor.draft, message: "Authoritative state changed after this preview. Your draft is preserved; preview it again before applying." }); return; }
    let body: Schemas["ConfigurationMutationRequest"];
    try { body = mutationFromDraft(scope, descriptor, editor.draft, preview.before.revision); } catch (cause) { onEditor({ state: "failed", key: descriptor.key, draft: editor.draft, message: errorMessage(cause) }); return; }
    onEditor({ state: "applying", key: descriptor.key, draft: editor.draft, preview });
    try { const result = await apiClient.applyConfigurationMutation(body, applyKey.current); try { await refetch(scope); } catch { onEditor({ state: "failed", key: descriptor.key, draft: editor.draft, message: "The apply receipt was returned, but authoritative state could not be refreshed. Your draft is preserved; refresh before issuing another command." }); return; } applyKey.current = idempotencyKey("apply"); onEditor({ state: "closed" }); onNotice({ state: "applied", message: `${descriptor.title} was applied at ${scope.type} scope.`, restart: result.restart, replayed: result.replayed }); }
    catch (cause) { if (cause instanceof ApiRequestError && cause.status === 409) { await safeRefetch(refetch, scope); onEditor({ state: "stale", key: descriptor.key, draft: editor.draft, message: "Apply found a revision conflict. Your draft is preserved; compare it with refreshed state and preview again." }); } else onEditor({ state: "failed", key: descriptor.key, draft: editor.draft, message: mutationError(cause, "Apply") }); }
  }
  return <section id="configuration-editor" className="configuration-editor" aria-labelledby="configuration-editor-title" aria-busy={busy}><header><div><p className="eyebrow">Typed mutation</p><h3 id="configuration-editor-title">Edit {descriptor.title}</h3></div><button type="button" className="icon-button" aria-label="Close configuration editor" disabled={busy} onClick={() => { if (window.confirm("Discard this configuration draft?")) onEditor({ state: "closed" }); }}>×</button></header><form onSubmit={(event) => void previewDraft(event)}><fieldset disabled={busy}><legend>Change operation</legend><label><input type="radio" name="change-operation" checked={editor.draft.operation === "set"} onChange={() => updateDraft(draftForSetting(descriptor, state))} /> Set typed value</label><label><input type="radio" name="change-operation" checked={editor.draft.operation === "unset"} onChange={() => updateDraft({ operation: "unset" })} /> Remove this scope override</label></fieldset>{editor.draft.operation === "set" && <TypedControl descriptor={descriptor} draft={editor.draft} disabled={busy} onChange={updateDraft} />}<div className="configuration-editor__actions"><button type="button" className="button" disabled={busy} onClick={() => { if (window.confirm("Discard this configuration draft?")) onEditor({ state: "closed" }); }}>Discard draft</button><button type="submit" className="button button--primary" disabled={busy}>{editor.state === "previewing" ? "Previewing…" : "Preview change"}</button></div></form>{(editor.state === "failed" || editor.state === "stale") && <AsyncPanel compact state="error" title={editor.state === "stale" ? "Authoritative state changed" : "Change not ready"} message={editor.message} />}{preview && <PreviewPanel preview={preview} descriptor={descriptor} onApply={() => void apply()} applying={editor.state === "applying"} />}</section>;
}

function TypedControl({ descriptor, draft, disabled, onChange }: { descriptor: ConfigurationDescriptor; draft: Exclude<ConfigurationDraft, { operation: "unset" }>; disabled: boolean; onChange(draft: ConfigurationDraft): void }) {
  const id = `configuration-input-${settingAnchor(descriptor.key)}`; const help = `${id}-help`;
  if (draft.type === "boolean") return <label className="configuration-toggle"><input id={id} type="checkbox" checked={draft.value} disabled={disabled} onChange={(event) => onChange({ operation: "set", type: "boolean", value: event.target.checked })} /><span>{draft.value ? "Enabled" : "Disabled"}</span></label>;
  if (draft.type === "enum") return <label className="field"><span>{descriptor.title}</span><select id={id} required={descriptor.constraints.required} disabled={disabled} value={draft.value} onChange={(event) => onChange({ operation: "set", type: "enum", value: event.target.value })}>{descriptor.constraints.allowedValues?.map((value) => <option key={value} value={value}>{humanize(value)}</option>)}</select></label>;
  return <label className="field"><span>{descriptor.title}</span><input id={id} type={draft.type === "integer" ? "number" : "text"} step={draft.type === "integer" ? 1 : undefined} min={draft.type === "integer" ? descriptor.constraints.minimum : undefined} max={draft.type === "integer" ? descriptor.constraints.maximum : undefined} required={descriptor.constraints.required} disabled={disabled} aria-describedby={help} autoComplete="off" value={draft.value} onChange={(event) => onChange({ operation: "set", type: draft.type, value: event.target.value } as ConfigurationDraft)} /><small id={help}>{controlHelp(descriptor)}</small></label>;
}
function PreviewPanel({ preview, descriptor, onApply, applying }: { preview: ConfigurationPreview; descriptor: ConfigurationDescriptor; onApply(): void; applying: boolean }) { const before = effectiveSetting(preview.before, descriptor.key); const after = effectiveSetting(preview.after, descriptor.key); return <section className="configuration-preview" aria-labelledby="configuration-preview-title"><header><h4 id="configuration-preview-title">Before / after preview</h4><span data-valid={preview.valid}>{preview.valid ? "Validated" : "Validation failed"}</span></header><div><PreviewValue label="Before" value={before ? typedValueText(before.value) : "Not reported"} source={before?.source} /><PreviewValue label="After" value={after ? typedValueText(after.value) : "Not reported"} source={after?.source} /></div>{preview.issues.length > 0 && <ul className="configuration-issues">{preview.issues.map((issue, index) => <li key={`${issue.field}-${issue.code}-${index}`}><code>{issue.field}</code><strong>{issue.code}</strong><span>{issue.message}</span></li>)}</ul>}<p>Restart impact: <strong>{preview.restart === "daemon" ? "Daemon restart required" : "No restart"}</strong></p><button type="button" className="button button--primary" disabled={!preview.valid || applying} onClick={onApply}>{applying ? "Applying…" : "Apply validated change"}</button></section>; }
function PreviewValue({ label, value, source }: { label: string; value: string; source?: ConfigurationState["effective"][number]["source"] }) { return <article><span>{label}</span><strong>{value}</strong><small>{source ? `${humanize(source.scope)} · ${source.reference}` : "No winning source reported"}</small></article>; }

function SecretWriter({ open, disabled, secretRevision, onNotice, onOpen, refetch }: { open: boolean; disabled: boolean; secretRevision?: string; onNotice(notice: ConfigurationNotice): void; onOpen(open: boolean): void; refetch(): Promise<ConfigurationState> }) {
  const [name, setName] = useState("");
  const [status, setStatus] = useState<{ state: "idle" } | { state: "writing" } | { state: "failed"; message: string }>({ state: "idle" });
  const secretInput = useRef<HTMLInputElement>(null);
  const secretKey = useRef(idempotencyKey("secret"));
  useEffect(() => () => { if (secretInput.current) secretInput.current.value = ""; onOpen(false); }, [onOpen]);
  function close() { if (secretInput.current) secretInput.current.value = ""; setStatus({ state: "idle" }); onOpen(false); }
  async function submit(event: FormEvent) {
    event.preventDefault(); if (!secretRevision) { setStatus({ state: "failed", message: "The server did not publish a secret revision, so secret writes are unavailable." }); return; }
    const secretMaterial = secretInput.current?.value ?? ""; if (!secretMaterial) return;
    setStatus({ state: "writing" }); onNotice({ state: "none" });
    try { const receipt = await apiClient.writeConfigurationSecret({ name: name.trim(), value: secretMaterial, expectedRevision: secretRevision }, secretKey.current); if (secretInput.current) secretInput.current.value = ""; setName(""); onOpen(false); secretKey.current = idempotencyKey("secret"); try { await refetch(); } catch { setStatus({ state: "failed", message: "The secret receipt was returned and the value was cleared, but authoritative state could not be refreshed. Refresh before another write." }); return; } setStatus({ state: "idle" }); onNotice({ state: "secret-written", message: `Secret ${receipt.name} was written. Its value was cleared and cannot be read back here.`, restart: receipt.restart, replayed: receipt.replayed }); }
    catch (cause) { if (cause instanceof ApiRequestError && cause.status === 409) { await safeRefetch(refetch, configurationScope("user")); setStatus({ state: "failed", message: "The secret revision changed. The entered value remains only in the password control; submit again after reviewing the refreshed revision." }); } else setStatus({ state: "failed", message: mutationError(cause, "Secret write") }); }
  }
  return <section className="secret-writer" aria-labelledby="secret-writer-title"><header><div><p className="eyebrow">Write-only secret channel</p><h3 id="secret-writer-title">Provider secrets</h3></div><button type="button" className="button" disabled={disabled || !secretRevision} aria-expanded={open} aria-controls="secret-write-form" onClick={() => { if (open) close(); else onOpen(true); }}>{open ? "Close and clear" : "Write secret"}</button></header><p>Stored secret values are never requested, prefetched, held in application state, or rendered.</p>{open && <form id="secret-write-form" aria-busy={status.state === "writing"} onSubmit={(event) => void submit(event)}><label className="field"><span>Secret name</span><input required pattern="[a-z][a-z0-9-]{0,63}" autoComplete="off" value={name} onChange={(event) => { secretKey.current = idempotencyKey("secret"); setName(event.target.value); }} placeholder="codex-api-key" /></label><label className="field"><span>New secret value</span><input ref={secretInput} required type="password" autoComplete="new-password" onInput={() => { secretKey.current = idempotencyKey("secret"); }} /></label><button type="button" className="button" disabled={status.state === "writing"} onClick={close}>Cancel and clear</button><button type="submit" className="button button--primary" disabled={status.state === "writing"}>{status.state === "writing" ? "Writing…" : "Write secret"}</button></form>}{status.state === "failed" && <p className="form-error" role="alert">{status.message}</p>}<small>Secret revision: {secretRevision ? <code>{secretRevision}</code> : "Unavailable — read-only"}</small></section>;
}

function SettingsSummary({ health, doctor, connection }: { health: Loadable<Health>; doctor: Loadable<DoctorReport>; connection: string }) { const recovery = health.state === "ready" ? health.value.recovery : undefined; const doctorStatus = doctor.state === "ready" ? doctor.value.status : doctor.state; return <section className="settings-summary" aria-label="Control plane summary"><Summary label="Daemon API" value={health.state === "ready" ? "Reachable" : health.state === "loading" ? "Checking" : "Unavailable"} tone={health.state === "ready" ? "healthy" : health.state === "loading" ? "loading" : "unhealthy"} /><Summary label="Subsystems" value={humanize(doctorStatus)} tone={doctorStatus} /><Summary label="Event stream" value={humanize(connection)} tone={connection === "live" ? "healthy" : connection === "offline" ? "unhealthy" : "degraded"} /><Summary label="Recovery" value={recovery ? (recovery.reconcileRequired ? `${recovery.reconcileRequired} unresolved` : `${recovery.reconciled} checked`) : "Checking"} tone={recovery?.reconcileRequired ? "unhealthy" : recovery ? "healthy" : "loading"} /></section>; }
function Summary({ label, value, tone }: { label: string; value: string; tone: string }) { return <article data-tone={tone}><span>{label}</span><strong>{value}</strong></article>; }
function HealthPanel({ resource }: { resource: Loadable<DoctorReport> }) { if (resource.state === "loading") return <DetailLoading label="Running subsystem diagnostics" />; if (resource.state !== "ready") return <DetailFailure title="Subsystem diagnostics unavailable" message={resource.message} />; return <section aria-labelledby="health-heading"><div className="settings-section-heading"><div><p className="eyebrow">Point-in-time diagnostics</p><h2 id="health-heading">Eight required subsystems</h2></div><span>Generated {formatDate(resource.value.generatedAt)}</span></div><p className="doctor-separation">Doctor results are read-only guidance. Configuration changes are available only in the catalog-driven Configuration tab.</p><div className="health-grid">{resource.value.checks.map((check) => <HealthCard key={check.subsystem} check={check} />)}</div></section>; }
function HealthCard({ check }: { check: HealthCheck }) { return <article className="health-card" data-status={check.status}><header><div><span>{subsystemLabels[check.subsystem]}</span><code>{check.code}</code></div><strong>{humanize(check.status)}</strong></header><p>{check.message}</p>{check.action && <aside className="doctor-guidance"><span>Doctor guidance</span><p>{check.action}</p></aside>}</article>; }
function ProviderPanel({ resource, onSelectExecutable }: { resource: Loadable<DoctorReport>; onSelectExecutable(value: string): void }) { if (resource.state === "loading") return <DetailLoading label="Inspecting provider readiness" />; if (resource.state !== "ready") return <DetailFailure title="Provider diagnostics unavailable" message={resource.message} />; const checks = resource.value.checks.filter((check) => check.subsystem === "codex" || check.subsystem === "provider"); return <section aria-labelledby="provider-heading"><div className="settings-section-heading"><div><p className="eyebrow">Credential-free inspection</p><h2 id="provider-heading">Codex &amp; selected provider</h2></div></div><p className="settings-boundary">Account identifiers, tokens, balances, and raw provider responses are never part of this projection.</p><div className="provider-grid">{checks.map((check) => <article className="provider-card" key={check.subsystem}><HealthCard check={check} />{check.providerDetails ? <ProviderFacts details={check.providerDetails} onSelectExecutable={onSelectExecutable} /> : <p className="settings-empty">No safe provider details were reported for this check.</p>}</article>)}</div></section>; }
function ProviderFacts({ details, onSelectExecutable }: { details: ProviderDetails; onSelectExecutable(value: string): void }) { return <div className="provider-facts"><dl><Fact label="Name" value={details.name} /><Fact label="Version" value={details.version || "Not reported"} /><Fact label="Authentication" value={humanize(details.authentication)} /><Fact label="Usage" value={humanize(details.usage)} /><Fact label="Platform" value={details.platform || "Not reported"} /><Fact label="Executable" value={details.executableIdentity || "Not reported"} mono /></dl><TokenList title="Instruction sources" values={details.instructionSources} />{details.conflictingExecutables.length > 0 && <section className="executable-conflicts" aria-labelledby="executable-conflicts-title"><h3 id="executable-conflicts-title">Conflicting executables</h3><p>Selecting one opens a catalog-backed draft; it does not write immediately.</p><ul>{details.conflictingExecutables.map((value) => <li key={value}><code>{value}</code><button type="button" className="button" onClick={() => onSelectExecutable(value)}>Use this executable</button></li>)}</ul></section>}<div className="capability-grid"><CapabilityList title="Available capabilities" values={details.availableCapabilities.map((value) => ({ name: value.name, detail: value.version }))} /><CapabilityList title="Unavailable capabilities" values={details.unavailableCapabilities.map((value) => ({ name: value.name, detail: value.reason }))} warning /></div></div>; }
function ProjectsPanel({ resource, onRegister, onConfigure }: { resource: Loadable<Project[]>; onRegister(): void; onConfigure(projectId: string): void }) { if (resource.state === "loading") return <DetailLoading label="Loading registered projects" />; if (resource.state !== "ready") return <DetailFailure title="Project registry unavailable" message={resource.message} />; return <section aria-labelledby="projects-heading"><div className="settings-section-heading"><div><p className="eyebrow">Durable registry</p><h2 id="projects-heading">Registered projects</h2></div><button type="button" className="button button--primary" onClick={onRegister}>Register project</button></div><p className="settings-boundary">Project-scoped configuration uses a registered project ID. The dashboard never sends an arbitrary mutation root.</p>{resource.value.length ? <div className="project-settings-list">{resource.value.map((project) => <article key={project.id}><header><div><strong>{project.name}</strong><code>{project.id}</code></div><span data-status={project.status}>{humanize(project.status)}</span></header><dl><Fact label="Source fingerprint" value={project.sourceHash} mono /><Fact label="Resource version" value={String(project.resourceVersion)} /><Fact label="Registered" value={formatDate(project.createdAt)} /><Fact label="Updated" value={formatDate(project.updatedAt)} /></dl>{project.status === "active" && <footer><button type="button" className="button" onClick={() => onConfigure(project.id)}>Configure project</button></footer>}</article>)}</div> : <div className="settings-empty-panel"><strong>No projects registered</strong><p>Register an existing local project before using project-scoped settings.</p><button type="button" className="button" onClick={onRegister}>Register project</button></div>}</section>; }
function RegisterProjectDialog({ refValue, onRegistered }: { refValue: RefObject<HTMLDialogElement | null>; onRegistered(): void }) { const [name, setName] = useState(""); const [source, setSource] = useState(""); const [submitting, setSubmitting] = useState(false); const [error, setError] = useState(""); const key = useRef(idempotencyKey("project-register")); function close() { if (!submitting && ((!name && !source) || window.confirm("Discard the unsaved project registration?"))) { refValue.current?.close(); setError(""); } } async function submit(event: FormEvent) { event.preventDefault(); let body: Schemas["ProjectRegistration"]; try { body = normalizeProjectRegistration(name, source); } catch (cause) { setError(errorMessage(cause)); return; } setSubmitting(true); setError(""); try { await apiClient.registerProject(body, key.current); refValue.current?.close(); setName(""); setSource(""); key.current = idempotencyKey("project-register"); onRegistered(); } catch (cause) { setError(settingsError(cause, "Project registration")); } finally { setSubmitting(false); } } return <dialog ref={refValue} className="work-dialog project-register-dialog" aria-labelledby="project-register-title" aria-describedby="project-register-description" onCancel={(event) => { if (submitting || ((name || source) && !window.confirm("Discard the unsaved project registration?"))) event.preventDefault(); }}><form aria-busy={submitting} onSubmit={(event) => void submit(event)}><header className="work-dialog__header"><div><p className="eyebrow">Idempotent API command</p><h2 id="project-register-title">Register project</h2></div><button type="button" className="icon-button" aria-label="Close project registration" onClick={close}>×</button></header><p id="project-register-description" className="work-dialog__intro">Register an existing absolute project directory. Configuration mutations subsequently use only the returned project identity.</p><label className="field"><span>Project name</span><input required autoFocus value={name} onChange={(event) => setName(event.target.value)} /></label><label className="field"><span>Absolute project path</span><input required value={source} onChange={(event) => setSource(event.target.value)} /></label>{error && <p className="form-error" role="alert">{error}</p>}<footer className="work-dialog__footer"><button type="button" className="button" disabled={submitting} onClick={close}>Cancel</button><button type="submit" className="button button--primary" disabled={submitting}>{submitting ? "Registering…" : "Register project"}</button></footer></form></dialog>; }

function Notice({ notice }: { notice: ConfigurationNotice }) { if (notice.state === "none") return null; if (notice.state === "failed") return <AsyncPanel compact state="error" title="Configuration command failed" message={notice.message} />; return <AsyncPanel compact state="success" title={notice.state === "restored" ? "Configuration restored" : notice.state === "secret-written" ? "Secret receipt received" : "Configuration applied"} message={`${notice.message} ${notice.restart === "daemon" ? "Restart the daemon before expecting new runtime behavior." : "No daemon restart is required."}${notice.replayed ? " The server replayed the original idempotent receipt." : ""}`} />; }
function Fact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) { return <div><dt>{label}</dt><dd className={mono ? "mono" : undefined}>{value}</dd></div>; }
function TokenList({ title, values }: { title: string; values: string[] }) { return <section className="settings-token-list"><h3>{title}</h3>{values.length ? <ul>{values.map((value) => <li key={value}><code>{value}</code></li>)}</ul> : <p>None reported</p>}</section>; }
function CapabilityList({ title, values, warning = false }: { title: string; values: Array<{ name: string; detail: string }>; warning?: boolean }) { return <section className="capability-list" data-warning={warning}><h3>{title}</h3>{values.length ? <ul>{values.map((value) => <li key={value.name}><strong>{value.name}</strong><span>{value.detail}</span></li>)}</ul> : <p>None reported</p>}</section>; }
function humanize(value: string) { return value.replaceAll("_", " ").replace(/\b\w/g, (match) => match.toUpperCase()); }
function idempotencyKey(operation: string) { return `dashboard-configuration-${operation}-${crypto.randomUUID()}`; }
function errorMessage(cause: unknown) { return cause instanceof Error ? cause.message : "The request is invalid."; }
function settingsError(cause: unknown, resource: string) { if (cause instanceof ApiRequestError && cause.status === 400) return `${resource} rejected the request.`; if (cause instanceof ApiRequestError && (cause.status === 404 || cause.status === 405 || cause.status === 501)) return `${resource} is unsupported by this daemon version.`; if (cause instanceof ApiRequestError && cause.status === 503) return `${resource} is temporarily unavailable.`; return `${resource} could not be loaded from the local API.`; }
function loadFailure(cause: unknown, resource: string): Loadable<never> { const unsupported = cause instanceof ApiRequestError && (cause.status === 404 || cause.status === 405 || cause.status === 501); return { state: unsupported ? "unsupported" : "failed", message: settingsError(cause, resource) }; }
function mutationError(cause: unknown, action: string) { if (cause instanceof ApiRequestError && cause.status === 409) return `${action} found a revision conflict.`; if (cause instanceof ApiRequestError && (cause.status === 400 || cause.status === 422)) return `${action} was rejected by server validation: ${cause.message}`; if (cause instanceof ApiRequestError && cause.status === 503) return `${action} is pending recovery or temporarily unavailable. Refresh authoritative state before retrying.`; return `${action} failed without changing the displayed authoritative state.`; }
function setIfActive<T>(abort: AbortController, setter: (value: Loadable<T>) => void, value: Loadable<T>) { if (!abort.signal.aborted) setter(value); }
async function safeRefetch(refetch: (scope?: ConfigurationScope) => Promise<ConfigurationState>, scope: ConfigurationScope) { try { await refetch(scope); } catch { /* The preserved draft already explains how to retry. */ } }
function draftWithText(descriptor: ConfigurationDescriptor, value: string): ConfigurationDraft { switch (descriptor.type) { case "boolean": return { operation: "set", type: "boolean", value: value === "true" }; case "integer": return { operation: "set", type: "integer", value }; case "string": case "enum": case "path": case "secret_reference": return { operation: "set", type: descriptor.type, value }; } }
function controlHelp(descriptor: ConfigurationDescriptor) { const parts = [`Catalog type: ${descriptor.type.replaceAll("_", " ")}.`]; if (descriptor.constraints.absolute) parts.push("Use an absolute path."); if (descriptor.constraints.existingFile) parts.push("The daemon must be able to read the existing file."); if (descriptor.constraints.minimum !== undefined || descriptor.constraints.maximum !== undefined) parts.push(`Range ${descriptor.constraints.minimum ?? "unbounded"} to ${descriptor.constraints.maximum ?? "unbounded"}.`); return parts.join(" "); }
