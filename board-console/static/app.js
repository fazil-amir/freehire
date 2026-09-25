// The server HTML of every [data-patch] piece of a [data-patch-region], as
// last rendered — recorded before anything below touches the page, so a
// live refresh can tell which pieces the server actually changed (see
// patchRegion).
var patchSrc = {};
document.querySelectorAll("[data-patch-region] [data-patch]").forEach(function (el) {
  patchSrc[el.dataset.patch] = el.outerHTML;
});

// A live refresh of a [data-patch-region]: only the [data-patch] pieces
// whose server HTML changed are swapped, one element each, so everything
// else — an open log, a hover, where the table is scrolled — stays exactly
// as it is. When pieces were added or removed (a schedule saved or
// deleted) it falls back to swapping the whole region.
function patchRegion(region, fresh) {
  function keyed(root) {
    var m = {};
    root.querySelectorAll("[data-patch]").forEach(function (el) { m[el.dataset.patch] = el; });
    return m;
  }
  var olds = keyed(region), news = keyed(fresh);
  var keys = Object.keys(news);
  var same = keys.length === Object.keys(olds).length && keys.every(function (k) { return olds[k]; });
  if (!same) {
    region.innerHTML = fresh.innerHTML;
    patchSrc = {};
    keys.forEach(function (k) { patchSrc[k] = news[k].outerHTML; });
    return;
  }
  keys.forEach(function (k) {
    var html = news[k].outerHTML;
    if (patchSrc[k] === html) return;
    patchSrc[k] = html;
    var hidden = olds[k].hidden; // a filter may have hidden it
    var next = document.importNode(news[k], true);
    next.hidden = hidden;
    olds[k].replaceWith(next);
  });
}

// Shows an inline error message right next to a field, for client-side
// validation — server-side validation (the source of truth) uses the
// equivalent Go-rendered .field-error instead.
function setFieldError(input, message) {
  var wrapper = input.closest("label") || input.parentElement;
  var err = wrapper.querySelector(".field-error-inline");
  if (!err) {
    err = document.createElement("span");
    err.className = "field-error-inline";
    wrapper.appendChild(err);
  }
  err.textContent = message || "";
  input.classList.toggle("input-invalid", !!message);
}

// Every in-page POST goes through postForm: the header tells the server
// it's a fetch (see respond.go), so it answers with a status rather than
// a redirect. Resolves on success; rejects with an Error whose message is
// the server's own (a 422's JSON {"error"}), or sends an expired session
// to login.
function postForm(form) {
  return fetch(form.getAttribute("action"), {
    method: "POST",
    headers: { "X-Board-Console-Fetch": "1" },
    body: new URLSearchParams(new FormData(form))
  }).then(function (r) {
    if (r.status === 401) {
      window.location.href = "/login";
      throw new Error("Session expired.");
    }
    if (r.ok) return;
    return r.text().then(function (text) {
      var msg = text;
      try { msg = JSON.parse(text).error || text; } catch (e) { /* plain-text error */ }
      throw new Error(msg || "Request failed (" + r.status + ").");
    });
  });
}

// A small non-blocking confirmation in the corner that fades on its own.
function toast(message, isError) {
  var stack = document.getElementById("toast-stack");
  if (!stack) {
    stack = document.createElement("div");
    stack.id = "toast-stack";
    stack.setAttribute("role", "status");
    document.body.appendChild(stack);
  }
  var el = document.createElement("div");
  el.className = "toast" + (isError ? " toast-error" : "");
  el.textContent = message;
  stack.appendChild(el);
  setTimeout(function () { el.classList.add("toast-hide"); }, isError ? 5000 : 2500);
  setTimeout(function () { el.remove(); }, isError ? 5400 : 2900);
}

// Re-renders the page's [data-live-region]s (its table, a status card)
// from the server, in place — the same Go template a full load would use,
// so an action's effect shows without a navigation or a second copy of the
// markup. Regions are matched to the fresh page's by order.
function refreshLiveRegion() {
  var regions = document.querySelectorAll("[data-live-region]");
  if (!regions.length) return Promise.resolve();
  // A re-render replaces the rows — and with them an open ⋮ menu, which
  // would snap shut under the pointer while a run is updating the table.
  // Skip this refresh; every caller schedules another, which lands once the
  // menu is closed.
  if (document.querySelector(".kebab-menu:not([hidden])")) return Promise.resolve();
  return fetch(window.location.pathname + window.location.search, {
    headers: { "X-Board-Console-Fetch": "1" }
  })
    .then(function (r) {
      if (r.status === 401) window.location.href = "/login";
      return r.ok ? r.text() : null;
    })
    .then(function (html) {
      if (!html) return;
      var fresh = new DOMParser().parseFromString(html, "text/html").querySelectorAll("[data-live-region]");
      regions.forEach(function (region, i) {
        if (!fresh[i]) return;
        if (region.hasAttribute("data-patch-region")) patchRegion(region, fresh[i]);
        else region.innerHTML = fresh[i].innerHTML;
      });
      document.dispatchEvent(new Event("liveregion:refreshed"));
    });
}

// Click a .run-row (Activity, Schedules) to expand the .run-detail row
// holding its output. Delegated, since tables are re-rendered in place;
// clicks on the row's own controls (a toggle, a kebab) are theirs, not this.
document.addEventListener("click", function (e) {
  var row = e.target.closest(".run-row");
  if (!row || e.target.closest("button, a, form, input, select")) return;
  var detail = document.getElementById("detail-" + row.dataset.runId);
  if (detail) detail.hidden = !detail.hidden;
});

// Click a .job-row (Activity) to show or hide its steps. Closing a job also
// closes its steps' open logs.
document.addEventListener("click", function (e) {
  var row = e.target.closest(".job-row");
  if (!row || e.target.closest("button, a, form, input, select")) return;
  setJobOpen(row, !row.classList.contains("open"));
});

function setJobOpen(row, open) {
  var key = row.dataset.job;
  row.classList.toggle("open", open);
  document.querySelectorAll('[data-step-of="' + key + '"]').forEach(function (el) { el.hidden = !open; });
  if (!open) {
    document.querySelectorAll('[data-detail-of="' + key + '"]').forEach(function (el) { el.hidden = true; });
  }
}

// refreshLiveRegion, keeping every expanded .run-detail expanded.
// refreshLiveRegion, keeping open what was open: expanded logs, and on
// Activity the open jobs' step rows ([data-keep-open]).
function refreshKeepingOpen() {
  var open = Array.prototype.map.call(
    document.querySelectorAll(".run-detail:not([hidden]), [data-keep-open]:not([hidden])"),
    function (el) { return el.id; }
  );
  return refreshLiveRegion().then(function () {
    open.forEach(function (id) {
      var el = document.getElementById(id);
      if (el) el.hidden = false;
    });
    // A job row reads open when any of its steps is showing.
    document.querySelectorAll(".job-row").forEach(function (row) {
      var shown = document.querySelector('[data-step-of="' + row.dataset.job + '"]:not([hidden])');
      row.classList.toggle("open", !!shown);
    });
  });
}

