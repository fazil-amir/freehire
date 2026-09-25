import { createContext, useCallback, useContext, useState, type ReactNode } from "react";

// Background-action confirmations, bottom right: a success fades after 2.5s,
// an error after 5s.
type Toast = { id: number; message: string; error: boolean; hiding: boolean };
type ToastFn = (message: string, error?: boolean) => void;

const ToastContext = createContext<ToastFn>(() => {});
export const useToast = () => useContext(ToastContext);

let nextId = 0;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const toast = useCallback<ToastFn>((message, error = false) => {
    const id = ++nextId;
    const life = error ? 5000 : 2500;
    setToasts((t) => [...t, { id, message, error, hiding: false }]);
    setTimeout(() => setToasts((t) => t.map((x) => (x.id === id ? { ...x, hiding: true } : x))), life);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), life + 400);
  }, []);
  return (
    <ToastContext.Provider value={toast}>
      {children}
      <div id="toast-stack" role="status">
        {toasts.map((t) => (
          <div key={t.id} className={"toast" + (t.error ? " toast-error" : "") + (t.hiding ? " toast-hide" : "")}>
            {t.message}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
