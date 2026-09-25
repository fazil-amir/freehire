import type { ActivityPage, Catalog, ExplainSection, Meta, RunDetail, ScheduleLoad, SchedulesPage, SystemStats } from "./types";

// The one way to Board Console. Its base URL and optional key come from
// config.js (written by the Docker container at start) and otherwise from the
// build's VITE_* variables (.env.local in development). Every failure becomes
// an ApiError carrying the server's own message ({"error": "…"}), which the UI
// shows as it is.
const runtime = window.__BOARD_CONSOLE__ ?? {};
const BASE = (runtime.apiBaseUrl || import.meta.env.VITE_API_BASE_URL || "http://localhost:8040").replace(/\/$/, "");
const KEY = runtime.apiKey || import.meta.env.VITE_API_KEY || "";

export class ApiError extends Error {
  constructor(message: string, readonly status: number) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (KEY) headers.Authorization = `Bearer ${KEY}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let res: Response;
  try {
    res = await fetch(BASE + path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch {
    throw new ApiError(`Board Console is not reachable at ${BASE}.`, 0);
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  let data: unknown = undefined;
  try {
    data = text ? JSON.parse(text) : undefined;
  } catch {
    /* not JSON: use the text as the message */
  }
  if (!res.ok) {
    const msg = (data as { error?: string } | undefined)?.error || text || `Request failed (${res.status}).`;
    throw new ApiError(msg, res.status);
  }
  return data as T;
}

export interface CatalogQuery {
  show?: string;
  kind?: string;
  q?: string;
  page?: number;
}

export const api = {
  meta: () => request<Meta>("GET", "/api/v1/meta"),
  catalog: (q: CatalogQuery) => {
    const p = new URLSearchParams();
    if (q.show) p.set("show", q.show);
    if (q.kind) p.set("kind", q.kind);
    if (q.q) p.set("q", q.q);
    if (q.page && q.page > 1) p.set("page", String(q.page));
    return request<Catalog>("GET", "/api/v1/catalog?" + p.toString());
  },
  newProvider: (body: { provider: string; board: string; company: string; crawlNow: boolean }) =>
    request<unknown>("POST", "/api/v1/providers", body),
  crawl: (provider: string, refetchAll = false) =>
    request<unknown>("POST", `/api/v1/providers/${encodeURIComponent(provider)}/crawl`, { refetchAll }),
  removeProvider: (provider: string) =>
    request<unknown>("POST", `/api/v1/providers/${encodeURIComponent(provider)}/remove`),
  reindex: () => request<unknown>("POST", "/api/v1/reindex"),
  scheduleLoad: () => request<ScheduleLoad>("GET", "/api/v1/schedules/load"),
  saveSchedule: (body: { id?: string; provider: string; times: number[] }) =>
    request<unknown>("PUT", "/api/v1/schedules", body),
  deleteSchedule: (id: string) => request<void>("DELETE", `/api/v1/schedules/${encodeURIComponent(id)}`),
  schedules: () => request<SchedulesPage>("GET", "/api/v1/schedules"),
  toggleSchedule: (id: string) => request<void>("POST", `/api/v1/schedules/${encodeURIComponent(id)}/toggle`),
  toggleSystemJob: (key: string) => request<void>("POST", `/api/v1/system-jobs/${key}/toggle`),
  setSystemJobTime: (key: string, minute: number) => request<void>("PUT", `/api/v1/system-jobs/${key}/time`, { minute }),
  runSystemJob: (key: string) => request<unknown>("POST", `/api/v1/system-jobs/${key}/run`),
  run: (id: number) => request<RunDetail>("GET", `/api/v1/runs/${id}`),
  explain: (id: number) => request<{ sections: ExplainSection[] }>("POST", `/api/v1/runs/${id}/explain`),
  activity: (params: URLSearchParams) => request<ActivityPage>("GET", "/api/v1/activity?" + params.toString()),
  cleanupPreview: () => request<unknown>("POST", "/api/v1/cleanup/preview"),
  cleanupRun: () => request<unknown>("POST", "/api/v1/cleanup/run"),
  recountCompanies: () => request<unknown>("POST", "/api/v1/companies/refresh"),
  stats: () => request<SystemStats>("GET", "/api/v1/system/stats"),
  pruneBuildCache: () => request<{ reclaimed: number; reclaimedText: string }>("POST", "/api/v1/system/build-cache/prune"),
};