// Keep a table live while something in it is running: every 3s while any
// row carries [data-running], re-render it in place (expanded output kept
// open). idleMs is the slower cadence used when nothing is running — 0 for
// a table where nothing starts on its own (Catalog: a click refreshes it,
// which restarts this loop through the refresh event); Schedules keeps a
// slow idle refresh because the scheduler starts runs by itself.
function keepLive(selector, idleMs) {
  if (!document.querySelector(selector)) return;
  var timer = null;
  function next() {
    clearTimeout(timer);
    var ms = document.querySelector(selector + " [data-running]") ? 3000 : idleMs;
    if (ms) timer = setTimeout(function () { refreshKeepingOpen().finally(next); }, ms);
  }
  document.addEventListener("liveregion:refreshed", next);
  next();
}
keepLive("[data-schedules]", 15000);
keepLive("#catalog-results", 0);

// "Explain this run": ask the model about one run and show the answer
// under its output. The server caches answers for finished runs and renders
// them on every refresh, so the answer survives the page's live updates.
document.addEventListener("click", function (e) {
  var btn = e.target.closest("[data-explain]");
  if (!btn) return;
  var id = btn.dataset.explain;
  btn.disabled = true;
  btn.textContent = "Thinking…";
  fetch("/activity/explain", {
    method: "POST",
    headers: { "X-Board-Console-Fetch": "1" },
    body: new URLSearchParams({ id: id })
  })
    .then(function (r) {
      return r.json().then(function (data) {
        if (!r.ok) throw new Error(data.error || "Could not explain this run.");
        return data.html;
      });
    })
    .then(function (html) {
      // Look the box up again: a live refresh may have replaced it meanwhile.
      // The HTML is the server's own template output (escaped there).
      document.querySelectorAll('[data-explain-for="' + id + '"]').forEach(function (box) {
        box.innerHTML = html;
      });
    })
    .catch(function (err) {
      toast(err.message, true);
      document.querySelectorAll('[data-explain="' + id + '"]').forEach(function (again) {
        again.disabled = false;
        again.textContent = "✦ Explain this run";
      });
    });
});

// Any [data-dialog-close] (the header's ×, a Cancel button) closes its
// dialog, as does a click on the backdrop — which lands on the <dialog>
// element itself, outside its content box.
document.addEventListener("click", function (e) {
  var closer = e.target.closest("[data-dialog-close]");
  if (closer) {
    closer.closest("dialog").close();
    return;
  }
  if (e.target.tagName === "DIALOG") {
    var r = e.target.getBoundingClientRect();
    var inside = e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom;
    if (!inside) e.target.close();
  }
});

// Row actions — kebab items and Catalog's Crawl buttons — are
// <form data-async>: submitted in the background, confirmed with a toast,
// and the operator stays on the page. data-confirm asks first,
// data-done is the toast text, data-refresh re-renders the table after.
// Delegated, so it covers rows swapped in by search or a refresh.
document.addEventListener("submit", function (e) {
  var form = e.target;
  if (!form.matches("form[data-async]")) return;
  e.preventDefault();
  var ask = form.dataset.confirm
    ? confirmDialog(form.dataset.confirm, {
        title: form.dataset.confirmTitle,
        confirmLabel: form.dataset.confirmLabel,
        danger: form.hasAttribute("data-confirm-danger")
      })
    : Promise.resolve(true);
  ask.then(function (ok) {
    if (ok) runAsyncForm(form);
  });
});

function runAsyncForm(form) {
  postForm(form)
    .then(function () {
      toast(form.dataset.done || "Done");
      if (form.hasAttribute("data-refresh")) return refreshLiveRegion();
    })
    .catch(function (err) { toast(err.message, true); });
}

// confirmDialog asks in Board Console's own dialog (templates/partials.html,
// "confirm-modal") and resolves true only when the confirm button is
// pressed. Cancel, ×, Esc and a backdrop click all resolve false. Focus
// starts on Cancel, so a stray Enter never confirms a destructive action.
// opts: {title, confirmLabel, danger}.
function confirmDialog(message, opts) {
  var dialog = document.getElementById("confirm-dialog");
  if (!dialog) return Promise.resolve(false);
  opts = opts || {};
  var ok = dialog.querySelector("[data-confirm-ok]");
  var cancel = dialog.querySelector("[data-confirm-cancel]");
  dialog.querySelector("#confirm-title").textContent = opts.title || "Are you sure?";
  dialog.querySelector("#confirm-message").textContent = message;
  ok.textContent = opts.confirmLabel || "Confirm";
  ok.className = "btn " + (opts.danger ? "btn-destructive" : "btn-primary");

  return new Promise(function (resolve) {
    var confirmed = false;
    ok.onclick = function () { confirmed = true; dialog.close(); };
    cancel.onclick = function () { dialog.close(); };
    dialog.addEventListener("close", function onClose() {
      dialog.removeEventListener("close", onClose);
      ok.onclick = cancel.onclick = null;
      resolve(confirmed);
    });
    dialog.showModal();
    cancel.focus();
  });
}

// Times are rendered by the server in UTC (its container's zone); every
// <time data-local> is rewritten here in the VIEWER's timezone, in the one
// format used everywhere: "20/12/2026 - 16:30:50". Re-run after anything
// swaps table HTML in.
function localizeTimes() {
  function pad(n) { return String(n).padStart(2, "0"); }
  document.querySelectorAll("time[data-local]").forEach(function (el) {
    var d = new Date(el.getAttribute("datetime"));
    if (isNaN(d)) return;
    var hm = pad(d.getHours()) + ":" + pad(d.getMinutes());
    // data-local="hm" is a time of day alone (a schedule's planned times).
    el.textContent = el.dataset.local === "hm" ? hm
      : pad(d.getDate()) + "/" + pad(d.getMonth() + 1) + "/" + d.getFullYear() + " - " + hm + ":" + pad(d.getSeconds());
    el.title = d.toString();
  });
}
localizeTimes();
document.addEventListener("liveregion:refreshed", localizeTimes);

