import { useEffect, useMemo, useRef, useState, type FormEvent, type KeyboardEvent, type RefObject } from "react";

import { ApiRequestError, apiClient } from "../api/client";
import type { components } from "../api/schema.generated";
import { useRouter } from "../app/router";
import { useDashboardState } from "../state/DashboardStateProvider";
import { DetailFailure, DetailLoading, formatDate } from "./WorkDetailPage";
import {
  decodeDoctorReport,
  decodeEffectiveConfiguration,
  normalizeProjectRegistration,
  parseSettingsTab,
  sortProjects,
  type DoctorReport,
  type EffectiveConfiguration,
  type HealthCheck,
  type ProviderDetails,
  type SettingsTab,
} from "./settingsModel";

type Schemas = components["schemas"];
type Project = Schemas["Project"];
type Health = Schemas["Health"];
type Resource<T> = { state: "loading" } | { state: "ready"; value: T } | { state: "error"; message: string };

const tabs: Array<{ id: SettingsTab; label: string }> = [
  { id: "health", label: "System health" },
  { id: "provider", label: "Provider" },
  { id: "projects", label: "Projects" },
  { id: "configuration", label: "Configuration" },
];

const subsystemLabels: Record<HealthCheck["subsystem"], string> = {
  database: "Database",
  daemon: "Daemon",
  paths: "Application paths",
  git: "Git repository",
  codex: "Codex installation",
  github: "GitHub delivery",
  configuration: "Configuration",
  provider: "Selected provider",
};

export function SettingsPage() {
  const { search, navigate } = useRouter();
  const { state } = useDashboardState();
  const params = useMemo(() => new URLSearchParams(search), [search]);
  const tab = parseSettingsTab(params.get("tab"));
  const projectRoot = params.get("projectRoot")?.trim() ?? "";
  const [rootDraft, setRootDraft] = useState(projectRoot);
  const [health, setHealth] = useState<Resource<Health>>({ state: "loading" });
  const [doctor, setDoctor] = useState<Resource<DoctorReport>>({ state: "loading" });
  const [configuration, setConfiguration] = useState<Resource<EffectiveConfiguration>>({ state: "loading" });
  const [projects, setProjects] = useState<Resource<Project[]>>({ state: "loading" });
  const [refreshVersion, setRefreshVersion] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  const [notice, setNotice] = useState("");
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);
  const registerDialog = useRef<HTMLDialogElement>(null);

  useEffect(() => setRootDraft(projectRoot), [projectRoot]);
  useEffect(() => {
    const abort = new AbortController();
    setRefreshing(true);
    const context = projectRoot || undefined;
    const requests = [
      apiClient.getHealth(abort.signal).then((value) => { if (!abort.signal.aborted) setHealth({ state: "ready", value }); }).catch((cause) => { if (!abort.signal.aborted) setHealth({ state: "error", message: settingsError(cause, "Daemon liveness") }); }),
      apiClient.getDoctorReport(context, abort.signal).then((value) => { if (!abort.signal.aborted) setDoctor({ state: "ready", value: decodeDoctorReport(value) }); }).catch((cause) => { if (!abort.signal.aborted) setDoctor({ state: "error", message: settingsError(cause, "Subsystem diagnostics") }); }),
      apiClient.getEffectiveConfiguration(context, abort.signal).then((value) => { if (!abort.signal.aborted) setConfiguration({ state: "ready", value: decodeEffectiveConfiguration(value) }); }).catch((cause) => { if (!abort.signal.aborted) setConfiguration({ state: "error", message: settingsError(cause, "Effective configuration") }); }),
      apiClient.listProjects(abort.signal).then((value) => { if (!abort.signal.aborted) setProjects({ state: "ready", value: sortProjects(value) }); }).catch((cause) => { if (!abort.signal.aborted) setProjects({ state: "error", message: settingsError(cause, "Project registry") }); }),
    ];
    void Promise.allSettled(requests).then(() => { if (!abort.signal.aborted) { setRefreshing(false); if (refreshVersion > 0) setNotice("Settings and health refreshed from the local API."); } });
    return () => abort.abort();
  }, [projectRoot, refreshVersion]);

  function setTab(next: SettingsTab) {
    const nextParams = new URLSearchParams(params);
    nextParams.set("tab", next);
    navigate(`/settings?${nextParams}`);
  }

  function onTabKeyDown(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    let target: number | undefined;
    if (event.key === "ArrowRight") target = (index + 1) % tabs.length;
    if (event.key === "ArrowLeft") target = (index - 1 + tabs.length) % tabs.length;
    if (event.key === "Home") target = 0;
    if (event.key === "End") target = tabs.length - 1;
    if (target === undefined) return;
    event.preventDefault();
    tabRefs.current[target]?.focus();
    setTab(tabs[target].id);
  }

  function applyProjectContext(event: FormEvent) {
    event.preventDefault();
    const next = new URLSearchParams(params);
    rootDraft.trim() ? next.set("projectRoot", rootDraft.trim()) : next.delete("projectRoot");
    navigate(`/settings${next.size ? `?${next}` : ""}`);
  }

  return <div className="page settings-page">
    <header className="page-header settings-header">
      <div><p className="eyebrow">Local control plane</p><h1>Settings &amp; Health</h1><p className="page-header__description">Inspect daemon readiness, provider capability, registered projects, and source-attributed effective configuration. Remediation text is guidance, never an executable dashboard action.</p></div>
      <button type="button" className="button button--primary" disabled={refreshing} onClick={() => { setNotice(""); setRefreshVersion((value) => value + 1); }}>{refreshing ? "Refreshing…" : "Refresh checks"}</button>
    </header>
    {notice && <p className="detail-action-message" role="status">{notice}</p>}
    <form className="settings-context" aria-label="Project diagnostic context" onSubmit={applyProjectContext}>
      <label><span>Project root <small>optional</small></span><input value={rootDraft} onChange={(event) => setRootDraft(event.target.value)} placeholder="Use daemon startup context" /></label>
      <button type="submit" className="button">Inspect context</button>
      {projectRoot && <button type="button" className="button" onClick={() => { setRootDraft(""); const next = new URLSearchParams(params); next.delete("projectRoot"); navigate(`/settings${next.size ? `?${next}` : ""}`); }}>Use daemon context</button>}
      <p>The path is sent only to authenticated read-only doctor and configuration queries. Registered projects retain a source fingerprint, not this location.</p>
    </form>
    <SettingsSummary health={health} doctor={doctor} connection={state.connection} />
    <div className="checkpoint-tabs settings-tabs" role="tablist" aria-label="Settings workspace">{tabs.map((item, index) => <button key={item.id} ref={(value) => { tabRefs.current[index] = value; }} id={`settings-tab-${item.id}`} type="button" role="tab" tabIndex={tab === item.id ? 0 : -1} aria-controls={`settings-panel-${item.id}`} aria-selected={tab === item.id} onKeyDown={(event) => onTabKeyDown(event, index)} onClick={() => setTab(item.id)}>{item.label}</button>)}</div>
	{tabs.map((item) => <div key={item.id} id={`settings-panel-${item.id}`} className="settings-panel" role="tabpanel" tabIndex={item.id === tab ? 0 : -1} aria-labelledby={`settings-tab-${item.id}`} aria-busy={refreshing} hidden={item.id !== tab}>
	  {item.id === "health" && <HealthPanel resource={doctor} />}
	  {item.id === "provider" && <ProviderPanel resource={doctor} />}
	  {item.id === "projects" && <ProjectsPanel resource={projects} onRegister={() => registerDialog.current?.showModal()} />}
	  {item.id === "configuration" && <ConfigurationPanel resource={configuration} />}
	</div>)}
    <RegisterProjectDialog refValue={registerDialog} onRegistered={() => { setNotice("Project registration was durably accepted by the local API."); setRefreshVersion((value) => value + 1); }} />
  </div>;
}

