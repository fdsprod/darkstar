import { useEffect, useMemo, useRef, useState, type FormEvent, type RefObject } from "react";

import { apiClient, ApiRequestError } from "../api/client";
import type { components } from "../api/schema.generated";
import { AppLink, useRouter } from "../app/router";
import { Icon } from "../components/Icon";
import { useDashboardState } from "../state/DashboardStateProvider";
import {
  LIFECYCLE_COLUMNS,
  availableCardActions,
  buildCreateWorkItemRequest,
  deriveBoardCards,
  filterBoardCards,
  type BoardCard,
  type BoardCardAction,
  type BoardLifecycle,
  type BoardView,
} from "./boardModel";

type Schemas = components["schemas"];

const lifecycleLabels: Record<BoardLifecycle, string> = {
  backlog: "Backlog", ready: "Ready", running: "Running", waiting: "Waiting",
  blocked: "Blocked", review: "Review", failed: "Failed", done: "Done",
};
const actionLabels: Record<Exclude<BoardCardAction, "start">, string> = {
  pause: "Pause", resume: "Resume", retry: "Retry", cancel: "Cancel",
};

export function BoardPage() {
  const { state, refresh } = useDashboardState();
  const { search } = useRouter();
  const createDialog = useRef<HTMLDialogElement>(null);
  const startDialog = useRef<HTMLDialogElement>(null);
  const [startCard, setStartCard] = useState<BoardCard>();
  const [workflows, setWorkflows] = useState<Schemas["WorkflowVersionSummary"][]>([]);
  const [view, setView] = useState<BoardView>("all");
  const [projectId, setProjectId] = useState("");
  const [workflowId, setWorkflowId] = useState("");
  const [query, setQuery] = useState("");
  const [pendingAction, setPendingAction] = useState("");
  const [actionMessage, setActionMessage] = useState<{ kind: "success" | "error"; text: string }>();

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

  function beginStart(card: BoardCard) {
    setStartCard(card);
    window.setTimeout(() => startDialog.current?.showModal(), 0);
  }

  async function runCardAction(card: BoardCard, action: BoardCardAction) {
    if (action === "start") return beginStart(card);
    if (!card.run) return;
    if (action === "cancel" && !window.confirm(`Cancel “${card.work.title}”? Its run history and evidence will be preserved.`)) return;
    const key = `${action}:${card.run.id}`;
    setPendingAction(key);
    setActionMessage(undefined);
    try {
      const idempotencyKey = `dashboard-${action}-${crypto.randomUUID()}`;
      if (action === "pause") await apiClient.pauseRun(card.run.id, card.run.resourceVersion, idempotencyKey);
      if (action === "resume") await apiClient.resumeRun(card.run.id, card.run.resourceVersion, idempotencyKey);
      if (action === "retry") await apiClient.retryRun(card.run.id, card.run.resourceVersion, idempotencyKey);
      if (action === "cancel") await apiClient.cancelRun(card.run.id, card.run.resourceVersion, idempotencyKey);
      await refresh();
      setActionMessage({ kind: "success", text: `${actionLabels[action]} requested. The board now reflects daemon state.` });
    } catch (error) {
      await refresh().catch(() => undefined);
      setActionMessage({ kind: "error", text: safeActionError(error) });
    } finally {
      setPendingAction("");
    }
  }

  const loading = state.hydration === "loading";
  const filtered = Boolean(projectId || workflowId || query || view === "attention");
  return (
    <div className="page page--board">
      <header className="page-header board-page-header">
        <div><p className="eyebrow">Operational workspace</p><h1>Lifecycle board</h1><p className="page-header__description">Live, server-authoritative work from intake through verified delivery.</p></div>
        <button className="button button--primary" type="button" onClick={() => createDialog.current?.showModal()} disabled={!state.snapshot.projects.some((project) => project.status === "active")}><Icon name="create" />Create work</button>
      </header>

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

      {state.hydration === "error" && <div className="board-notice board-notice--error" role="alert"><strong>Board data is unavailable.</strong><span>{state.message}</span><button type="button" onClick={() => void refresh()}>Try again</button></div>}
      {actionMessage && <div className={`board-notice board-notice--${actionMessage.kind}`} role={actionMessage.kind === "error" ? "alert" : "status"}>{actionMessage.text}</div>}

      <section className="board-preview" aria-label="Work lifecycle" aria-busy={loading || state.hydration === "refreshing"}>
        {LIFECYCLE_COLUMNS.map((lifecycle) => <BoardColumn key={lifecycle} lifecycle={lifecycle} cards={cards.filter((card) => card.lifecycle === lifecycle)} count={counts[lifecycle]} loading={loading} pendingAction={pendingAction} onAction={runCardAction} onCreate={() => createDialog.current?.showModal()} />)}
      </section>

      {!loading && cards.length === 0 && filtered && <p className="board-zero-results">No work matches these filters. <button type="button" onClick={() => { setProjectId(""); setWorkflowId(""); setQuery(""); setView("all"); }}>Show all work</button></p>}
      {!loading && !state.snapshot.projects.some((project) => project.status === "active") && <p className="board-setup-note">Register an active project with <code>darkstar project add</code> before creating work.</p>}
      <CreateWorkDialog dialogRef={createDialog} projects={state.snapshot.projects} onCreated={refresh} />
      <StartRunDialog dialogRef={startDialog} card={startCard} workflows={workflows} onRefresh={refresh} onStarted={() => setActionMessage({ kind: "success", text: "Run created. The board now reflects daemon state." })} />
    </div>
  );
}

