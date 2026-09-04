import { AppLink } from "../app/router";
import { Icon } from "../components/Icon";
import { PageHeader } from "../components/PageStructure";

export function PlaceholderPage({ eyebrow, title, description, action }: {
  eyebrow: string;
  title: string;
  description: string;
  action?: { label: string; to: string };
}) {
  return (
    <div className="page placeholder-page">
      <PageHeader eyebrow={eyebrow} title={title} description={description} breadcrumbs={[{ label: title }]} />
      <section className="empty-panel">
        <span className="empty-panel__icon"><Icon name="activity" /></span>
        <h2>This view is ready for live data</h2>
        <p>The application shell and route are in place. Authoritative content will appear here as the local API is connected.</p>
        {action ? <AppLink className="button" to={action.to}>{action.label}<Icon name="arrow-right" /></AppLink> : null}
      </section>
    </div>
  );
}