function SettingsSummary({ health, doctor, connection }: { health: Resource<Health>; doctor: Resource<DoctorReport>; connection: string }) {
  const recovery = health.state === "ready" ? health.value.recovery : undefined;
  const doctorStatus = doctor.state === "ready" ? doctor.value.status : doctor.state;
  return <section className="settings-summary" aria-label="Control plane summary">
    <Summary label="Daemon API" value={health.state === "ready" ? "Reachable" : health.state === "error" ? "Unavailable" : "Checking"} tone={health.state === "ready" ? "healthy" : health.state === "error" ? "unhealthy" : "loading"} />
    <Summary label="Subsystems" value={humanize(doctorStatus)} tone={doctorStatus} />
    <Summary label="Event stream" value={humanize(connection)} tone={connection === "live" ? "healthy" : connection === "offline" ? "unhealthy" : "degraded"} />
    <Summary label="Recovery" value={recovery ? (recovery.reconcileRequired ? `${recovery.reconcileRequired} unresolved` : `${recovery.reconciled} checked`) : "Checking"} tone={recovery?.reconcileRequired ? "unhealthy" : recovery ? "healthy" : "loading"} />
  </section>;
}

function Summary({ label, value, tone }: { label: string; value: string; tone: string }) {
  return <article data-tone={tone}><span>{label}</span><strong>{value}</strong></article>;
}

