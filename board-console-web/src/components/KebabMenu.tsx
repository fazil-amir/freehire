import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";

// A row's ⋮ menu. The menu is position: fixed at the button (a table card
// clips its overflow, which would cut an absolute menu off), flips above the
// button when there is no room below, and closes on any outside click, on
// scroll and on resize. `children` gets `close` so an item can shut it.
export function KebabMenu({ label, children }: { label: string; children: (close: () => void) => ReactNode }) {
  const [open, setOpen] = useState(false);
  const btn = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const close = () => setOpen(false);

  useLayoutEffect(() => {
    if (!open || !btn.current || !menu.current) return;
    const rect = btn.current.getBoundingClientRect();
    const m = menu.current;
    const gap = 4, margin = 8;
    let top = rect.bottom + gap;
    if (top + m.offsetHeight > window.innerHeight - margin && rect.top - gap - m.offsetHeight >= margin) {
      top = rect.top - gap - m.offsetHeight;
    }
    const left = Math.min(rect.right - m.offsetWidth, window.innerWidth - margin - m.offsetWidth);
    m.style.top = top + "px";
    m.style.left = Math.max(margin, left) + "px";
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!menu.current?.contains(e.target as Node) && !btn.current?.contains(e.target as Node)) close();
    };
    document.addEventListener("mousedown", onDown);
    window.addEventListener("scroll", close, true);
    window.addEventListener("resize", close);
    return () => {
      document.removeEventListener("mousedown", onDown);
      window.removeEventListener("scroll", close, true);
      window.removeEventListener("resize", close);
    };
  }, [open]);

  return (
    <div className="kebab-wrap">
      <button ref={btn} type="button" className="kebab-btn" aria-label={label} aria-expanded={open} onClick={() => setOpen((o) => !o)}>⋮</button>
      {open && <div ref={menu} className="kebab-menu">{children(close)}</div>}
    </div>
  );
}
