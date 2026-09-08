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
  DISABLED_REASON_LABELS,
  applyTransitionPlans,
  buildCreateWorkItemRequest,
  buildWorkTransitionRequest,
  deriveBoardCards,
  disabledTransitionReason,
  filterBoardCards,
  legalTransitionTargets,
  transitionDecision,
  type BoardCard,
  type BoardLifecycle,
  type BoardView,
  type WorkTransitionSource,
} from "./boardModel";

type Schemas = components["schemas"];

const lifecycleLabels: Record<BoardLifecycle, string> = {
  backlog: "Backlog", ready: "Ready", running: "Running", waiting: "Waiting",
  blocked: "Blocked", review: "Review", failed: "Failed", done: "Done",
};
export function BoardPage() {
  const { state, refresh } = useDashboardState();
  const { search } = useRouter();
  const createDialog = useRef<HTMLDialogElement>(null);
  const [workflows, setWorkflows] = useState<Schemas["WorkflowVersionSummary"][]>([]);
  const [view, setView] = useState<BoardView>("all");
  const [projectId, setProjectId] = useState("");
  const [workflowId, setWorkflowId] = useState("");
  const [query, setQuery] = useState("");
  const [pendingAction, setPendingAction] = useState<{ workId: string; target: BoardLifecycle }>();
  const [actionMessage, setActionMessage] = useState<{ kind: "success" | "error"; text: string }>();
  const [transitionPlans, setTransitionPlans] = useState<Record<string, Schemas["WorkTransitionPlan"]>>({});
  const [selectedWorkId, setSelectedWorkId] = useState("");
  const [draggedWorkId, setDraggedWorkId] = useState("");

  const allCards = useMemo(() => deriveBoardCards(state.snapshot), [state.snapshot]);
  const boardCards = useMemo(() => applyTransitionPlans(allCards, transitionPlans), [allCards, transitionPlans]);
  const cards = useMemo(
    () => filterBoardCards(boardCards, { projectId: projectId || undefined, workflowId: workflowId || undefined, query, view }),
    [boardCards, projectId, query, view, workflowId],
  );
  const workflowNames = useMemo(
    () => [...new Set([...workflows.map((workflow) => workflow.name), ...state.snapshot.runs.map((run) => run.workflowId)])].sort((left, right) => left.localeCompare(right)),
    [state.snapshot.runs, workflows],
  );
  const counts = useMemo(() => countByLifecycle(cards), [cards]);
  const selectedCard = boardCards.find((card) => card.work.id === selectedWorkId);

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
    void Promise.allSettled(allCards.map(async (card) => [card.work.id, await apiClient.planWorkItemTransition(card.work.id, card.lifecycle)] as const))
      .then((results) => {
        if (!live) return;
        setTransitionPlans(Object.fromEntries(results.flatMap((result) => result.status === "fulfilled" ? [result.value] : [])));
      });
    return () => { live = false; };
  }, [allCards, state.lastSynchronizedAt]);

  async function runTransition(card: BoardCard, target: BoardLifecycle, source: WorkTransitionSource) {
    const plan = transitionPlans[card.work.id];
    const decision = plan && transitionDecision(plan, target);
    if (!plan || decision?.availability !== "enabled" || pendingAction) return;
    if (decision.confirmation === "required" && !window.confirm(`Move “${card.work.title}” to ${lifecycleLabels[target]}? Its run history and evidence will be preserved.`)) return;
    setPendingAction({ workId: card.work.id, target });
    setActionMessage(undefined);
    try {
      const idempotencyKey = `dashboard-work-transition-${crypto.randomUUID()}`;
      const result = await apiClient.applyWorkItemTransition(card.work.id, plan.resourceVersion, idempotencyKey, buildWorkTransitionRequest(source, target));
      setTransitionPlans((current) => ({ ...current, [card.work.id]: result.after }));
      await refresh();
      setActionMessage({ kind: "success", text: `${card.work.title} moved to ${lifecycleLabels[target]}.` });
    } catch (error) {
      if (error instanceof ApiRequestError && error.workTransitionPlan) setTransitionPlans((current) => ({ ...current, [card.work.id]: error.workTransitionPlan! }));
      await refresh().catch(() => undefined);
      setActionMessage({ kind: "error", text: safeActionError(error) });
    } finally {
      setPendingAction(undefined);
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
      {pendingAction && <AsyncPanel compact state="loading" title="Lifecycle command pending" message={<>Moving <code>{pendingAction.workId}</code> to {lifecycleLabels[pendingAction.target]}. The card stays in its authoritative column until the daemon accepts the command.</>} />}
      {actionMessage && <AsyncPanel compact state={actionMessage.kind} title={actionMessage.kind === "success" ? "Command accepted" : "Command failed"} message={actionMessage.text} />}

      <div className="board-operational-layout">
        <section className="board-preview" aria-label="Work lifecycle" aria-busy={loading || state.hydration === "refreshing"}>
          {LIFECYCLE_COLUMNS.map((lifecycle) => <BoardColumn key={lifecycle} lifecycle={lifecycle} cards={cards.filter((card) => card.lifecycle === lifecycle)} count={counts[lifecycle]} loading={loading} pendingAction={pendingAction} plans={transitionPlans} draggedCard={boardCards.find((card) => card.work.id === draggedWorkId)} onSelect={setSelectedWorkId} onDrag={setDraggedWorkId} onMove={runTransition} onCreate={() => createDialog.current?.showModal()} />)}
        </section>
        {selectedCard && <WorkQuickPanel card={selectedCard} plan={transitionPlans[selectedCard.work.id]} events={state.recentEvents} pending={pendingAction?.workId === selectedCard.work.id} onClose={() => setSelectedWorkId("")} onMove={runTransition} />}
      </div>

      {!loading && cards.length === 0 && filtered && <EmptyState compact kind="filtered" title="No work matches these filters" message="The active project, workflow, search, or attention filters exclude every work item." action={<button type="button" className="button" onClick={() => { setProjectId(""); setWorkflowId(""); setQuery(""); setView("all"); }}>Clear filters</button>} />}
      {!loading && !canCreate && <p className="board-setup-note">Register an active project with <code>darkstar project add</code> before creating work.</p>}
      <CreateWorkDialog dialogRef={createDialog} projects={state.snapshot.projects} workflows={workflows} onCreated={refresh} />
    </div>
  );
}

