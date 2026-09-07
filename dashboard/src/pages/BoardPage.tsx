import { useEffect, useMemo, useRef, useState, type FormEvent, type RefObject } from "react";

import { apiClient, ApiRequestError } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink, useRouter } from "../app/router";
import { Icon } from "../components/Icon";
import { ActionGuidance, AsyncPanel, EmptyState } from "../components/InteractionPatterns";
import { PageHeader } from "../components/PageStructure";
import { useDashboardState } from "../state/DashboardStateProvider";
import {
  LIFECYCLE_COLUMNS,
  availableCardActions,
  buildCreateWorkItemRequest,
  buildPrepareRunRequest,
  buildWorkTransitionRequest,
  deriveBoardCards,
  filterBoardCards,
  transitionTargetForAction,
  workflowProfiles,
  type BoardCard,
  type BoardCardAction,
  type BoardLifecycle,
  type BoardView,
  type WorkflowProfileOption,
} from "./boardModel";

type Schemas = components["schemas"];

const lifecycleLabels: Record<BoardLifecycle, string> = {
  backlog: "Backlog", ready: "Ready", running: "Running", waiting: "Waiting",
  blocked: "Blocked", review: "Review", failed: "Failed", done: "Done",
};
const actionLabels: Record<BoardCardAction, string> = {
  prepare: "Prepare run", launch: "Start run", pause: "Pause", resume: "Resume", retry: "Retry", cancel: "Cancel",
};

type WorkflowProfileState =
  | { kind: "idle" }
  | { kind: "loading"; selection: string }
  | { kind: "ready"; selection: string; profiles: WorkflowProfileOption[] }
  | { kind: "error"; selection: string };

