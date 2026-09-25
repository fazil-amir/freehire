import { useAppData } from "./AppData";
import { LocalTime } from "./LocalTime";

/** Which Boardly build is answering, bottom right of every page. */
export function PageFooter() {
  const { meta } = useAppData();
  if (!meta) return null;
  return (
    <footer className="page-footer">
      Build <code>{meta.buildId}</code>
      {meta.buildDate && <><span className="page-footer-sep">·</span><LocalTime iso={meta.buildDate} /></>}
    </footer>
  );
}