function BoardColumn({ lifecycle, cards, count, loading, pendingAction, plans, draggedCard, onSelect, onDrag, onMove, onCreate }: { lifecycle: BoardLifecycle; cards: BoardCard[]; count: number; loading: boolean; pendingAction?: { workId: string; target: BoardLifecycle }; plans: Record<string, Schemas["WorkTransitionPlan"]>; draggedCard?: BoardCard; onSelect(workId: string): void; onDrag(workId: string): void; onMove(card: BoardCard, target: BoardLifecycle, source: WorkTransitionSource): Promise<void>; onCreate(): void }) {
  const legalDrop = Boolean(draggedCard && legalTransitionTargets(plans[draggedCard.work.id]).includes(lifecycle));
  return <article className="board-column" data-lifecycle={lifecycle} data-drop-available={draggedCard ? String(legalDrop) : undefined}
    onDragOver={(event) => { if (legalDrop) event.preventDefault(); }}
    onDrop={(event) => { event.preventDefault(); onDrag(""); if (draggedCard && legalDrop) void onMove(draggedCard, lifecycle, "drag"); }}>
    <header className="board-column__header"><span className={`state-dot state-dot--${lifecycle}`} aria-hidden="true" /><h2>{lifecycleLabels[lifecycle]}</h2><span className="board-column__count" aria-label={`${count} work items`}>{count}</span></header>
    <div className="board-column__cards">
      {loading && [0, 1].map((key) => <div className="work-card work-card--loading" key={key} aria-hidden="true"><span /><span /><span /></div>)}
      {!loading && cards.map((card) => <WorkCard key={card.work.id} card={card} plan={plans[card.work.id]} pending={pendingAction?.workId === card.work.id} onSelect={onSelect} onDrag={onDrag} onMove={onMove} />)}
      {!loading && cards.length === 0 && lifecycle === "backlog" && <button className="board-empty-card" type="button" onClick={onCreate}><span className="board-empty-card__icon"><Icon name="spark" /></span><strong>No backlog work</strong><span>Create a work item to begin.</span></button>}
      {!loading && cards.length === 0 && lifecycle !== "backlog" && <div className="board-column__empty"><span>No {lifecycleLabels[lifecycle].toLowerCase()} work</span></div>}
    </div>
  </article>;
}

