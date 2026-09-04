import { AppLink } from "../app/router";
import { EmptyState } from "../components/InteractionPatterns";
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
      <EmptyState
        kind="unavailable"
        title="That dashboard page does not exist"
        message="Check the address, or return to a known dashboard location."
        action={action ? <AppLink className="navigation-action" to={action.to}>{action.label}<Icon name="arrow-right" /></AppLink> : undefined}
      />
    </div>
  );
}
