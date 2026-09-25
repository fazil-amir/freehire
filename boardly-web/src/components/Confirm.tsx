import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from "react";
import { Dialog, DialogHeader } from "./Dialog";

// The one confirmation dialog: every action that asks first goes through
// useConfirm(), never the browser's confirm().
export type ConfirmOptions = { title?: string; confirmLabel?: string; danger?: boolean };
type ConfirmFn = (message: string, opts?: ConfirmOptions) => Promise<boolean>;

const ConfirmContext = createContext<ConfirmFn>(async () => false);
export const useConfirm = () => useContext(ConfirmContext);

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<{ message: string; opts: ConfirmOptions } | null>(null);
  const resolver = useRef<(ok: boolean) => void>(() => {});
  const confirm = useCallback<ConfirmFn>((message, opts = {}) => {
    setState({ message, opts });
    return new Promise<boolean>((resolve) => { resolver.current = resolve; });
  }, []);
  const settle = (ok: boolean) => { setState(null); resolver.current(ok); };
  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      <Dialog open={!!state} onClose={() => settle(false)} className="dialog-sm">
        {state && (
          <>
            <DialogHeader title={state.opts.title || "Are you sure?"} onClose={() => settle(false)} />
            <div className="dialog-body"><p className="confirm-message">{state.message}</p></div>
            <div className="dialog-actions">
              {/* Focus starts on Cancel: Enter must never confirm by accident. */}
              <button type="button" className="btn btn-secondary" data-autofocus onClick={() => settle(false)}>Cancel</button>
              <button type="button" className={"btn " + (state.opts.danger ? "btn-destructive" : "btn-primary")} onClick={() => settle(true)}>
                {state.opts.confirmLabel || "Confirm"}
              </button>
            </div>
          </>
        )}
      </Dialog>
    </ConfirmContext.Provider>
  );
}