function WorkCard({ card, plan, pending, onSelect, onDrag, onMove }: { card: BoardCard; plan?: Schemas["WorkTransitionPlan"]; pending: boolean; onSelect(workId: string): void; onDrag(workId: string): void; onMove(card: BoardCard, target: BoardLifecycle, source: WorkTransitionSource): Promise<void> }) {
  const projectName = card.project?.name ?? "Unknown project";
  return <article className="work-card" draggable={!pending && legalTransitionTargets(plan).length > 0}
    aria-label={`${card.work.title}, ${lifecycleLabels[card.lifecycle]}`}
    onDragStart={(event) => { event.dataTransfer.effectAllowed = "move"; event.dataTransfer.setData("text/plain", card.work.id); onDrag(card.work.id); }}
    onDragEnd={() => onDrag("")}>
    <div className="work-card__project"><span aria-hidden="true">{initials(projectName)}</span><span>{projectName}</span></div>
    <button className="work-card__title" type="button" onClick={() => onSelect(card.work.id)}>{card.work.title}</button>
    <div className="work-card__metadata"><span title={card.work.id}>{compactId(card.work.id)}</span><span>Priority {card.work.priority}</span></div>
    {card.run ? <div className="work-card__run"><AppLink to={`/work/${encodeURIComponent(card.work.id)}/run/${encodeURIComponent(card.run.id)}`}>{card.run.workflowId} · v{card.run.workflowVersion}</AppLink><span className={`run-status run-status--${card.lifecycle}`}><span aria-hidden="true" />{humanize(card.run.status)}</span></div> : <p className="work-card__unrouted">Route not selected</p>}
    <div className="work-card__actions" aria-label={`Actions for ${card.work.title}`}><MoveMenu card={card} plan={plan} pending={pending} onMove={onMove} /><button className="card-action" type="button" onClick={() => onSelect(card.work.id)}>Quick view</button></div>
  </article>;
}

function MoveMenu({ card, plan, pending, onMove }: { card: BoardCard; plan?: Schemas["WorkTransitionPlan"]; pending: boolean; onMove(card: BoardCard, target: BoardLifecycle, source: WorkTransitionSource): Promise<void> }) {
  const menuLabel = card.lifecycle === "ready" ? "Start" : "Move";
  return <details className="move-menu"><summary className="card-action" aria-label={`${menuLabel} ${card.work.title}`}>{pending ? "Working…" : menuLabel}</summary><div className="move-menu__items" aria-label={`${menuLabel} ${card.work.title}`}>
    {LIFECYCLE_COLUMNS.map((target) => {
      const enabled = plan ? transitionDecision(plan, target)?.availability === "enabled" : false;
      const reason = disabledTransitionReason(plan, target);
      return <button key={target} type="button" disabled={!enabled || pending} title={enabled ? undefined : reason} onClick={(event) => { event.currentTarget.closest("details")?.removeAttribute("open"); void onMove(card, target, "menu"); }}><span>{target === "running" && card.lifecycle === "ready" ? "Start run" : `Move to ${lifecycleLabels[target]}`}</span><small>{enabled ? "Available" : reason}</small></button>;
    })}
  </div></details>;
}

