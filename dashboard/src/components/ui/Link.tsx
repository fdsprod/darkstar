import type { AnchorHTMLAttributes, ReactNode } from "react";

export interface LinkProps extends Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href" | "onClick"> {
  href: string;
  children: ReactNode;
  onNavigate?(href: string): void;
}

// The router-free half of navigation: a wrapper supplies onNavigate, so this
// renders in isolation and in tests without a history stack.
export function Link({ href, children, onNavigate, className, ...props }: LinkProps) {
  return <a href={href} className={className} {...props} onClick={(event) => {
    if (!onNavigate || event.defaultPrevented || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
    event.preventDefault();
    onNavigate(href);
  }}>{children}</a>;
}

export function NavigationAction({ className = "", ...props }: LinkProps) {
  return <Link className={`navigation-action ${className}`.trim()} {...props} />;
}