export function BoardPage() {
  const { state, refresh } = useDashboardState();
  const { search } = useRouter();
  const createDialog = useRef<HTMLDialogElement>(null);
  const prepareDialog = useRef<HTMLDialogElement>(null);
  const [prepareCard, setPrepareCard] = useState<BoardCard>();
  const [workflows, setWorkflows] = useState<Schemas["WorkflowVersionSummary"][]>([]);
  const [view, setView] = useState<BoardView>("all");
  const [projectId, setProjectId] = useState("");
  const [workflowId, setWorkflowId] = useState("");
  const [query, setQuery] = useState("");
  const [pendingAction, setPendingAction] = useState("");
  const [actionMessage, setActionMessage] = useState<{ kind: "success" | "error"; text: string }>();
  const [transitionPlans, setTransitionPlans] = useState<Record<string, Schemas["WorkTransitionPlan"]>>({});

  const allCards = useMemo(() => deriveBoardCards(state.snapshot), [state.snapshot]);
  const cards = useMemo(
    () => filterBoardCards(allCards, { projectId: projectId || undefined, workflowId: workflowId || undefined, query, view }),
    [allCards, projectId, query, view, workflowId],
  );
  const workflowNames = useMemo(
    () => [...new Set([...workflows.map((workflow) => workflow.name), ...state.snapshot.runs.map((run) => run.workflowId)])].sort((left, right) => left.localeCompare(right)),
    [state.snapshot.runs, workflows],
  );
  const counts = useMemo(() => countByLifecycle(cards), [cards]);

  useEffect(() => {
    if (new URLSearchParams(search).get("create") === "1" && !createDialog.current?.open) createDialog.current?.showModal();
  }, [search]);
  useEffect(() => {
    let live = true;
    void apiClient.listWorkflows().then((values) => { if (live) setWorkflows(values); }).catch(() => undefined);
    return () => { live = false; };
  }, [state.lastSynchronizedAt]);
  useEffect(() => {
    let live = true;
    void Promise.all(allCards.map(async (card) => [card.work.id, await apiClient.planWorkItemTransition(card.work.id, "running")] as const))
      .then((entries) => { if (live) setTransitionPlans(Object.fromEntries(entries)); })
      .catch(() => { if (live) setTransitionPlans({}); });
    return () => { live = false; };
  }, [allCards, state.lastSynchronizedAt]);

  function beginPrepare(card: BoardCard) {
    setPrepareCard(card);
    window.setTimeout(() => prepareDialog.current?.showModal(), 0);
  }

  async function runCardAction(card: BoardCard, action: BoardCardAction) {
    if (action === "prepare") return beginPrepare(card);
    const plan = transitionPlans[card.work.id];
    if (!plan) return;
    if (action === "cancel" && !window.confirm(`Cancel “${card.work.title}”? Its run history and evidence will be preserved.`)) return;
    const key = `${action}:${card.work.id}`;
    setPendingAction(key);
    setActionMessage(undefined);
    try {
      const idempotencyKey = `dashboard-${action}-${crypto.randomUUID()}`;
      const target = transitionTargetForAction(action);
      await apiClient.applyWorkItemTransition(card.work.id, plan.resourceVersion, idempotencyKey, buildWorkTransitionRequest("menu", target));
      await refresh();
      setActionMessage({ kind: "success", text: `${actionLabels[action]} requested. The board now reflects daemon state.` });
    } catch (error) {
      if (error instanceof ApiRequestError && error.workTransitionPlan) setTransitionPlans((current) => ({ ...current, [card.work.id]: error.workTransitionPlan! }));
      await refresh().catch(() => undefined);
      setActionMessage({ kind: "error", text: safeActionError(error) });
    } finally {
      setPendingAction("");
    }
  }

  const loading = state.hydration === "loading";
  const filtered = Boolean(projectId || workflowId || query || view === "attention");
  const canCreate = state.snapshot.projects.some((project) => project.status === "active");
  return (
    <div className="page page--board">
      <PageHeader className="board-page-header" eyebrow="Operational workspace" title="Lifecycle board" description="Live, server-authoritative work from intake through verified delivery." breadcrumbs={[{ label: "Board" }]} actions={<button className="button button--primary" type="button" aria-describedby={!canCreate ? "create-work-guidance" : undefined} onClick={() => createDialog.current?.showModal()} disabled={!canCreate}><Icon name="create" />Create work</button>} />
      {!canCreate && <ActionGuidance id="create-work-guidance">Create Work is unavailable until an active project is registered.</ActionGuidance>}

      <div className="board-toolbar" aria-label="Board controls">
        <div className="view-tabs" aria-label="Board view">
          <button className="view-tab" type="button" aria-pressed={view === "all"} onClick={() => setView("all")}>All work <span>{allCards.length}</span></button>
          <button className="view-tab" type="button" aria-pressed={view === "attention"} onClick={() => setView("attention")}>Needs attention</button>
        </div>
        <div className="board-filters">
          <label><span className="sr-only">Search work</span><span className="board-search"><Icon name="search" /><input value={query} onChange={(event) => setQuery(event.target.value)} placeholder="Search work…" /></span></label>
          <label><span className="sr-only">Filter by project</span><select value={projectId} onChange={(event) => setProjectId(event.target.value)}><option value="">All projects</option>{state.snapshot.projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label>
          <label><span className="sr-only">Filter by workflow</span><select value={workflowId} onChange={(event) => setWorkflowId(event.target.value)}><option value="">All workflows</option>{workflowNames.map((workflow) => <option key={workflow} value={workflow}>{workflow}</option>)}</select></label>
          {filtered && <button className="filter-reset" type="button" onClick={() => { setProjectId(""); setWorkflowId(""); setQuery(""); setView("all"); }}>Clear</button>}
        </div>
      </div>

      {state.hydration === "error" && <AsyncPanel compact state="error" title="Board data is unavailable" message={state.message} action={<button className="button" type="button" onClick={() => void refresh()}>Retry</button>} />}
      {pendingAction && <AsyncPanel compact state="loading" title="Run command pending" message={<>Updating run <code>{pendingAction.split(":").slice(1).join(":")}</code> through the local API.</>} />}
      {actionMessage && <AsyncPanel compact state={actionMessage.kind} title={actionMessage.kind === "success" ? "Command accepted" : "Command failed"} message={actionMessage.text} />}

      <section className="board-preview" aria-label="Work lifecycle" aria-busy={loading || state.hydration === "refreshing"}>
        {LIFECYCLE_COLUMNS.map((lifecycle) => <BoardColumn key={lifecycle} lifecycle={lifecycle} cards={cards.filter((card) => card.lifecycle === lifecycle)} count={counts[lifecycle]} loading={loading} pendingAction={pendingAction} plans={transitionPlans} onAction={runCardAction} onCreate={() => createDialog.current?.showModal()} />)}
      </section>

      {!loading && cards.length === 0 && filtered && <EmptyState compact kind="filtered" title="No work matches these filters" message="The active project, workflow, search, or attention filters exclude every work item." action={<button type="button" className="button" onClick={() => { setProjectId(""); setWorkflowId(""); setQuery(""); setView("all"); }}>Clear filters</button>} />}
      {!loading && !canCreate && <p className="board-setup-note">Register an active project with <code>darkstar project add</code> before creating work.</p>}
      <CreateWorkDialog dialogRef={createDialog} projects={state.snapshot.projects} onCreated={refresh} />
      <PrepareRunDialog dialogRef={prepareDialog} card={prepareCard} workflows={workflows} onRefresh={refresh} onPrepared={() => setActionMessage({ kind: "success", text: "Run prepared and ready to start. The board now reflects daemon state." })} />
    </div>
  );
}

function BoardColumn({ lifecycle, cards, count, loading, pendingAction, plans, onAction, onCreate }: { lifecycle: BoardLifecycle; cards: BoardCard[]; count: number; loading: boolean; pendingAction: string; plans: Record<string, Schemas["WorkTransitionPlan"]>; onAction(card: BoardCard, action: BoardCardAction): Promise<void>; onCreate(): void }) {
  return <article className="board-column" data-lifecycle={lifecycle}>
    <header className="board-column__header"><span className={`state-dot state-dot--${lifecycle}`} aria-hidden="true" /><h2>{lifecycleLabels[lifecycle]}</h2><span className="board-column__count" aria-label={`${count} work items`}>{count}</span></header>
    <div className="board-column__cards">
      {loading && [0, 1].map((key) => <div className="work-card work-card--loading" key={key} aria-hidden="true"><span /><span /><span /></div>)}
      {!loading && cards.map((card) => <WorkCard key={card.work.id} card={card} plan={plans[card.work.id]} pendingAction={pendingAction} onAction={onAction} />)}
      {!loading && cards.length === 0 && lifecycle === "backlog" && <button className="board-empty-card" type="button" onClick={onCreate}><span className="board-empty-card__icon"><Icon name="spark" /></span><strong>No backlog work</strong><span>Create a work item to begin.</span></button>}
      {!loading && cards.length === 0 && lifecycle !== "backlog" && <div className="board-column__empty"><span>No {lifecycleLabels[lifecycle].toLowerCase()} work</span></div>}
    </div>
  </article>;
}

function WorkCard({ card, plan, pendingAction, onAction }: { card: BoardCard; plan?: Schemas["WorkTransitionPlan"]; pendingAction: string; onAction(card: BoardCard, action: BoardCardAction): Promise<void> }) {
  const actions = availableCardActions(card, plan);
  const projectName = card.project?.name ?? "Unknown project";
  return <article className="work-card">
    <div className="work-card__project"><span aria-hidden="true">{initials(projectName)}</span><span>{projectName}</span></div>
    <AppLink className="work-card__title" to={`/work/${encodeURIComponent(card.work.id)}`}>{card.work.title}</AppLink>
    <div className="work-card__metadata"><span title={card.work.id}>{compactId(card.work.id)}</span><span>Priority {card.work.priority}</span></div>
    {card.run ? <div className="work-card__run"><AppLink to={`/work/${encodeURIComponent(card.work.id)}/run/${encodeURIComponent(card.run.id)}`}>{card.run.workflowId} · v{card.run.workflowVersion}</AppLink><span className={`run-status run-status--${card.lifecycle}`}><span aria-hidden="true" />{humanize(card.run.status)}</span></div> : <p className="work-card__unrouted">Route not selected</p>}
    <div className="work-card__actions" aria-label={`Actions for ${card.work.title}`}><AppLink className="card-action" to={`/artifacts?targetKind=work&targetId=${encodeURIComponent(card.work.id)}&ingest=1`}>Add evidence</AppLink>{actions.map((action) => {
      const key = `${action}:${card.work.id}`;
      const pending = pendingAction === key;
      return <button key={action} type="button" className={action === "cancel" ? "card-action card-action--danger" : "card-action"} disabled={Boolean(pendingAction)} onClick={() => void onAction(card, action)}>{pending ? "Working…" : actionLabels[action]}</button>;
    })}</div>
  </article>;
}

function CreateWorkDialog({ dialogRef, projects, onCreated }: { dialogRef: RefObject<HTMLDialogElement | null>; projects: Schemas["Project"][]; onCreated(): Promise<void> }) {
  const { navigate } = useRouter();
  const activeProjects = projects.filter((project) => project.status === "active");
  const [projectId, setProjectId] = useState(""); const [title, setTitle] = useState(""); const [priority, setPriority] = useState("0");
  const [submitting, setSubmitting] = useState(false); const [error, setError] = useState("");
  useEffect(() => { if (!projectId && activeProjects.length === 1) setProjectId(activeProjects[0].id); }, [activeProjects, projectId]);
  function close() { if (!submitting) { dialogRef.current?.close(); if (new URLSearchParams(window.location.search).has("create")) navigate("/board", { replace: true }); } }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setError(""); setSubmitting(true);
    try { const body = buildCreateWorkItemRequest({ projectId, title, priority: Number(priority) }); await apiClient.createWorkItem(body, `dashboard-create-work-${crypto.randomUUID()}`); await onCreated(); setTitle(""); setPriority("0"); dialogRef.current?.close(); }
    catch (cause) { setError(cause instanceof Error && !(cause instanceof ApiRequestError) ? cause.message : safeActionError(cause)); }
    finally { setSubmitting(false); }
  }
  return <dialog ref={dialogRef} className="work-dialog" onCancel={(event) => { event.preventDefault(); close(); }} onClose={() => { if (new URLSearchParams(window.location.search).has("create")) navigate("/board", { replace: true }); }}><form aria-busy={submitting} onSubmit={(event) => void submit(event)}>
    <header className="work-dialog__header"><div><p className="eyebrow">New work item</p><h2>Create requested outcome</h2></div><button className="icon-button" type="button" aria-label="Close create work dialog" disabled={submitting} onClick={close}><Icon name="x" /></button></header>
    <p className="work-dialog__intro">Create authored work in a registered project. Route selection happens from the durable work record.</p>
    <label className="field"><span>Project</span><select required autoFocus value={projectId} onChange={(event) => setProjectId(event.target.value)}><option value="" disabled>Choose a project</option>{activeProjects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label>
    <label className="field"><span>Requested outcome</span><textarea required rows={4} maxLength={500} value={title} onChange={(event) => setTitle(event.target.value)} placeholder="Describe the result you want DARKSTAR to deliver" /><small>{title.length}/500</small></label>
    <label className="field field--priority"><span>Priority</span><input required type="number" inputMode="numeric" min="0" step="1" value={priority} onChange={(event) => setPriority(event.target.value)} /><small>Higher values are scheduled first.</small></label>
    {error && <p className="form-error" role="alert">{error}</p>}
    <footer className="work-dialog__footer"><p className="dialog-draft-note">Closing discards unsaved changes.</p><button className="button" type="button" disabled={submitting} onClick={close}>Cancel</button><button className="button button--primary" type="submit" disabled={submitting || activeProjects.length === 0}>{submitting ? "Creating…" : "Create work"}</button></footer>
  </form></dialog>;
}

function PrepareRunDialog({ dialogRef, card, workflows, onRefresh, onPrepared }: { dialogRef: RefObject<HTMLDialogElement | null>; card?: BoardCard; workflows: Schemas["WorkflowVersionSummary"][]; onRefresh(): Promise<void>; onPrepared(): void }) {
  const [selection, setSelection] = useState(""); const [profile, setProfile] = useState(""); const [profileState, setProfileState] = useState<WorkflowProfileState>({ kind: "idle" });
  const [profileReload, setProfileReload] = useState(0);
  const [submitting, setSubmitting] = useState(false); const [error, setError] = useState("");
  function close() { if (!submitting) { dialogRef.current?.close(); setError(""); } }
  useEffect(() => { if (!selection && workflows.length === 1) setSelection(`${workflows[0].name}\u0000${workflows[0].version}`); }, [selection, workflows]);
  useEffect(() => {
    setProfile("");
    if (!selection) { setProfileState({ kind: "idle" }); return; }
    const [workflowId, workflowVersion] = selection.split("\u0000");
    if (!workflowId || !workflowVersion) { setProfileState({ kind: "error", selection }); return; }
    const controller = new AbortController();
    setProfileState({ kind: "loading", selection });
    void apiClient.showWorkflow(workflowId, workflowVersion, controller.signal)
      .then((definition) => { if (!controller.signal.aborted) setProfileState({ kind: "ready", selection, profiles: workflowProfiles(definition) }); })
      .catch(() => { if (!controller.signal.aborted) setProfileState({ kind: "error", selection }); });
    return () => controller.abort();
  }, [profileReload, selection]);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (!card) return; const [workflowId, workflowVersion] = selection.split("\u0000"); if (!workflowId || !workflowVersion) return setError("Choose a workflow version.");
    setSubmitting(true); setError("");
    try {
      const body = buildPrepareRunRequest({ workItemId: card.work.id, workflowId, workflowVersion, profile });
      const preparation: Schemas["WorkTransitionPreparation"] = { workflowId: body.workflowId, workflowVersion: body.workflowVersion, ...(body.profile ? { profile: body.profile } : {}) };
      const plan = await apiClient.planWorkItemTransition(card.work.id, "ready", preparation);
      await apiClient.applyWorkItemTransition(card.work.id, plan.resourceVersion, `dashboard-transition-ready-${crypto.randomUUID()}`, buildWorkTransitionRequest("menu", "ready", preparation));
      await onRefresh(); onPrepared(); dialogRef.current?.close();
    }
    catch (cause) { setError(safeActionError(cause)); await onRefresh().catch(() => undefined); }
    finally { setSubmitting(false); }
  }
  return <dialog ref={dialogRef} className="work-dialog work-dialog--compact" onCancel={(event) => { if (submitting) event.preventDefault(); }} onClose={() => { if (!submitting) setError(""); }}><form aria-busy={submitting} onSubmit={(event) => void submit(event)}>
    <header className="work-dialog__header"><div><p className="eyebrow">Prepare run</p><h2>{card?.work.title ?? "Choose a workflow"}</h2></div><button className="icon-button" type="button" aria-label="Close prepare run dialog" disabled={submitting} onClick={close}><Icon name="x" /></button></header>
    <p className="work-dialog__intro">Select the installed workflow route to validate and move this work to Ready. Starting remains a separate action.</p>
    <label className="field"><span>Workflow version</span><select required autoFocus value={selection} onChange={(event) => setSelection(event.target.value)}><option value="" disabled>Choose a workflow</option>{workflows.map((workflow) => <option key={`${workflow.name}:${workflow.version}:${workflow.digest}`} value={`${workflow.name}\u0000${workflow.version}`}>{workflow.name} · {workflow.version} ({workflow.sourceScope})</option>)}</select></label>
    {profileState.kind === "loading" && <p role="status">Loading workflow profiles…</p>}
    {profileState.kind === "ready" && profileState.selection === selection && <label className="field"><span>Workflow profile</span><select value={profile} onChange={(event) => setProfile(event.target.value)}><option value="">Default route</option>{profileState.profiles.map((option) => <option key={option.id} value={option.id}>{option.id}{option.description ? ` — ${option.description}` : ""}</option>)}</select><small>Profiles choose an authored route and its declared input defaults.</small></label>}
    {profileState.kind === "error" && profileState.selection === selection && <div><p className="form-error" role="alert">The selected workflow definition could not be loaded. Retry after checking daemon health.</p><button className="button" type="button" onClick={() => setProfileReload((value) => value + 1)}>Retry profile load</button></div>}
    {workflows.length === 0 && <p className="form-error" role="status">No installed workflows are available. Install one with the CLI first.</p>}{error && <p className="form-error" role="alert">{error}</p>}
    <footer className="work-dialog__footer"><p className="dialog-draft-note">Closing discards unsaved changes.</p><button className="button" type="button" disabled={submitting} onClick={close}>Cancel</button><button className="button button--primary" type="submit" disabled={submitting || workflows.length === 0 || profileState.kind !== "ready" || profileState.selection !== selection}>{submitting ? "Preparing…" : "Move to Ready"}</button></footer>
  </form></dialog>;
}

function countByLifecycle(cards: readonly BoardCard[]) { const counts = Object.fromEntries(LIFECYCLE_COLUMNS.map((lifecycle) => [lifecycle, 0])) as Record<BoardLifecycle, number>; for (const card of cards) counts[card.lifecycle] += 1; return counts; }
function safeActionError(error: unknown) { if (error instanceof ApiRequestError) { if (error.status === 409 || error.status === 412) return "This item changed before the action completed. The board was refreshed; try again."; if (error.status === 400) return "The daemon rejected this action because it is not valid for the current state."; } return "The action could not be completed. Check daemon health and try again."; }
function compactId(value: string) { return value.length < 16 ? value : `${value.slice(0, 8)}…${value.slice(-4)}`; }
function initials(value: string) { return value.split(/\s+/).filter(Boolean).slice(0, 2).map((part) => part[0]).join("").toUpperCase(); }
function humanize(value: string) { return value.replaceAll("_", " ").replace(/^./, (character) => character.toUpperCase()); }
