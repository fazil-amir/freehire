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
        if (fresh[i]) region.innerHTML = fresh[i].innerHTML;
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
    el.textContent = pad(d.getDate()) + "/" + pad(d.getMonth() + 1) + "/" + d.getFullYear() + " - " +
      pad(d.getHours()) + ":" + pad(d.getMinutes()) + ":" + pad(d.getSeconds());
    el.title = d.toString();
  });
}
localizeTimes();
document.addEventListener("liveregion:refreshed", localizeTimes);

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
// existing is null for an add, or {id, intervalValue, intervalUnit,
// reindexAfter} for an edit.
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

  form.elements["id"].value = existing ? existing.id : "";
  providerSelect.value = provider || "";
  form.elements["interval_value"].value = existing ? existing.intervalValue : "30";
  form.elements["interval_unit"].value = existing ? existing.intervalUnit : "minutes";
  form.elements["reindex_after"].checked = !!(existing && existing.reindexAfter);

  dialog.querySelector("h2").textContent = existing ? "Edit schedule" : "Add schedule";
  form.querySelector('button[type="submit"]').textContent = existing ? "Save changes" : "Save schedule";
  showScheduleError(dialog, "");
  setFieldError(providerSelect, "");
  setFieldError(form.elements["interval_value"], "");
  dialog.showModal();
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
  var valueInput = form.elements["interval_value"];
  var unitSelect = form.elements["interval_unit"];

  document.addEventListener("click", function (e) {
    var trigger = e.target.closest("[data-schedule-open]");
    if (!trigger) return;
    e.preventDefault();
    var d = trigger.dataset;
    openScheduleModal(d.provider || "", d.id ? {
      id: d.id,
      intervalValue: d.intervalValue,
      intervalUnit: d.intervalUnit,
      reindexAfter: d.reindexAfter === "true"
    } : null);
  });

  if (dialog.dataset.openOnLoad === "true") dialog.showModal();

  var minutesPerUnit = { minutes: 1, hours: 60, days: 1440 };

  form.addEventListener("submit", function (e) {
    e.preventDefault();
    var ok = true;

    if (!providerSelect.value) {
      setFieldError(providerSelect, "Select a provider.");
      ok = false;
    } else {
      setFieldError(providerSelect, "");
    }

    var minutes = (minutesPerUnit[unitSelect.value] || 1) * Number(valueInput.value || 0);
    if (!valueInput.value || minutes < 2) {
      setFieldError(valueInput, "Interval must be at least 2 minutes.");
      ok = false;
    } else {
      setFieldError(valueInput, "");
    }

    if (!ok) return;

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