function WorkQuickPanel({ card, plan, events, pending, onClose, onMove }: { card: BoardCard; plan?: Schemas["WorkTransitionPlan"]; events: readonly { aggregateId: string; kind: string; occurredAt: string; globalPosition: number }[]; pending: boolean; onClose(): void; onMove(card: BoardCard, target: BoardLifecycle, source: WorkTransitionSource): Promise<void> }) {
  const activity = events.filter((event) => event.aggregateId === card.work.id || event.aggregateId === card.run?.id).slice(-5).reverse();
  const blockers = plan?.targets.flatMap((target) => target.disabledReasons).filter((reason) => !["current_state", "unsupported_target", "run_not_ready", "active_run", "terminal_work"].includes(reason)) ?? [];
  return <aside className="work-quick-panel" aria-labelledby="quick-panel-title">
    <header><div><p className="eyebrow">Quick view</p><h2 id="quick-panel-title">{card.work.title}</h2></div><button className="icon-button" type="button" aria-label="Close quick view" onClick={onClose}><Icon name="x" /></button></header>
    <section><h3>Requested outcome</h3><p>{card.work.details || card.work.title}</p></section>
    <section><h3>Blocker</h3><p>{blockers.length ? [...new Set(blockers)].map((reason) => DISABLED_REASON_LABELS[reason]).join("; ") : "No blocking condition is reported by the lifecycle plan."}</p></section>
    <section><h3>Readiness</h3><p>{readinessSummary(card, plan)}</p>{card.run && <AppLink to={`/work/${encodeURIComponent(card.work.id)}/run/${encodeURIComponent(card.run.id)}/readiness`}>Open readiness →</AppLink>}</section>
    <section><h3>Next legal actions</h3><MoveMenu card={card} plan={plan} pending={pending} onMove={onMove} /></section>
    <section><h3>Recent activity</h3>{activity.length ? <ol className="quick-activity">{activity.map((event) => <li key={event.globalPosition}><strong>{humanize(event.kind)}</strong><time dateTime={event.occurredAt}>{new Date(event.occurredAt).toLocaleString()}</time></li>)}</ol> : <p>No recent streamed activity for this work item.</p>}</section>
    <footer><AppLink to={`/work/${encodeURIComponent(card.work.id)}`}>Open full work context →</AppLink>{card.run && <AppLink to={`/work/${encodeURIComponent(card.work.id)}/run/${encodeURIComponent(card.run.id)}`}>Open run →</AppLink>}<AppLink to={`/artifacts?targetKind=work&targetId=${encodeURIComponent(card.work.id)}&ingest=1`}>Add evidence →</AppLink></footer>
  </aside>;
}

function readinessSummary(card: BoardCard, plan?: Schemas["WorkTransitionPlan"]) {
  if (!plan) return "Checking the server-provided lifecycle plan.";
  const running = transitionDecision(plan, "running");
  if (running?.availability === "enabled") return card.lifecycle === "ready" ? "Ready to start." : "The run can continue.";
  return disabledTransitionReason(plan, "running");
}