// The Schedules plan (templates/schedules.html): one table whose rows are
// the providers, each run drawn on its row's 24-hour track in the viewer's
// timezone from the row's slots (slotRuns in handlers_schedules.go:
// minutes after 00:00 UTC, each with how its latest run went — coloured
// here). The Load row comes from #timeline-data. The system jobs' rows
// (their own tbody) are drawn the same way but never sorted or counted. Every mark sits on the moment it stands for — an
// hour label on its gridline, a run's square centred on its start, the
// "now" line — and rows are ordered by their first run of the viewer's day.
var slotShown = {}; // a plan row's log panel (detail row) id → the run shown in it (see showSlotLog)
function drawPlan() {
  var table = document.querySelector("[data-plan]");
  var src = document.getElementById("timeline-data");
  if (!table || !src) return;
  var data;
  try { data = JSON.parse(src.textContent); } catch (e) { return; }
  var DAY = 1440;
  var shift = -new Date().getTimezoneOffset(); // local = UTC + shift (minutes)
  function local(min) { return ((min + shift) % DAY + DAY) % DAY; }
  function pct(min) { return (min / DAY * 100) + "%"; }
  function el(tag, cls, parent) { var e = document.createElement(tag); if (cls) e.className = cls; if (parent) parent.appendChild(e); return e; }
  function hhmm(min) { var m = local(min); return String(Math.floor(m / 60)).padStart(2, "0") + ":" + String(m % 60).padStart(2, "0"); }
  // A run's square: centred on its start (its size is CSS's).
  function square(track, startUTC, cls, label) {
    var b = el("div", cls, track);
    b.style.left = pct(local(startUTC));
    b.title = label;
    return b;
  }
  var nowMin = new Date().getHours() * 60 + new Date().getMinutes();
  function slots(track) { try { return JSON.parse(track.dataset.slots || "[]"); } catch (e) { return []; } }
  function times(track) { return slots(track).map(function (x) { return x.m; }); }
  var STATUS = { success: "success", partial: "partial", failed: "failed", running: "running", pending: "not run yet", none: "no run" };
  function ago(iso) {
    var mins = Math.round((Date.now() - new Date(iso)) / 60000);
    return mins < 60 ? mins + " min ago" : Math.round(mins / 60) + " h ago";
  }

  // Hour labels in the header, each on its hour; the ones between the
  // 3-hourly labels hide on a narrow screen.
  var scale = table.querySelector("[data-plan-scale]");
  scale.replaceChildren();
  for (var h = 0; h < 24; h++) {
    var t = el("span", h % 3 ? "tl-tick minor" : "tl-tick", scale);
    t.style.left = pct(h * 60);
    t.textContent = String(h).padStart(2, "0") + ":00";
  }

  // Load: how many planned crawls start in each slot, shading from light
  // (one crawl) to full (booked), red past the capacity.
  var loadTrack = table.querySelector("[data-plan-load]");
  if (loadTrack) {
    loadTrack.replaceChildren();
    (data.load || []).forEach(function (n, i) {
      if (!n) return;
      var b = square(loadTrack, i * 15, n > data.capacity ? "tl-slot over" : "tl-slot",
        n + " crawl" + (n > 1 ? "s" : "") + " at " + hhmm(i * 15) +
        (n > data.capacity ? " — " + (n - data.capacity) + " will queue" : n >= data.capacity ? " — booked" : ""));
      if (n < data.capacity) b.style.opacity = String(0.35 + 0.65 * n / data.capacity);
    });
  }

  // Each provider's runs, coloured by how each one last went; the next
  // one is ringed.
  function drawRuns(track, cls) {
    track.replaceChildren();
    var next = track.dataset.next ? new Date(track.dataset.next) : null;
    var nextUTC = next ? next.getUTCHours() * 60 + next.getUTCMinutes() : -1;
    slots(track).forEach(function (x) {
      var isNext = x.m === nextUTC;
      // Later today (in the viewer's day) it has not run yet: its latest
      // run was yesterday's, which this day's plan does not show.
      if (local(x.m) > nowMin) {
        square(track, x.m, cls + " run-upcoming" + (isNext ? " next" : ""),
          track.dataset.provider + " · " + hhmm(x.m) + " — later today" + (isNext ? " (next run)" : ""));
        return;
      }
      var label = track.dataset.provider + " · " + hhmm(x.m) + " — " + STATUS[x.s] +
        (x.at ? " (" + ago(x.at) + ")" : "") + (x.sum ? " · " + x.sum : "") + (isNext ? "\nNext run" : "");
      var b = square(track, x.m, cls + " run-" + x.s + (isNext ? " next" : ""), label + (x.run ? "\nClick for its log" : ""));
      if (x.run) {
        b.dataset.run = x.run;
        b.dataset.label = track.dataset.provider + " · " + hhmm(x.m);
      }
    });
  }
  var tracks = Array.prototype.slice.call(table.querySelectorAll("[data-plan-track]"));
  var enabled = 0, perDay = 0;
  tracks.forEach(function (track) {
    drawRuns(track, "tl-block");
    if (track.hasAttribute("data-enabled")) { enabled++; perDay += times(track).length; }
  });
  table.querySelectorAll("[data-system-track]").forEach(function (track) { drawRuns(track, "tl-block sys"); });

  // Rows by their first run of the viewer's day; each keeps its log row.
  var body = table.tBodies[0];
  function firstLocal(track) { return Math.min.apply(null, times(track).map(local).concat([DAY])); }
  tracks.slice().sort(function (a, b) {
    return firstLocal(a) - firstLocal(b) || (a.dataset.provider < b.dataset.provider ? -1 : 1);
  }).forEach(function (track) {
    var row = track.closest("tr");
    var detail = document.getElementById("detail-" + row.dataset.runId);
    body.appendChild(row);
    if (detail) body.appendChild(detail);
  });
  var nomatch = body.querySelector(".plan-nomatch");
  if (nomatch) body.appendChild(nomatch);
  Object.keys(slotShown).forEach(function (id) {
    var detail = document.getElementById(id);
    if (detail) markSlot(detail, slotShown[id]);
  });

  // "Now", as a share of the day: every timeline cell draws its line there.
  var now = new Date();
  table.style.setProperty("--now", String((now.getHours() * 60 + now.getMinutes()) / DAY));

  var peak = Math.max.apply(null, (data.load || [0]).concat([0]));
  var summary = table.parentNode.querySelector("[data-plan-summary]");
  applyPlanFilters();
  if (summary) {
    summary.textContent = enabled + " schedule" + (enabled === 1 ? "" : "s") + " · " + perDay +
      " crawls a day · busiest slot " + peak + " of " + data.capacity;
    summary.classList.toggle("over", peak > data.capacity);
  }
}
// The plan's toolbar: Enabled/Paused, the last run's result and a provider
// search, applied in the page (every schedule is already on it) and kept in
// the URL, so a reload or a shared link shows the same view.
var planFilter = { state: "", status: "", q: "" };
(function () {
  var bar = document.querySelector("[data-plan-filters]");
  if (!bar) return;
  var params = new URLSearchParams(window.location.search);
  planFilter = { state: params.get("state") || "", status: params.get("status") || "", q: params.get("q") || "" };
  var search = bar.querySelector("[data-plan-search]");
  search.value = planFilter.q;
  function mark() {
    bar.querySelectorAll("[data-plan-state]").forEach(function (b) { b.classList.toggle("active", b.dataset.planState === planFilter.state); });
    bar.querySelectorAll("[data-plan-status]").forEach(function (b) { b.classList.toggle("active", b.dataset.planStatus === planFilter.status); });
  }
  function commit() {
    var p = new URLSearchParams();
    if (planFilter.state) p.set("state", planFilter.state);
    if (planFilter.status) p.set("status", planFilter.status);
    if (planFilter.q) p.set("q", planFilter.q);
    var qs = p.toString();
    history.replaceState(null, "", window.location.pathname + (qs ? "?" + qs : ""));
    mark();
    applyPlanFilters();
  }
  bar.addEventListener("click", function (e) {
    var b = e.target.closest("[data-plan-state], [data-plan-status]");
    if (!b) return;
    if (b.hasAttribute("data-plan-state")) planFilter.state = b.dataset.planState;
    else planFilter.status = b.dataset.planStatus;
    commit();
  });
  search.addEventListener("input", function () { planFilter.q = search.value.trim(); commit(); });
  mark();
})();