function HealthPanel({ resource }: { resource: Resource<DoctorReport> }) {
  if (resource.state === "loading") return <DetailLoading label="Running subsystem diagnostics" />;
  if (resource.state === "error") return <DetailFailure title="Subsystem diagnostics unavailable" message={resource.message} />;
  return <section aria-labelledby="health-heading"><div className="settings-section-heading"><div><p className="eyebrow">Point-in-time diagnostics</p><h2 id="health-heading">Eight required subsystems</h2></div><span>Generated {formatDate(resource.value.generatedAt)}</span></div><div className="health-grid">{resource.value.checks.map((check) => <HealthCard key={check.subsystem} check={check} />)}</div></section>;
}

function HealthCard({ check }: { check: HealthCheck }) {
  return <article className="health-card" data-status={check.status}><header><div><span>{subsystemLabels[check.subsystem]}</span><code>{check.code}</code></div><strong>{humanize(check.status)}</strong></header><p>{check.message}</p>{check.action && <aside><span>Recommended action</span><p>{check.action}</p></aside>}</article>;
}

function ProviderPanel({ resource }: { resource: Resource<DoctorReport> }) {
  if (resource.state === "loading") return <DetailLoading label="Inspecting provider readiness" />;
  if (resource.state === "error") return <DetailFailure title="Provider diagnostics unavailable" message={resource.message} />;
  const checks = resource.value.checks.filter((check) => check.subsystem === "codex" || check.subsystem === "provider");
  return <section aria-labelledby="provider-heading"><div className="settings-section-heading"><div><p className="eyebrow">Credential-free inspection</p><h2 id="provider-heading">Codex &amp; selected provider</h2></div></div><p className="settings-boundary">Account identifiers, tokens, balances, and raw provider responses are never part of this projection.</p><div className="provider-grid">{checks.map((check) => <article className="provider-card" key={check.subsystem}><HealthCard check={check} />{check.providerDetails ? <ProviderFacts details={check.providerDetails} /> : <p className="settings-empty">No safe provider details were reported for this check.</p>}</article>)}</div></section>;
}

function ProviderFacts({ details }: { details: ProviderDetails }) {
  return <div className="provider-facts"><dl><Fact label="Name" value={details.name} /><Fact label="Version" value={details.version || "Not reported"} /><Fact label="Authentication" value={humanize(details.authentication)} /><Fact label="Usage" value={humanize(details.usage)} /><Fact label="Platform" value={details.platform || "Not reported"} /><Fact label="Executable" value={details.executableIdentity || "Not reported"} mono /></dl><TokenList title="Instruction sources" values={details.instructionSources} />{details.conflictingExecutables.length > 0 && <TokenList title="Conflicting executables" values={details.conflictingExecutables} warning />}<div className="capability-grid"><CapabilityList title="Available capabilities" values={details.availableCapabilities.map((value) => ({ name: value.name, detail: value.version }))} /><CapabilityList title="Unavailable capabilities" values={details.unavailableCapabilities.map((value) => ({ name: value.name, detail: value.reason }))} warning /></div></div>;
}

function ProjectsPanel({ resource, onRegister }: { resource: Resource<Project[]>; onRegister(): void }) {
  if (resource.state === "loading") return <DetailLoading label="Loading registered projects" />;
  if (resource.state === "error") return <DetailFailure title="Project registry unavailable" message={resource.message} />;
  return <section aria-labelledby="projects-heading"><div className="settings-section-heading"><div><p className="eyebrow">Durable registry</p><h2 id="projects-heading">Registered projects</h2></div><button type="button" className="button button--primary" onClick={onRegister}>Register project</button></div><p className="settings-boundary">Registration is the only settings mutation available here because it maps directly to the public project API and its idempotent CLI command.</p>{resource.value.length ? <div className="project-settings-list">{resource.value.map((project) => <article key={project.id}><header><div><strong>{project.name}</strong><code>{project.id}</code></div><span data-status={project.status}>{humanize(project.status)}</span></header><dl><Fact label="Source fingerprint" value={project.sourceHash} mono /><Fact label="Resource version" value={String(project.resourceVersion)} /><Fact label="Registered" value={formatDate(project.createdAt)} /><Fact label="Updated" value={formatDate(project.updatedAt)} /></dl></article>)}</div> : <div className="settings-empty-panel"><strong>No projects registered</strong><p>Register an existing local project root before creating work.</p><button type="button" className="button" onClick={onRegister}>Register project</button></div>}</section>;
}

