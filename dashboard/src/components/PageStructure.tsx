import type { ReactNode } from "react";

import { AppLink } from "../app/router";
import { ActionBar, StatusBadge } from "./InteractionPatterns";

export interface BreadcrumbItem {
  label: string;
  to?: string;
}

export function Breadcrumbs({ items }: { items: BreadcrumbItem[] }) {
  return <nav className="detail-breadcrumb" aria-label="Breadcrumb"><ol>{items.map((item, index) => <li key={`${item.label}:${index}`}>{index > 0 && <span aria-hidden="true">/</span>}{item.to ? <AppLink to={item.to}>{item.label}</AppLink> : <span aria-current="page">{item.label}</span>}</li>)}</ol></nav>;
}

export function PageHeader({ eyebrow, title, description, breadcrumbs, status, actions, readOnly, className = "" }: {
  eyebrow: string;
  title: ReactNode;
  description: ReactNode;
  breadcrumbs?: BreadcrumbItem[];
  status?: ReactNode;
  actions?: ReactNode;
  readOnly?: string;
  className?: string;
}) {
  return <>
    {breadcrumbs?.length ? <Breadcrumbs items={breadcrumbs} /> : null}
    <header className={`page-header ${className}`.trim()}>
      <div><p className="eyebrow">{eyebrow}</p><h1>{title}</h1><p className="page-header__description">{description}</p></div>
      {(status || actions || readOnly) && <div className="page-header__aside">{status}{readOnly && <StatusBadge tone="readonly">{readOnly}</StatusBadge>}{actions && <ActionBar label="Page actions">{actions}</ActionBar>}</div>}
    </header>
  </>;
}
