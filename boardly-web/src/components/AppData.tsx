import { createContext, useContext, type ReactNode } from "react";
import { api } from "../api/client";
import type { Meta, SystemStats } from "../api/types";
import { usePoll } from "../hooks/usePoll";

// What every page shares: Boardly's meta (scope, build, kinds), re-read
// every minute — so a first load that failed recovers and a new build shows —
// and its server stats, polled every 5s for the Server card and the Activity
// pulse — one request for both.
type AppData = {
  meta: Meta | null;
  stats: SystemStats | null;
  statsError: Error | null;
  reloadStats: () => Promise<void>;
};

const AppDataContext = createContext<AppData>({ meta: null, stats: null, statsError: null, reloadStats: async () => {} });
export const useAppData = () => useContext(AppDataContext);

export function AppDataProvider({ children }: { children: ReactNode }) {
  const meta = usePoll(api.meta, 60000);
  const stats = usePoll(api.stats, 5000);
  return (
    <AppDataContext.Provider value={{ meta: meta.data, stats: stats.data, statsError: stats.error, reloadStats: stats.reload }}>
      {children}
    </AppDataContext.Provider>
  );
}