function applyPlanFilters() {
  var table = document.querySelector("[data-plan]");
  if (!table) return;
  var q = planFilter.q.toLowerCase(), shown = 0, any = false, systemShown = 0;
  table.querySelectorAll("[data-plan-track], [data-system-track]").forEach(function (track) {
    var system = track.hasAttribute("data-system-track");
    if (!system) any = true;
    var row = track.closest("tr");
    var ok = (!q || track.dataset.provider.toLowerCase().indexOf(q) !== -1) &&
      (!planFilter.state || (planFilter.state === "enabled") === track.hasAttribute("data-enabled")) &&
      (!planFilter.status || track.dataset.last === planFilter.status);
    row.hidden = !ok;
    if (system) { if (ok) systemShown++; return; }
    var detail = document.getElementById("detail-" + row.dataset.runId);
    if (!ok && detail) detail.hidden = true; // a hidden row takes its open log with it
    if (ok) shown++;
  });
  var group = table.querySelector(".plan-group");
  if (group) group.hidden = systemShown === 0;
  var nomatch = table.querySelector(".plan-nomatch");
  if (nomatch) nomatch.hidden = !any || shown > 0;
}

drawPlan();
document.addEventListener("liveregion:refreshed", drawPlan);

// Click a square on the plan (only one that ran: a past time of today):
// the run that served that time opens in the panel under its row, fetched
// from /schedules/run. Clicking it again, or ✕, closes it; clicking another
// square switches to that run. The row itself is not clickable. The panel
// is left alone by live refreshes (patchRegion); only a run still going is
// re-read, keeping the log's scroll position.
function markSlot(detail, runId) {
  var row = detail.previousElementSibling;
  if (!row) return;
  row.querySelectorAll(".tl-block.selected").forEach(function (b) { b.classList.remove("selected"); });
  if (runId) row.querySelectorAll('.tl-block[data-run="' + runId + '"]').forEach(function (b) { b.classList.add("selected"); });
}
function closeSlotLog(detail) {
  delete slotShown[detail.id];
  detail.hidden = true;
  detail.querySelector("[data-slot-log]").replaceChildren();
  markSlot(detail, null);
}
function showSlotLog(detail, runId, title) {
  var box = detail.querySelector("[data-slot-log]");
  fetch("/schedules/run?id=" + encodeURIComponent(runId), { headers: { "X-Board-Console-Fetch": "1" } })
    .then(function (r) {
      if (r.status === 401) window.location.href = "/login";
      return r.text().then(function (t) { if (!r.ok) throw new Error(t || "Could not load this run."); return t; });
    })
    .then(function (html) {
      var same = slotShown[detail.id] === runId;
      var pre = box.querySelector(".run-output"), top = pre ? pre.scrollTop : 0;
      box.innerHTML = html;
      if (same && (pre = box.querySelector(".run-output"))) pre.scrollTop = top;
      if (title) detail.querySelector("[data-slot-title]").textContent = title;
      detail.hidden = false;
      slotShown[detail.id] = runId;
      markSlot(detail, runId);
      localizeTimes();
    })
    .catch(function (err) { toast(err.message.trim(), true); });
}
document.addEventListener("click", function (e) {
  var close = e.target.closest("[data-slot-close]");
  if (close) { closeSlotLog(close.closest(".slot-detail")); return; }
  var sq = e.target.closest(".tl-block[data-run]");
  if (!sq) return;
  var row = sq.closest(".plan-row");
  var detail = row && document.getElementById("detail-" + row.dataset.runId);
  if (!detail) return;
  if (!detail.hidden && slotShown[detail.id] === sq.dataset.run) { closeSlotLog(detail); return; }
  showSlotLog(detail, sq.dataset.run, sq.dataset.label);
});
document.addEventListener("liveregion:refreshed", function () {
  Object.keys(slotShown).forEach(function (id) {
    var detail = document.getElementById(id);
    if (!detail) { delete slotShown[id]; return; }
    var sq = detail.previousElementSibling && detail.previousElementSibling.querySelector('.tl-block[data-run="' + slotShown[id] + '"]');
    // A full swap (a schedule added or removed) empties the panel; a run
    // still going has a growing log. Anything else is left as it is.
    var empty = !detail.querySelector("[data-slot-log]").childElementCount;
    if (empty || (sq && sq.classList.contains("run-running"))) showSlotLog(detail, slotShown[id], sq && sq.dataset.label);
  });
});
// Keep the "now" line moving between refreshes.
setInterval(function () {
  var table = document.querySelector("[data-plan]");
  if (!table) return;
  var now = new Date();
  table.style.setProperty("--now", String((now.getHours() * 60 + now.getMinutes()) / 1440));
}, 60000);

// Real-time catalog search: every keystroke (debounced) fetches the
// results fragment and swaps it in place — no reload, no Search button.
// The surrounding <form> still works as a plain GET via Enter or without
// JS, so this is additive, not the only path.
(function () {
  var input = document.getElementById("search-input");
  var kindInput = document.getElementById("search-kind");
  var showInput = document.getElementById("search-show");
  var results = document.getElementById("catalog-results");
  if (!input || !results) return;

  var debounceTimer = null;

  function runSearch() {
    var params = new URLSearchParams();
    params.set("kind", kindInput ? kindInput.value : "");
    params.set("q", input.value);
    if (showInput) params.set("show", showInput.value);

    fetch("/catalog/results?" + params.toString())
      .then(function (r) { return r.text(); })
      .then(function (html) {
        results.innerHTML = html;
        // Same announcement as an in-place refresh: times get localized and
        // a crawling row starts the live refresh (keepLive).
        document.dispatchEvent(new Event("liveregion:refreshed"));
        history.replaceState(null, "", "/?" + params.toString());
      })
      .catch(function () { /* leave the current results showing */ });
  }

  input.addEventListener("input", function () {
    clearTimeout(debounceTimer);
    debounceTimer = setTimeout(runSearch, 250);
  });

  // Enter still works, but as a live fetch rather than a full navigation —
  // JS is already present at that point, so there's no reason to reload.
  input.closest("form").addEventListener("submit", function (e) {
    e.preventDefault();
    clearTimeout(debounceTimer);
    runSearch();
  });
})();

