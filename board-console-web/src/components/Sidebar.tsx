import { NavLink } from "react-router-dom";
import { useAppData } from "./AppData";
import { ServerCard } from "./ServerCard";

const navClass = ({ isActive }: { isActive: boolean }) => (isActive ? "active" : "");

export function Sidebar() {
  const { meta, stats } = useAppData();
  const running = stats?.running ?? 0;
  return (
    <aside className="sidebar">
      <div className="brand">
        <span className="brand-mark" aria-hidden="true">
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round"><rect x="3" y="3" width="7" height="7" rx="1.5" /><rect x="14" y="3" width="7" height="7" rx="1.5" /><rect x="3" y="14" width="7" height="7" rx="1.5" /><rect x="14" y="14" width="7" height="7" rx="1.5" /></svg>
        </span>
        <div className="brand-text">
          Board Console
          {meta && (meta.techOnly
            ? <span className="scope-pill" title="CATALOGUE_TECH_ONLY is on: crawls reject non-technical jobs and search hides them.">IT only</span>
            : <span className="scope-pill" title="CATALOGUE_TECH_ONLY=false: every job is crawled, stored and searchable. Enrichment, embeddings and the sitemap stay IT-only.">All jobs</span>)}
        </div>
      </div>
      <nav className="side-nav">
        <NavLink to="/" end className={navClass}>
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M3 5h18M3 12h18M3 19h18" /></svg>
          Catalog
        </NavLink>
        <NavLink to="/schedules" className={navClass}>
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><rect x="3" y="4" width="18" height="18" rx="2" /><path d="M16 2v4M8 2v4M3 10h18" /></svg>
          Schedules
        </NavLink>
        <NavLink to="/activity" className={navClass}>
          <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><path d="M22 12h-4l-3 9L9 3l-3 9H2" /></svg>
          Activity
          {running > 0 && (
            <span className="nav-live" title={running === 1 ? "A job is running" : `${running} jobs running`}>
              <span className="nav-live-dot" aria-hidden="true" />
              {running > 1 && <span>{running}</span>}
            </span>
          )}
        </NavLink>
      </nav>
      <div className="sidebar-end">
        <ServerCard />
      </div>
    </aside>
  );
}
