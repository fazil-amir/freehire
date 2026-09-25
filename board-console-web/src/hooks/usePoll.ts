import { useCallback, useEffect, useRef, useState } from "react";

// usePoll loads with fn and reloads every `every` ms (0 = never), pausing
// while the tab is hidden and reloading at once when it comes back. reload()
// refreshes on demand, e.g. right after an action. A failed load keeps the
// last good data and reports the error.
export function usePoll<T>(fn: () => Promise<T>, every: number, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const fnRef = useRef(fn);
  fnRef.current = fn;

  const reload = useCallback(async () => {
    try {
      const d = await fnRef.current();
      setData(d);
      setError(null);
    } catch (e) {
      setError(e as Error);
    }
  }, []);

  useEffect(() => {
    void reload();
  }, deps);

  useEffect(() => {
    if (!every) return;
    const t = setInterval(() => {
      if (!document.hidden) void reload();
    }, every);
    const onVisible = () => {
      if (!document.hidden) void reload();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearInterval(t);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [every, reload]);

  return { data, error, reload };
}