function ConfigurationPanel({ resource }: { resource: Resource<EffectiveConfiguration> }) {
  if (resource.state === "loading") return <DetailLoading label="Resolving effective configuration" />;
  if (resource.state === "error") return <DetailFailure title="Effective configuration unavailable" message={resource.message} />;
  return <section aria-labelledby="configuration-heading"><div className="settings-section-heading"><div><p className="eyebrow">Read-only resolved values</p><h2 id="configuration-heading">Effective configuration</h2></div><code>{resource.value.projectRoot}</code></div><div className="settings-boundary"><strong>Precedence</strong><span>Command line → run/work → project → user → shipped defaults</span><p>Only the winning source for each leaf appears below. The separate secrets file is never loaded by this endpoint, and sensitive-looking values are withheld.</p></div><div className="config-files" aria-label="Configuration files">{resource.value.files.map((file) => <article key={file.scope}><span>{humanize(file.scope)}</span><code>{file.path}</code></article>)}</div>{resource.value.entries.length ? <div className="config-entries">{resource.value.entries.map((entry) => <article key={entry.path} data-kind={entry.value.kind}><header><code>{entry.path}</code><span>{humanize(entry.source.scope)}</span></header><pre>{entry.value.display}</pre><small>{entry.value.kind === "redacted" ? "Value withheld" : humanize(entry.value.kind)} · {entry.source.reference}</small></article>)}</div> : <div className="settings-empty-panel"><strong>No effective values</strong><p>User and project configuration files contain no resolved leaves; shipped defaults are currently empty.</p></div>}</section>;
}

function RegisterProjectDialog({ refValue, onRegistered }: { refValue: RefObject<HTMLDialogElement | null>; onRegistered(): void }) {
  const [name, setName] = useState(""); const [source, setSource] = useState(""); const [submitting, setSubmitting] = useState(false); const [error, setError] = useState("");
  const key = useRef(`dashboard-project-register-${crypto.randomUUID()}`);
  function close() { if (!submitting) { refValue.current?.close(); setError(""); } }
  async function submit(event: FormEvent) { event.preventDefault(); let body: Schemas["ProjectRegistration"]; try { body = normalizeProjectRegistration(name, source); } catch (cause) { setError(cause instanceof Error ? cause.message : "Project registration is invalid."); return; } setSubmitting(true); setError(""); try { await apiClient.registerProject(body, key.current); refValue.current?.close(); setName(""); setSource(""); key.current = `dashboard-project-register-${crypto.randomUUID()}`; onRegistered(); } catch (cause) { setError(cause instanceof ApiRequestError && cause.status === 400 ? "Project registration requires an existing absolute directory visible to the local daemon." : settingsError(cause, "Project registration")); } finally { setSubmitting(false); } }
  return <dialog ref={refValue} className="work-dialog project-register-dialog" aria-labelledby="project-register-title" aria-describedby="project-register-description" onCancel={(event) => { if (submitting) event.preventDefault(); }} onClose={() => { if (!submitting) setError(""); }}><form aria-busy={submitting} onSubmit={(event) => void submit(event)}><header className="work-dialog__header"><div><p className="eyebrow">Idempotent API command</p><h2 id="project-register-title">Register project</h2></div><button type="button" className="icon-button" aria-label="Close project registration" onClick={close}>×</button></header><p id="project-register-description" className="work-dialog__intro">Register an existing absolute project directory. DARKSTAR persists only its source fingerprint in the project projection.</p><label className="field"><span>Project name</span><input required autoFocus value={name} onChange={(event) => setName(event.target.value)} placeholder="Software Factory" /></label><label className="field"><span>Absolute project path</span><input required value={source} onChange={(event) => setSource(event.target.value)} placeholder="C:\\source\\project" /></label>{error && <p className="form-error" role="alert">{error}</p>}<footer className="work-dialog__footer"><button type="button" className="button" disabled={submitting} onClick={close}>Back</button><button type="submit" className="button button--primary" disabled={submitting}>{submitting ? "Registering…" : "Register project"}</button></footer></form></dialog>;
}

function Fact({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) { return <div><dt>{label}</dt><dd className={mono ? "mono" : undefined}>{value}</dd></div>; }
function TokenList({ title, values, warning = false }: { title: string; values: string[]; warning?: boolean }) { return <section className="settings-token-list" data-warning={warning}><h3>{title}</h3>{values.length ? <ul>{values.map((value) => <li key={value}><code>{value}</code></li>)}</ul> : <p>None reported</p>}</section>; }
function CapabilityList({ title, values, warning = false }: { title: string; values: Array<{ name: string; detail: string }>; warning?: boolean }) { return <section className="capability-list" data-warning={warning}><h3>{title}</h3>{values.length ? <ul>{values.map((value) => <li key={value.name}><strong>{value.name}</strong><span>{value.detail}</span></li>)}</ul> : <p>None reported</p>}</section>; }
function humanize(value: string) { return value.replaceAll("_", " ").replace(/\b\w/g, (match) => match.toUpperCase()); }
function settingsError(cause: unknown, resource: string) { if (cause instanceof ApiRequestError && cause.status === 400) return `${resource} rejected the selected project context.`; if (cause instanceof ApiRequestError && cause.status === 503) return `${resource} is not configured or temporarily unavailable.`; if (cause instanceof Error && !(cause instanceof ApiRequestError)) return `${resource} returned data outside the supported contract.`; return `${resource} could not be loaded from the local API.`; }
