// Time as the UI shows it, always in the viewer's own timezone; the API
// speaks UTC (RFC 3339 instants, and times of day as minutes after 00:00 UTC).

const pad = (n: number) => String(n).padStart(2, "0");

/** "dd/mm/yyyy - HH:MM:SS" in local time. */
export function formatLocal(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return `${pad(d.getDate())}/${pad(d.getMonth() + 1)}/${d.getFullYear()} - ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** "dd/mm HH:MM" in local time — where space is tight. */
export function formatShort(iso: string): string {
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return `${pad(d.getDate())}/${pad(d.getMonth() + 1)} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Minutes east of UTC right now: local = UTC + tzShift(). */
export const tzShift = () => -new Date().getTimezoneOffset();
export const toLocalMin = (utc: number) => (((utc + tzShift()) % 1440) + 1440) % 1440;
export const toUTCMin = (local: number) => (((local - tzShift()) % 1440) + 1440) % 1440;
export const fmtHM = (min: number) => `${pad(Math.floor(min / 60))}:${pad(min % 60)}`;

/** The viewer's short timezone name, e.g. "GMT+5:30"; empty when unavailable. */
export function zoneName(): string {
  try {
    return new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).formatToParts(new Date()).find((p) => p.type === "timeZoneName")?.value ?? "";
  } catch {
    return "";
  }
}

const GB = 1024 * 1024 * 1024;
export const size = (n: number) => (n >= GB ? (n / GB).toFixed(1) + " GB" : Math.round(n / 1024 / 1024) + " MB");
