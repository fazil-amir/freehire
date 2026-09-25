import { formatLocal, formatShort } from "../lib/time";

/** An instant from the API, shown in the viewer's timezone; `short` drops
 * the year and seconds where space is tight. */
export function LocalTime({ iso, short = false }: { iso: string; short?: boolean }) {
  return <time dateTime={iso} title={new Date(iso).toString()}>{short ? formatShort(iso) : formatLocal(iso)}</time>;
}
