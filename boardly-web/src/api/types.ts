// The shapes Boardly's JSON API (/api/v1) answers with — see its api.go.

export interface KindTab {
  value: string;
  label: string;
}

export interface Meta {
  techOnly: boolean;
  buildId: string;
  buildDate: string | null;
  scheduleCapacity: number;
  kindTabs: KindTab[];
  newProviderKinds: string[];
  providerKinds: Record<string, string>; // provider → kind
}

export interface ProviderSchedule {
  id: string;
  enabled: boolean;
  times: number[]; // minutes after 00:00 UTC
  nextRun: string | null; // null while paused or due now
  dueNow: boolean;
}

export interface LastRun {
  at: string;
  status: string; // success / partial / failed / running / queued
  summary: string;
}

export interface Provider {
  provider: string;
  kind: string;
  companyCount: number;
  addedCount: number;
  schedule: ProviderSchedule | null;
  lastRun: LastRun | null;
  recentCrawl: string; // "4 minutes ago" when a Crawl should ask first
  crawling: boolean;
}

export interface Catalog {
  providers: Provider[];
  page: number;
  totalPages: number;
  total: number;
  addedOnly: boolean;
  kind: string;
  q: string;
  dbError: string;
  addedProviders: string[];
}

export interface ScheduleLoad {
  capacity: number;
  load: number[]; // crawls starting per 15-minute slot, from 00:00 UTC
}

export interface SystemStats {
  disk: { total: number; used: number; free: number } | null;
  mem: { total: number; used: number } | null;
  cpu: { pct: number; cores: number } | null;
  reindexFloorGB: number;
  running: number;
  docker: boolean;
  buildCache: { total: number; reclaimable: number } | null;
  dockerError?: string;
}

// --- Schedules ---

/** One planned run on a plan row: its time and how its latest occurrence went. */
export interface SlotRun {
  m: number; // minutes after 00:00 UTC
  s: "success" | "partial" | "failed" | "running" | "pending" | "none";
  at?: string;
  sum?: string;
  run?: number; // the run a click on the square opens
}

export interface LastCrawl {
  status: string; // "" = never
  badge: string;
  summary: string;
  at: string | null;
}

export interface ScheduleRow {
  id: string;
  provider: string;
  times: number[];
  enabled: boolean;
  running: boolean;
  queued: boolean;
  dueNow: boolean;
  nextRun: string | null;
  last: LastCrawl;
  slots: SlotRun[];
}

export interface UnscheduledRow {
  provider: string;
  crawling: boolean;
  last: LastCrawl;
  runs: SlotRun[];
}

export interface SystemRow {
  key: string;
  name: string;
  description: string;
  activityUrl: string;
  enabled: boolean;
  minute: number;
  running: boolean;
  lastRun: string | null;
  lastStatus: string;
  nextRun: string | null;
  slot: SlotRun[];
}

export interface SchedulesPage {
  capacity: number;
  load: number[];
  dbError: string;
  scheduled: ScheduleRow[];
  unscheduled: UnscheduledRow[];
  system: SystemRow[];
  addedProviders: string[];
}

// --- Runs & Activity ---

export interface ExplainSection {
  label: string;
  body: string;
}

export interface RunDetail {
  id: number;
  action: string;
  provider: string;
  label: string;
  status: string;
  summary: string;
  startedAt: string;
  finishedAt: string | null;
  duration: string;
  stdout: string;
  stderr: string;
  err: string;
  explanation: ExplainSection[] | null;
  explainEnabled: boolean;
}

export interface Step {
  id: number;
  action: string;
  label: string;
  sharedWith: string;
  status: string;
  summary: string;
  duration: string;
  startedAt: string;
  running: boolean;
}

export interface Job {
  key: string;
  kind: string;
  provider: string;
  status: string;
  summary: string;
  duration: string;
  startedAt: string;
  actions: string[];
  steps: Step[];
}

export interface ActivityPage {
  jobs: Job[];
  page: number;
  totalPages: number;
  total: number;
  running: boolean;
  explainEnabled: boolean;
  cleanup: { lastRun: string | null; lastStatus: string; nextRun: string | null; dueNow: boolean; paused: boolean };
}