// "+ New provider" dialog: kind selector filters the provider input's
// datalist AND drives which fields are even shown — an Aggregator is a
// single feed (provider only), a Career site is always boardless
// (provider + company), and only an ATS platform needs all three. On a
// server-side validation failure the page reloads with the modal already
// open and every field prefilled by Go (see catalog.html) — this script
// just needs to reapply the same kind-driven visibility for whichever
// kind came back selected.
(function () {
  var openBtn = document.getElementById("new-provider-btn");
  var dialog = document.getElementById("new-provider-dialog");
  var kindSelect = document.getElementById("new-provider-kind");
  var providerInput = document.getElementById("new-provider-input");
  var boardInput = document.getElementById("new-provider-board");
  var companyInput = document.getElementById("new-provider-company");
  var crawlNowInput = document.querySelector('#new-provider-dialog [name="crawl_now"]');
  var submitBtn = document.getElementById("new-provider-submit");
  var datalist = document.getElementById("new-provider-datalist");
  var dataEl = document.getElementById("provider-kinds-data");
  if (!openBtn || !dialog) return;

  var providerKinds = {};
  if (dataEl) {
    try { providerKinds = JSON.parse(dataEl.textContent); } catch (e) { providerKinds = {}; }
  }

  // Mirrors displayName() in providerkind.go — doesn't need to be perfect,
  // just a reasonable stand-in the operator can hand-edit in the CSV
  // afterward if it looks off.
  function displayName(provider) {
    return provider.replace(/[-_]/g, " ")
      .split(" ")
      .filter(function (w) { return w.length > 0; })
      .map(function (w) { return w.charAt(0).toUpperCase() + w.slice(1); })
      .join(" ");
  }

  // Kind decides which fields are even relevant: an ATS platform is many
  // companies each with their own board (all three fields); an Aggregator
  // is one feed, not a per-company entry (provider only — board/company
  // are derived, not asked for); a Career site is always boardless
  // (provider + company, no board).
  function applyKindFields(kind) {
    var showBoard = kind === "ATS platform";
    var showCompany = kind === "ATS platform" || kind === "Career site";
    var showCrawlToggle = kind !== "Aggregator";

    document.getElementById("field-board").hidden = !showBoard;
    boardInput.required = showBoard;
    if (!showBoard) boardInput.value = "";

    document.getElementById("field-company").hidden = !showCompany;
    companyInput.required = showCompany;
    setFieldError(companyInput, "");

    document.getElementById("field-crawl-now").hidden = !showCrawlToggle;
    if (kind === "Aggregator") crawlNowInput.checked = true;

    submitBtn.textContent = kind === "Aggregator" ? "Add + Crawl" : "Add row";
  }

  function populateDatalist(kind) {
    datalist.innerHTML = "";
    Object.keys(providerKinds).sort().forEach(function (provider) {
      if (providerKinds[provider] === kind) {
        var opt = document.createElement("option");
        opt.value = provider;
        datalist.appendChild(opt);
      }
    });
  }

  // The single place that makes the Provider input and the Board/Company/
  // crawl-now fields agree with whichever kind is selected — called on
  // every kind change, but ALSO once right after setup (below) so the
  // page's default kind (ATS platform, selected server-side in the
  // template unless a validation error reopened it as something else) is
  // already correct from first paint, not just after the user touches the
  // dropdown.
  function selectKind(kind) {
    kindSelect.value = kind;
    providerInput.disabled = !kind;
    providerInput.placeholder = kind
      ? "Type to search " + kind + " providers…"
      : "Select a kind first…";
    if (kind) populateDatalist(kind);
    applyKindFields(kind);
  }

  if (kindSelect && providerInput && datalist) {
    kindSelect.addEventListener("change", function () {
      providerInput.value = "";
      setFieldError(providerInput, "");
      selectKind(kindSelect.value);
    });

    // Correct from first paint — see selectKind's own comment.
    selectKind(kindSelect.value);

    // Fresh open (button click, not a server-side reopen after an error):
    // clear the form back to its default state — ATS platform selected,
    // its fields showing, nothing else filled in.
    openBtn.addEventListener("click", function () {
      providerInput.value = "";
      boardInput.value = "";
      companyInput.value = "";
      crawlNowInput.checked = false;
      setFieldError(kindSelect, "");
      setFieldError(providerInput, "");
      setFieldError(companyInput, "");
      selectKind("ATS platform");
      dialog.showModal();
    });
  } else {
    openBtn.addEventListener("click", function () { dialog.showModal(); });
  }

  if (dialog.dataset.openOnLoad === "true") dialog.showModal();

  var form = dialog.querySelector("form");
  form.addEventListener("submit", function (e) {
    var ok = true;

    if (!kindSelect.value) {
      setFieldError(kindSelect, "Select a kind.");
      ok = false;
    } else {
      setFieldError(kindSelect, "");
    }

    if (!providerInput.value || providerKinds[providerInput.value] === undefined) {
      setFieldError(providerInput, "Select a provider from the list.");
      ok = false;
    } else {
      setFieldError(providerInput, "");
    }

    // Aggregator: no board, and the company name is derived from the
    // provider right here — the field is hidden, there's nothing for the
    // operator to have typed.
    if (kindSelect.value === "Aggregator") {
      boardInput.value = "";
      if (!companyInput.value.trim() && providerInput.value) {
        companyInput.value = displayName(providerInput.value);
      }
    } else if (kindSelect.value === "Career site") {
      boardInput.value = "";
    }

    if (kindSelect.value === "ATS platform" && !boardInput.value.trim()) {
      setFieldError(boardInput, "Board is required for an ATS platform.");
      ok = false;
    } else {
      setFieldError(boardInput, "");
    }

    if (companyInput.required && !companyInput.value.trim()) {
      setFieldError(companyInput, "Company is required.");
      ok = false;
    } else {
      setFieldError(companyInput, "");
    }

    if (!ok) e.preventDefault();
  });
})();