function BoardColumn({ lifecycle, cards, count, loading, pendingAction, onAction, onCreate }: { lifecycle: BoardLifecycle; cards: BoardCard[]; count: number; loading: boolean; pendingAction: string; onAction(card: BoardCard, action: BoardCardAction): Promise<void>; onCreate(): void }) {
  return <article className="board-column" data-lifecycle={lifecycle}>
    <header className="board-column__header"><span className={`state-dot state-dot--${lifecycle}`} aria-hidden="true" /><h2>{lifecycleLabels[lifecycle]}</h2><span className="board-column__count" aria-label={`${count} work items`}>{count}</span></header>
    <div className="board-column__cards">
      {loading && [0, 1].map((key) => <div className="work-card work-card--loading" key={key} aria-hidden="true"><span /><span /><span /></div>)}
      {!loading && cards.map((card) => <WorkCard key={card.work.id} card={card} pendingAction={pendingAction} onAction={onAction} />)}
      {!loading && cards.length === 0 && lifecycle === "backlog" && <button className="board-empty-card" type="button" onClick={onCreate}><span className="board-empty-card__icon"><Icon name="spark" /></span><strong>No backlog work</strong><span>Create a work item to begin.</span></button>}
      {!loading && cards.length === 0 && lifecycle !== "backlog" && <div className="board-column__empty"><span>No {lifecycleLabels[lifecycle].toLowerCase()} work</span></div>}
    </div>
  </article>;
}

function WorkCard({ card, pendingAction, onAction }: { card: BoardCard; pendingAction: string; onAction(card: BoardCard, action: BoardCardAction): Promise<void> }) {
  const actions = availableCardActions(card);
  const projectName = card.project?.name ?? "Unknown project";
  return <article className="work-card">
    <div className="work-card__project"><span aria-hidden="true">{initials(projectName)}</span><span>{projectName}</span></div>
    <AppLink className="work-card__title" to={`/work/${encodeURIComponent(card.work.id)}`}>{card.work.title}</AppLink>
    <div className="work-card__metadata"><span title={card.work.id}>{compactId(card.work.id)}</span><span>Priority {card.work.priority}</span></div>
    {card.run ? <div className="work-card__run"><AppLink to={`/work/${encodeURIComponent(card.work.id)}/run/${encodeURIComponent(card.run.id)}`}>{card.run.workflowId} · v{card.run.workflowVersion}</AppLink><span className={`run-status run-status--${card.lifecycle}`}><span aria-hidden="true" />{humanize(card.run.status)}</span></div> : <p className="work-card__unrouted">Route not selected</p>}
    {actions.length > 0 && <div className="work-card__actions" aria-label={`Actions for ${card.work.title}`}>{actions.map((action) => {
      const key = `${action}:${card.run?.id ?? card.work.id}`;
      const pending = pendingAction === key;
      return <button key={action} type="button" className={action === "cancel" ? "card-action card-action--danger" : "card-action"} disabled={Boolean(pendingAction)} onClick={() => void onAction(card, action)}>{pending ? "Working…" : action === "start" ? "Start run" : actionLabels[action]}</button>;
    })}</div>}
  </article>;
}

