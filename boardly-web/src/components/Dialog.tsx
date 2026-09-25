import { useEffect, useRef, type ReactNode } from "react";

// A native <dialog> shown modally while `open`: Escape, a click on the
// backdrop, the × and Cancel all go through onClose, so the parent owns
// whether it is open.
export function Dialog({
  open, onClose, className = "", children,
}: { open: boolean; onClose: () => void; className?: string; children: ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const d = ref.current;
    if (!d) return;
    if (open && !d.open) {
      d.showModal();
      // showModal focuses the first focusable element (the ×); a
      // [data-autofocus] inside takes it instead.
      d.querySelector<HTMLElement>("[data-autofocus]")?.focus();
    }
    if (!open && d.open) d.close();
  }, [open]);
  return (
    <dialog
      ref={ref}
      className={className}
      onCancel={(e) => { e.preventDefault(); onClose(); }}
      // A click on the backdrop lands on the <dialog> itself, outside its box.
      onClick={(e) => {
        if (e.target !== e.currentTarget) return;
        const r = e.currentTarget.getBoundingClientRect();
        if (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom) onClose();
      }}
    >
      {open && children}
    </dialog>
  );
}

export function DialogHeader({ title, subtitle, onClose }: { title: string; subtitle?: string; onClose: () => void }) {
  return (
    <div className="dialog-header">
      <div>
        <h2>{title}</h2>
        {subtitle && <p className="dialog-subtitle">{subtitle}</p>}
      </div>
      <button type="button" className="dialog-close" aria-label="Close" onClick={onClose}>×</button>
    </div>
  );
}