// The one add/edit schedule dialog (templates/schedule_modal.html), shared
// by every page that renders it. openScheduleModal is the single way in:
// the "+ Add schedule" button and every kebab Add/Edit item call it via
// [data-schedule-open], whose href (/schedules?open=...) stays the no-JS
// fallback. Saving POSTs in the background, refreshes the page's table in
// place and closes the dialog — it never navigates.
//
// A schedule is a list of run times. Each row is an hour and a minute in
// the viewer's timezone; tz_offset tells the server how to turn them into
// UTC. existing is null for an add, or {id, times} for an edit (times:
// minutes after 00:00 UTC).
function openScheduleModal(provider, existing) {
  var dialog = document.getElementById("schedule-dialog");
  if (!dialog) return;
  var form = dialog.querySelector("form");
  var providerSelect = form.elements["provider"];

  // Mirrors ensureProviderListed in handlers_schedules.go: the provider
  // being scheduled must be selectable even when it isn't in the list.
  if (provider && !Array.prototype.some.call(providerSelect.options, function (o) { return o.value === provider; })) {
    providerSelect.add(new Option(provider, provider));
  }
  // One schedule per provider: "Add" for a provider that has one edits it.
  if (!existing && provider) existing = scheduleOfProvider(provider);

  form.elements["id"].value = existing ? existing.id : "";
  providerSelect.value = provider || "";
  delete dialog.dataset.checked; // no row is called out until a save is tried
  setTimeRows(dialog, existing ? existing.times : []);
  // Remembered so the booked check leaves this schedule's own runs out.
  dialog.dataset.editTimes = existing ? existing.times.join(",") : "";
  // Fresh every time: schedules saved since the page loaded count too.
  scheduleLoad = null;
  updateTimeRows(dialog);
  fetch("/schedules/load", { headers: { "X-Board-Console-Fetch": "1" } })
    .then(function (r) { return r.ok ? r.json() : null; })
    .then(function (data) { if (data) { scheduleLoad = data; updateTimeRows(dialog); } })
    .catch(function () { /* no warning is shown rather than a wrong one */ });

  dialog.querySelector("h2").textContent = existing ? "Edit schedule" : "Add schedule";
  form.querySelector('button[type="submit"]').textContent = existing ? "Save changes" : "Save schedule";
  showScheduleError(dialog, "");
  setFieldError(providerSelect, "");
  dialog.showModal();
}

// The schedule a provider already has, read off the page's Edit links
// (Schedules table or Catalog kebab), or null.
function scheduleOfProvider(provider) {
  var link = Array.prototype.find.call(document.querySelectorAll("[data-schedule-open][data-id]"),
    function (a) { return a.dataset.provider === provider; });
  return link ? { id: link.dataset.id, times: parseTimes(link.dataset.times) } : null;
}

function parseTimes(s) {
  return (s || "").split(",").filter(Boolean).map(Number);
}

function tzShift() { return -new Date().getTimezoneOffset(); } // local = UTC + shift (minutes)
function toLocalMin(utc) { return ((utc + tzShift()) % 1440 + 1440) % 1440; }
function toUTCMin(local) { return ((local - tzShift()) % 1440 + 1440) % 1440; }

// Rebuild the rows from UTC minutes, in local time and in order — one empty
// row when there are none, ready to fill.
function setTimeRows(dialog, utcTimes) {
  var list = dialog.querySelector("[data-time-rows]");
  list.replaceChildren();
  var local = utcTimes.map(toLocalMin).sort(function (a, b) { return a - b; });
  if (!local.length) local = [-1];
  local.forEach(function (m) { addTimeRow(dialog, m); });
  dialog.querySelector("form").elements["tz_offset"].value = String(tzShift());
}

function addTimeRow(dialog, localMin) {
  var tpl = dialog.querySelector("[data-time-row-template]");
  var row = tpl.content.firstElementChild.cloneNode(true);
  if (localMin >= 0) {
    row.querySelector('[name="hour"]').value = String(Math.floor(localMin / 60));
    row.querySelector('[name="minute"]').value = String(localMin % 60);
  }
  dialog.querySelector("[data-time-rows]").appendChild(row);
  return row;
}

// A row's time in local minutes, or -1 while either half is unset.
function rowLocalMin(row) {
  var h = row.querySelector('[name="hour"]').value, m = row.querySelector('[name="minute"]').value;
  return h === "" || m === "" ? -1 : Number(h) * 60 + Number(m);
}

// A row with one half chosen and the other not — the one case a save
// refuses, since a fully empty row is just an unused one.
function rowHalfFilled(row) {
  return (row.querySelector('[name="hour"]').value === "") !== (row.querySelector('[name="minute"]').value === "");
}

// The day's crawls-starting-per-slot, as /schedules/load returned it when
// the modal last opened; null until it arrives.
var scheduleLoad = null;

// Per row, how taken its slot already is (this schedule's own runs left
// out): free, some of the capacity, or booked — only a warning, saving is
// still allowed and the crawl queues. A repeated time is flagged; the
// server keeps it once. Nothing here blocks: rows can be added freely, an
// empty one is simply ignored on save, and a half-filled one (hour but no
// minutes, or the reverse) is only called out once a save is attempted.
function updateTimeRows(dialog) {
  var rows = Array.prototype.slice.call(dialog.querySelectorAll(".time-row"));
  var load = scheduleLoad ? (scheduleLoad.load || []).slice() : null;
  var cap = scheduleLoad ? scheduleLoad.capacity : 0;
  if (load) {
    parseTimes(dialog.dataset.editTimes).forEach(function (t) {
      var s = Math.floor(t / 15) % 96;
      load[s] = Math.max(0, load[s] - 1);
    });
  }
  var seen = {}, filled = 0;
  rows.forEach(function (row, i) {
    var note = row.querySelector("[data-time-note]");
    var m = rowLocalMin(row), msg = "", state = "";
    row.querySelector(".time-index").textContent = String(i + 1);
    if (m < 0) {
      if (dialog.dataset.checked && rowHalfFilled(row)) {
        msg = row.querySelector('[name="hour"]').value === "" ? "pick the hour" : "pick the minutes";
        state = "invalid";
      }
    } else if (seen[m]) {
      msg = "same time twice";
      state = "dup";
    } else {
      seen[m] = true;
      filled++;
      if (load) {
        var n = load[Math.floor(toUTCMin(m) / 15) % 96];
        if (n >= cap) { msg = "booked · will queue"; state = "booked"; }
        else if (n > 0) { msg = n + " of " + cap + " taken"; state = "busy"; }
        else { msg = "free"; state = "free"; }
      }
    }
    row.dataset.state = state;
    note.textContent = msg;
    row.querySelector("[data-remove-time]").hidden = rows.length === 1;
  });
  var summary = dialog.querySelector("[data-times-summary]");
  if (summary) {
    var zone = "";
    try { zone = new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).formatToParts(new Date()).find(function (p) { return p.type === "timeZoneName"; }).value; } catch (e) { /* no zone name: just "your timezone" */ }
    summary.textContent = (filled ? filled + "× a day · " : "") + (zone ? "times in " + zone : "in your timezone");
  }
}