function CreateWorkDialog({ dialogRef, projects, workflows, onCreated }: { dialogRef: RefObject<HTMLDialogElement | null>; projects: Schemas["Project"][]; workflows: Schemas["WorkflowVersionSummary"][]; onCreated(): Promise<void> }) {
  const { navigate } = useRouter();
  const activeProjects = projects.filter((project) => project.status === "active");
  const workflowNames = [...new Set(workflows.map((workflow) => workflow.name))].sort((left, right) => left.localeCompare(right));
  const [projectId, setProjectId] = useState(""); const [title, setTitle] = useState(""); const [details, setDetails] = useState(""); const [evidence, setEvidence] = useState("");
  const [routingMode, setRoutingMode] = useState<"automatic" | "override">("automatic"); const [workflowSelection, setWorkflowSelection] = useState(""); const [entryNodeId, setEntryNodeId] = useState(""); const [terminalNodeIds, setTerminalNodeIds] = useState("");
  const [submitting, setSubmitting] = useState(false); const [error, setError] = useState("");
  useEffect(() => { if (!projectId && activeProjects.length === 1) setProjectId(activeProjects[0].id); }, [activeProjects, projectId]);
  function close() { if (!submitting) { dialogRef.current?.close(); if (new URLSearchParams(window.location.search).has("create")) navigate("/board", { replace: true }); } }
  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault(); setError(""); setSubmitting(true);
    try {
      const [workflowId, workflowVersion] = workflowSelection.split("\u0000");
      const routingIntent: Schemas["WorkRoutingIntent"] = routingMode === "automatic" ? { mode: "automatic" } : { mode: "override", workflowId: workflowId ?? "", ...(workflowVersion ? { workflowVersion } : {}), ...(entryNodeId.trim() ? { entryNodeId } : {}), ...(terminalNodeIds.trim() ? { terminalNodeIds: terminalNodeIds.split(",") } : {}) };
      const body = buildCreateWorkItemRequest({ projectId, title, details, evidence: evidence.split("\n"), routingIntent }); await apiClient.createWorkItem(body, `dashboard-create-work-${crypto.randomUUID()}`); await onCreated(); setTitle(""); setDetails(""); setEvidence(""); setRoutingMode("automatic"); setWorkflowSelection(""); setEntryNodeId(""); setTerminalNodeIds(""); dialogRef.current?.close();
    }
    catch (cause) { setError(cause instanceof Error && !(cause instanceof ApiRequestError) ? cause.message : safeActionError(cause)); }
    finally { setSubmitting(false); }
  }
  return <dialog ref={dialogRef} className="work-dialog" onCancel={(event) => { event.preventDefault(); close(); }} onClose={() => { if (new URLSearchParams(window.location.search).has("create")) navigate("/board", { replace: true }); }}><form aria-busy={submitting} onSubmit={(event) => void submit(event)}>
    <header className="work-dialog__header"><div><p className="eyebrow">New work item</p><h2>Create requested outcome</h2></div><button className="icon-button" type="button" aria-label="Close create work dialog" disabled={submitting} onClick={close}><Icon name="x" /></button></header>
    <p className="work-dialog__intro">Create authored work in a registered project. Route selection happens from the durable work record.</p>
    <label className="field"><span>Project</span><select required autoFocus value={projectId} onChange={(event) => setProjectId(event.target.value)}><option value="" disabled>Choose a project</option>{activeProjects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}</select></label>
    <label className="field"><span>Requested outcome</span><textarea required rows={4} maxLength={500} value={title} onChange={(event) => setTitle(event.target.value)} placeholder="Describe the result you want DARKSTAR to deliver" /><small>{title.length}/500</small></label>
    <label className="field"><span>Details <small>(optional)</small></span><textarea rows={3} value={details} onChange={(event) => setDetails(event.target.value)} placeholder="Constraints, context, or success criteria" /></label>
    <label className="field"><span>Evidence <small>(optional)</small></span><textarea rows={2} value={evidence} onChange={(event) => setEvidence(event.target.value)} placeholder="One file, URL, or reference per line" /><small>Evidence is attached to the work record and does not start a run.</small></label>
    <details className="diagnostics-details"><summary>Advanced routing</summary><div className="diagnostics-details__body">
      <label className="field"><span>Routing</span><select value={routingMode} onChange={(event) => setRoutingMode(event.target.value as "automatic" | "override")}><option value="automatic">Automatic (recommended)</option><option value="override">Override workflow</option></select></label>
      {routingMode === "override" && <><label className="field"><span>Workflow</span><select required value={workflowSelection} onChange={(event) => setWorkflowSelection(event.target.value)}><option value="" disabled>Choose a workflow</option>{workflowNames.map((name) => <option key={`${name}:latest`} value={`${name}\u0000`}>{name} · latest installed</option>)}{workflows.map((workflow) => <option key={`${workflow.name}:${workflow.version}:${workflow.digest}`} value={`${workflow.name}\u0000${workflow.version}`}>{workflow.name} · {workflow.version}</option>)}</select><small>Latest installed resolves by semantic version when the run is prepared.</small></label><label className="field"><span>Entry node <small>(optional)</small></span><input value={entryNodeId} onChange={(event) => setEntryNodeId(event.target.value)} /></label><label className="field"><span>Terminal nodes <small>(optional, comma-separated)</small></span><input value={terminalNodeIds} onChange={(event) => setTerminalNodeIds(event.target.value)} /></label></>}
    </div></details>
    {error && <p className="form-error" role="alert">{error}</p>}
    <footer className="work-dialog__footer"><p className="dialog-draft-note">Closing discards unsaved changes.</p><button className="button" type="button" disabled={submitting} onClick={close}>Cancel</button><button className="button button--primary" type="submit" disabled={submitting || activeProjects.length === 0}>{submitting ? "Creating…" : "Create work"}</button></footer>
  </form></dialog>;
}

function countByLifecycle(cards: readonly BoardCard[]) { const counts = Object.fromEntries(LIFECYCLE_COLUMNS.map((lifecycle) => [lifecycle, 0])) as Record<BoardLifecycle, number>; for (const card of cards) counts[card.lifecycle] += 1; return counts; }
function safeActionError(error: unknown) { if (error instanceof ApiRequestError) { if (error.status === 412) return "This item changed before the action completed. The board was refreshed; try again."; if (error.code === "WORK_TRANSITION_FAILED") return error.message; if (error.status === 409) return "This action is not available in the item’s current state. The board was refreshed."; if (error.status === 400) return "The daemon rejected this action because it is not valid for the current state."; } return "The action could not be completed. Check daemon health and try again."; }
function compactId(value: string) { return value.length < 16 ? value : `${value.slice(0, 8)}…${value.slice(-4)}`; }
function initials(value: string) { return value.split(/\s+/).filter(Boolean).slice(0, 2).map((part) => part[0]).join("").toUpperCase(); }
function humanize(value: string) { return value.replaceAll("_", " ").replace(/^./, (character) => character.toUpperCase()); }
