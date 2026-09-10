import type { ButtonHTMLAttributes, ReactNode } from "react";

import { Icon, type IconName } from "../Icon";

export type ButtonVariant = "default" | "primary" | "danger";
export type ButtonSize = "default" | "compact";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  icon?: IconName;
  children?: ReactNode;
}

// Emits the same markup the pages hand-wrote, so existing CSS and acceptance
// selectors keep matching while call sites migrate.
export function Button({ variant = "default", size = "default", icon, children, className = "", type = "button", ...props }: ButtonProps) {
  const classes = ["button", variant === "default" ? "" : `button--${variant}`, size === "compact" ? "button--compact" : "", className].filter(Boolean).join(" ");
  return <button type={type} className={classes} {...props}>{icon && <Icon name={icon} />}{children}</button>;
}

export interface IconButtonProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "children"> {
  icon: IconName;
  label: string;
}

// An icon-only control always carries its accessible name; the glyph is hidden.
export function IconButton({ icon, label, className = "", type = "button", ...props }: IconButtonProps) {
  return <button type={type} className={`icon-button ${className}`.trim()} aria-label={label} {...props}><Icon name={icon} /></button>;
}