function showScheduleError(dialog, message) {
  var el = dialog.querySelector(".field-error");
  el.textContent = message;
  el.hidden = !message;
}

(function () {
  var dialog = document.getElementById("schedule-dialog");
  if (!dialog) return;
  var form = dialog.querySelector("form");
  var providerSelect = form.elements["provider"];

  document.addEventListener("click", function (e) {
    var trigger = e.target.closest("[data-schedule-open]");
    if (!trigger) return;
    e.preventDefault();
    var d = trigger.dataset;
    openScheduleModal(d.provider || "", d.id ? { id: d.id, times: parseTimes(d.times) } : null);
  });

  // Picking a provider that already has a schedule switches to editing it.
  providerSelect.addEventListener("change", function () {
    if (form.elements["id"].value) return;
    var existing = scheduleOfProvider(providerSelect.value);
    if (existing) openScheduleModal(providerSelect.value, existing);
  });

  // "+ Add another time" always adds a row, however many are unfilled.
  dialog.querySelector("[data-add-time]").addEventListener("click", function () {
    addTimeRow(dialog, -1).querySelector('[name="hour"]').focus();
    updateTimeRows(dialog);
  });
  dialog.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-remove-time]");
    if (!btn) return;
    btn.closest(".time-row").remove();
    updateTimeRows(dialog);
  });
  dialog.addEventListener("change", function (e) {
    if (e.target.closest(".time-row")) updateTimeRows(dialog);
  });

  if (dialog.dataset.openOnLoad === "true") {
    // A no-JS round-trip rendered the rows in UTC; show them locally.
    var utc = Array.prototype.map.call(dialog.querySelectorAll(".time-row"), rowLocalMin).filter(function (m) { return m >= 0; });
    setTimeRows(dialog, utc);
    updateTimeRows(dialog);
    dialog.showModal();
  }

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    var ok = true;

    if (!providerSelect.value) {
      setFieldError(providerSelect, "Select a provider.");
      ok = false;
    } else {
      setFieldError(providerSelect, "");
    }
    // Validation happens here, at the end: a half-filled row is named, an
    // empty one is ignored, and at least one time must be set.
    var rows = Array.prototype.slice.call(dialog.querySelectorAll(".time-row"));
    var half = [];
    rows.forEach(function (r, i) { if (rowHalfFilled(r)) half.push(i + 1); });
    var filled = rows.some(function (r) { return rowLocalMin(r) >= 0; });
    dialog.dataset.checked = "1";
    updateTimeRows(dialog);
    var msg = half.length ? (half.length === 1 ? "Time " + half[0] + " is" : "Times " + half.join(", ") + " are") +
        " missing the hour or the minutes." : filled ? "" : "Add at least one run time.";
    showScheduleError(dialog, msg);
    if (!ok || msg) return;

    form.elements["tz_offset"].value = String(tzShift());
    var provider = providerSelect.value;
    postForm(form)
      .then(function () {
        dialog.close();
        toast("Schedule saved for " + provider);
        return refreshLiveRegion();
      })
      .catch(function (err) { showScheduleError(dialog, err.message); });
  });
})();

// "Change time…" on a system job's row: the re-time dialog
// (templates/schedules.html) with its one hour/minute row in the viewer's
// timezone, saved in the background like a schedule.
(function () {
  var dialog = document.getElementById("system-time-dialog");
  if (!dialog) return;
  var form = dialog.querySelector("form");
  document.addEventListener("click", function (e) {
    var trigger = e.target.closest("[data-system-time]");
    if (!trigger) return;
    var local = toLocalMin(Number(trigger.dataset.minute));
    form.elements["key"].value = trigger.dataset.key;
    form.elements["hour"].value = String(Math.floor(local / 60));
    form.elements["minute"].value = String(local % 60);
    dialog.querySelector("[data-system-title]").textContent = "Change time — " + trigger.dataset.name;
    var zone = "";
    try { zone = new Intl.DateTimeFormat(undefined, { timeZoneName: "short" }).formatToParts(new Date()).find(function (p) { return p.type === "timeZoneName"; }).value; } catch (err) { /* just "your timezone" */ }
    dialog.querySelector("[data-system-zone]").textContent = zone ? "time in " + zone : "in your timezone";
    dialog.querySelector("[data-remove-time]").hidden = true;
    showScheduleError(dialog, "");
    dialog.showModal();
  });
  form.addEventListener("submit", function (e) {
    e.preventDefault();
    if (form.elements["hour"].value === "" || form.elements["minute"].value === "") {
      showScheduleError(dialog, "Pick the hour and the minutes.");
      return;
    }
    form.elements["tz_offset"].value = String(tzShift());
    postForm(form)
      .then(function () {
        dialog.close();
        toast("Time saved");
        return refreshLiveRegion();
      })
      .catch(function (err) { showScheduleError(dialog, err.message); });
  });
})();

// Activity page: one row per job (see jobs.go), polled while anything runs.
// When the server's fingerprint of the page differs from the one it was
// rendered with — a job appeared, a step joined one, a status changed — the
// table re-renders in place with open jobs and logs kept open; otherwise
// only durations and the open logs' growing output are patched, so a live
// crawl's log grows without the table jumping. Never a full page reload.
(function () {
  if (!document.getElementById("activity-table")) return;

  // The provider filter auto-submits (debounced) like the catalog search;
  // the status/action selects already submit on change (see the template).
  var filterForm = document.getElementById("activity-filters");
  var providerFilter = document.getElementById("activity-provider-filter");
  if (filterForm && providerFilter) {
    var filterDebounce = null;
    providerFilter.addEventListener("input", function () {
      clearTimeout(filterDebounce);
      filterDebounce = setTimeout(function () { filterForm.submit(); }, 300);
    });
  }

  function formatOutput(step) {
    var text = "stdout:\n" + step.stdout + "\n\nstderr:\n" + step.stderr;
    if (step.err) text += "\n\nerror: " + step.err;
    return text;
  }

  var timer = null;
  function schedulePoll(ms) {
    clearTimeout(timer);
    timer = setTimeout(poll, ms);
  }

  function poll() {
    // Same filters and page as the rendered table, so the server returns
    // the SAME jobs.
    fetch("/activity/status" + window.location.search)
      .then(function (r) { return r.json(); })
      .then(function (data) {
        var table = document.getElementById("activity-table");
        if (table && data.fingerprint !== table.dataset.fingerprint) {
          refreshKeepingOpen().finally(function () { schedulePoll(2000); });
          return;
        }
        (data.jobs || []).forEach(function (job) {
          var cell = document.querySelector('[data-job-duration="' + job.key + '"]');
          if (cell) cell.textContent = job.duration;
          (job.steps || []).forEach(function (step) {
            var d = document.querySelector('[data-step-duration="' + step.dom + '"]');
            if (d) d.textContent = step.duration;
            var detail = document.getElementById("detail-" + step.dom);
            if (detail && !detail.hidden) {
              detail.querySelector(".run-output").textContent = formatOutput(step);
            }
          });
        });
        // Idle, keep watching: a job a schedule, another tab or a click on
        // this one starts should appear within seconds, without a reload.
        schedulePoll(data.running ? 2000 : 5000);
      })
      .catch(function () { schedulePoll(3000); });
  }

  document.addEventListener("liveregion:refreshed", function () { schedulePoll(1000); });
  poll();
})();

