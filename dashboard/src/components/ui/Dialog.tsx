import { useEffect, useRef, type FormEvent, type ReactNode } from "react";

export interface ModalDialogProps {
  open: boolean;
  labelledBy?: string;
  describedBy?: string;
  className?: string;
  /** Blocks Escape and the backdrop while a submission is in flight. */
  busy?: boolean;
  onClose(): void;
  children: ReactNode;
}

// `<dialog>` needs an imperative call to reach the top layer, so the element is
// driven from the `open` prop. No API, router, or app state is involved, which
// keeps the dialog previewable on its own.
export function ModalDialog({ open, labelledBy, describedBy, className = "", busy = false, onClose, children }: ModalDialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);
  return <dialog
    ref={ref}
    className={`work-dialog ${className}`.trim()}
    aria-labelledby={labelledBy}
    aria-describedby={describedBy}
    onCancel={(event) => { if (busy) event.preventDefault(); }}
    onClose={() => { if (!busy) onClose(); }}
  >{children}</dialog>;
}

export function DialogForm({ busy = false, onSubmit, children }: { busy?: boolean; onSubmit(event: FormEvent<HTMLFormElement>): void; children: ReactNode }) {
  return <form aria-busy={busy} onSubmit={onSubmit}>{children}</form>;
}

export function DialogHeader({ id, title, intro }: { id?: string; title: ReactNode; intro?: ReactNode }) {
  return <><header className="work-dialog__header"><h2 id={id}>{title}</h2></header>{intro && <p className="work-dialog__intro">{intro}</p>}</>;
}

export function DialogFooter({ note, children }: { note?: ReactNode; children: ReactNode }) {
  return <footer className="work-dialog__footer">{note && <p className="dialog-draft-note">{note}</p>}{children}</footer>;
}
