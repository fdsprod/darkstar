import type { InputHTMLAttributes, ReactNode, SelectHTMLAttributes, TextareaHTMLAttributes } from "react";

export interface FieldProps {
  label: ReactNode;
  hint?: ReactNode;
  children: ReactNode;
  className?: string;
}

// The label owns the control, matching the existing `.field` grid: caption text
// stays inside the label span so the hint is announced with the name.
export function Field({ label, hint, children, className = "" }: FieldProps) {
  return <label className={`field ${className}`.trim()}><span>{label}{hint && <> <small>{hint}</small></>}</span>{children}</label>;
}

type Omitted = "className";

export function TextField({ label, hint, fieldClassName, ...props }: Omit<InputHTMLAttributes<HTMLInputElement>, Omitted> & { label: ReactNode; hint?: ReactNode; fieldClassName?: string }) {
  return <Field label={label} hint={hint} className={fieldClassName}><input {...props} /></Field>;
}

export function TextAreaField({ label, hint, fieldClassName, ...props }: Omit<TextareaHTMLAttributes<HTMLTextAreaElement>, Omitted> & { label: ReactNode; hint?: ReactNode; fieldClassName?: string }) {
  return <Field label={label} hint={hint} className={fieldClassName}><textarea {...props} /></Field>;
}

export function SelectField({ label, hint, fieldClassName, children, ...props }: Omit<SelectHTMLAttributes<HTMLSelectElement>, Omitted> & { label: ReactNode; hint?: ReactNode; fieldClassName?: string }) {
  return <Field label={label} hint={hint} className={fieldClassName}><select {...props}>{children}</select></Field>;
}

// Validation copy is assertive: it appears in response to a submit the reader
// already made.
export function FormError({ children }: { children: ReactNode }) {
  return children ? <p className="form-error" role="alert">{children}</p> : null;
}