// Kebab (⋮) row-action menus, on Providers and Schedules alike: click the
// button to toggle its own menu, click anywhere else (another kebab, a
// menu item, or outside entirely) to close every open one. One delegated
// listener covers every row, including rows a live-region refresh swapped
// in.
//
// An open menu is position: fixed at the button's on-screen rect, not
// absolute inside its row: .table-card clips its overflow (for its rounded
// corners), which cut the menu off in a short table. Fixed escapes that
// clipping, and the menu flips above the button when there's no room below.
(function () {
  function closeAll() {
    document.querySelectorAll(".kebab-menu").forEach(function (menu) { menu.hidden = true; });
  }

  function place(button, menu) {
    menu.hidden = false;
    var rect = button.getBoundingClientRect();
    var gap = 4, margin = 8;
    var top = rect.bottom + gap;
    if (top + menu.offsetHeight > window.innerHeight - margin &&
        rect.top - gap - menu.offsetHeight >= margin) {
      top = rect.top - gap - menu.offsetHeight;
    }
    var left = Math.min(rect.right - menu.offsetWidth, window.innerWidth - margin - menu.offsetWidth);
    menu.style.top = top + "px";
    menu.style.left = Math.max(margin, left) + "px";
  }

  document.addEventListener("click", function (e) {
    var toggle = e.target.closest("[data-kebab-toggle]");
    var menu = toggle && toggle.nextElementSibling;
    var wasOpen = menu && !menu.hidden;
    closeAll();
    if (menu && !wasOpen) place(toggle, menu);
  });

  // A fixed menu would stay put while its row scrolls away — close instead.
  window.addEventListener("scroll", closeAll, true);
  window.addEventListener("resize", closeAll);
})();

// The sidebar's Server card (templates/partials.html): disk, memory, CPU
// and Docker's build cache from /system/stats, every 5 seconds while the
// tab is visible. A meter with no reading stays hidden; a failed poll greys
// the "live" dot, so a number is never shown as current when it is not.
(function () {
  var card = document.querySelector("[data-server-card]");
  if (!card) return;
  var live = card.querySelector("[data-server-live]");
  var pruneBtn = card.querySelector("[data-prune]");
  var GB = 1024 * 1024 * 1024;
  var timer = null;

  function size(n) {
    return n >= GB ? (n / GB).toFixed(1) + " GB" : Math.round(n / 1024 / 1024) + " MB";
  }
  function meter(key, pct, detail, note) {
    var m = card.querySelector('[data-meter="' + key + '"]');
    if (pct == null) { m.hidden = true; return; }
    m.hidden = false;
    var p = Math.max(0, Math.min(100, pct));
    m.classList.toggle("warn", p >= 75 && p < 90);
    m.classList.toggle("crit", p >= 90);
    m.querySelector("[data-pct]").textContent = Math.round(p) + "%";
    m.querySelector("[data-detail]").textContent = detail;
    m.querySelector("[data-bar]").style.width = p + "%";
    var n = m.querySelector("[data-note]");
    n.textContent = note || "";
    n.hidden = !note;
  }

  function render(s) {
    var d = s.disk;
    meter("disk", d && d.total ? d.used / d.total * 100 : null,
      d ? size(d.used) + " of " + size(d.total) : "",
      d && s.reindexFloorGB && d.free < s.reindexFloorGB * GB
        ? size(d.free) + " free — reindex needs " + s.reindexFloorGB + " GB" : "");
    var m = s.mem;
    meter("mem", m && m.total ? m.used / m.total * 100 : null, m ? size(m.used) + " of " + size(m.total) : "");
    meter("cpu", s.cpu ? s.cpu.pct : null, s.cpu ? s.cpu.cores + (s.cpu.cores === 1 ? " core" : " cores") : "");

    var cache = card.querySelector("[data-cache]");
    var off = card.querySelector("[data-cache-off]");
    if (s.buildCache) {
      cache.hidden = false;
      off.hidden = true;
      card.querySelector("[data-cache-total]").textContent = size(s.buildCache.total);
      var r = s.buildCache.reclaimable;
      card.querySelector("[data-cache-sub]").textContent = r > 0 ? size(r) + " can be cleared" : "Nothing to clear";
      if (!pruneBtn.dataset.busy) pruneBtn.disabled = r <= 0;
    } else {
      cache.hidden = true;
      off.hidden = false;
      off.textContent = s.docker ? "Build cache: Docker not reachable" : "Build cache: Docker not connected";
      off.title = s.dockerError || "Set DOCKER_PROXY_URL (see docker-compose.yml) to show and clear it.";
    }
    card.hidden = false;
  }

  function poll() {
    clearTimeout(timer);
    if (document.hidden) return; // resumed by visibilitychange
    fetch("/system/stats", { headers: { "X-Board-Console-Fetch": "1" } })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
      .then(function (s) {
        render(s);
        live.classList.remove("stale");
        live.textContent = "live";
        live.title = "Updated every 5 seconds";
      })
      .catch(function () {
        live.classList.add("stale");
        live.textContent = "stale";
        live.title = "Could not refresh — showing the last reading";
      })
      .finally(function () { timer = setTimeout(poll, 5000); });
  }
  document.addEventListener("visibilitychange", function () { if (!document.hidden) poll(); });
  poll();

  pruneBtn.addEventListener("click", function () {
    confirmDialog("Deletes Docker's cached image build steps only. Containers, the images they run and all data are untouched; the next image build is just slower.",
      { title: "Clear build cache?", confirmLabel: "Clear cache" })
      .then(function (ok) {
        if (!ok) return;
        pruneBtn.dataset.busy = "1";
        pruneBtn.disabled = true;
        pruneBtn.textContent = "Clearing…";
        return fetch("/system/build-cache/prune", { method: "POST", headers: { "X-Board-Console-Fetch": "1" } })
          .then(function (r) {
            return r.json().catch(function () { return {}; }).then(function (body) {
              if (!r.ok) throw new Error(body.error || "Request failed (" + r.status + ").");
              toast("Cleared " + (body.reclaimedText || "the build cache"));
            });
          })
          .catch(function (err) { toast(err.message, true); })
          .finally(function () {
            delete pruneBtn.dataset.busy;
            pruneBtn.textContent = "Clear build cache";
            poll();
          });
      });
  });
})();