function CreateWorkDialog({ dialogRef, projects, onCreated }: { dialogRef: RefObject<HTMLDialogElement | null>; projects: Schemas["Project"][]; onCreated(): Promise<void> }) {
  const { navigate } = useRouter();
  const activeProjects = projects.filter((project) => project.status === "active");
  const [projectId, setProjectId] = useState(""); const [title, setTitle] = useState(""); const [priority, setPriority] = useState("0");
  const [submitting, setSubmitting] = useState(false); const [error, setError] = useState("");
  useEffect(() => { if (!projectId && activeProjects.length === 1) setProjectId(activeProjects[0].id); }, [activeProjects, projectId]);
  function close() { dialogRef.current?.close(); if (new URLSearchParams(window.location.search).has("create")) navigate("/board", { replace: true }); }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setError(""); setSubmitting(true);
    try { const body = buildCreateWorkItemRequest({ projectId, title, priority: Number(priority) }); await apiClient.createWorkItem(body, `dashboard-create-work-${crypto.randomUUID()}`); await onCreated(); setTitle(""); setPriority("0"); close(); }
    catch (cause) { setError(cause instanceof Error && !(cause instanceof ApiRequestError) ? cause.message : safeActionError(cause)); }
    finally { setSubmitting(false); }
  }
  return <dialog ref={dialogRef} className="work-dialog" onCancel={(event) => { event.preventDefault(); close(); }} onClose={() => { if (new URLSearchParams(window.location.search).has("create")) navigate("/board", { replace: true }); }}><form onSubmit={(event) => void submit(event)}>
    <header className="work-dialog__header"><div><p className="eyebrow">New work item</p><h2>Create requested outcome</h2></div><button className="icon-button" type="button" aria-label="Close create work dialog" onClick={close}><Icon name="x" /></button></header>
    <p className="work-dialog__intro">Create authored work in a registered project. Route selection happens from the durable work record.</p>
    <label className="field"><span>Project</span><select required autoFocus value={projectId} onChange={(event) => setProjectId(event.target.value)}><option value="" disabled>Choose a project</option>{activeProjects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label>
    <label className="field"><span>Requested outcome</span><textarea required rows={4} maxLength={500} value={title} onChange={(event) => setTitle(event.target.value)} placeholder="Describe the result you want DARKSTAR to deliver" /><small>{title.length}/500</small></label>
    <label className="field field--priority"><span>Priority</span><input required type="number" inputMode="numeric" min="0" step="1" value={priority} onChange={(event) => setPriority(event.target.value)} /><small>Higher values are scheduled first.</small></label>
    {error && <p className="form-error" role="alert">{error}</p>}
    <footer className="work-dialog__footer"><button className="button" type="button" onClick={close}>Cancel</button><button className="button button--primary" type="submit" disabled={submitting || activeProjects.length === 0}>{submitting ? "Creating…" : "Create work"}</button></footer>
  </form></dialog>;
}

function StartRunDialog({ dialogRef, card, workflows, onRefresh, onStarted }: { dialogRef: RefObject<HTMLDialogElement | null>; card?: BoardCard; workflows: Schemas["WorkflowVersionSummary"][]; onRefresh(): Promise<void>; onStarted(): void }) {
  const [selection, setSelection] = useState(""); const [submitting, setSubmitting] = useState(false); const [error, setError] = useState("");
  useEffect(() => { if (!selection && workflows.length === 1) setSelection(`${workflows[0].name}\u0000${workflows[0].version}`); }, [selection, workflows]);
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); if (!card) return; const [workflowId, workflowVersion] = selection.split("\u0000"); if (!workflowId || !workflowVersion) return setError("Choose a workflow version.");
    setSubmitting(true); setError("");
    try { await apiClient.createRun({ workItemId: card.work.id, workflowId, workflowVersion }, `dashboard-start-run-${crypto.randomUUID()}`); await onRefresh(); onStarted(); dialogRef.current?.close(); }
    catch (cause) { setError(safeActionError(cause)); await onRefresh().catch(() => undefined); }
    finally { setSubmitting(false); }
  }
  return <dialog ref={dialogRef} className="work-dialog work-dialog--compact"><form onSubmit={(event) => void submit(event)}>
    <header className="work-dialog__header"><div><p className="eyebrow">Start run</p><h2>{card?.work.title ?? "Choose a workflow"}</h2></div><button className="icon-button" type="button" aria-label="Close start run dialog" onClick={() => dialogRef.current?.close()}><Icon name="x" /></button></header>
    <p className="work-dialog__intro">This creates a durable run pinned to the selected installed workflow version.</p>
    <label className="field"><span>Workflow version</span><select required autoFocus value={selection} onChange={(event) => setSelection(event.target.value)}><option value="" disabled>Choose a workflow</option>{workflows.map((workflow) => <option key={`${workflow.name}:${workflow.version}:${workflow.digest}`} value={`${workflow.name}\u0000${workflow.version}`}>{workflow.name} · {workflow.version} ({workflow.sourceScope})</option>)}</select></label>
    {workflows.length === 0 && <p className="form-error" role="status">No installed workflows are available. Install one with the CLI first.</p>}{error && <p className="form-error" role="alert">{error}</p>}
    <footer className="work-dialog__footer"><button className="button" type="button" onClick={() => dialogRef.current?.close()}>Cancel</button><button className="button button--primary" type="submit" disabled={submitting || workflows.length === 0}>{submitting ? "Starting…" : "Start run"}</button></footer>
  </form></dialog>;
}

function countByLifecycle(cards: readonly BoardCard[]) { const counts = Object.fromEntries(LIFECYCLE_COLUMNS.map((lifecycle) => [lifecycle, 0])) as Record<BoardLifecycle, number>; for (const card of cards) counts[card.lifecycle] += 1; return counts; }
function safeActionError(error: unknown) { if (error instanceof ApiRequestError) { if (error.status === 409 || error.status === 412) return "This item changed before the action completed. The board was refreshed; try again."; if (error.status === 400) return "The daemon rejected this action because it is not valid for the current state."; } return "The action could not be completed. Check daemon health and try again."; }
function compactId(value: string) { return value.length < 16 ? value : `${value.slice(0, 8)}…${value.slice(-4)}`; }
function initials(value: string) { return value.split(/\s+/).filter(Boolean).slice(0, 2).map((part) => part[0]).join("").toUpperCase(); }
function humanize(value: string) { return value.replaceAll("_", " ").replace(/^./, (character) => character.toUpperCase()); }
