import { AppLink } from "../app/router";
import { Icon } from "../components/Icon";

const lifecycle = ["Backlog", "Ready", "Running", "Waiting", "Blocked", "Review", "Failed", "Done"];

export function BoardPage() {
  return (
    <div className="page page--board">
      <header className="page-header">
        <div>
          <p className="eyebrow">Operational workspace</p>
          <h1>Lifecycle board</h1>
          <p className="page-header__description">Work follows authoritative run state from intake through verified delivery.</p>
        </div>
        <AppLink className="button button--primary" to="/board?create=1"><Icon name="create" />Create work</AppLink>
      </header>

      <div className="board-toolbar" aria-label="Board controls">
        <div className="view-tabs" role="tablist" aria-label="Board view">
          <button className="view-tab" role="tab" aria-selected="true">All work</button>
          <button className="view-tab" role="tab" aria-selected="false">Needs attention</button>
        </div>
        <button className="filter-button" type="button"><Icon name="settings" />Filter</button>
      </div>

      <section className="board-preview" aria-label="Work lifecycle">
        {lifecycle.map((state, index) => (
          <article className="board-column" key={state}>
            <header className="board-column__header">
              <span className={`state-dot state-dot--${state.toLowerCase()}`} />
              <h2>{state}</h2>
              <span className="board-column__count">0</span>
            </header>
            {index === 0 ? (
              <div className="board-empty-card">
                <span className="board-empty-card__icon"><Icon name="spark" /></span>
                <strong>No work yet</strong>
                <p>Create or import a work item to begin.</p>
              </div>
            ) : <div className="board-column__empty" aria-hidden="true" />}
          </article>
        ))}
      </section>
    </div>
  );
}
