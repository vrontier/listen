// The MCP page's log of accesses: filters and sorting in the browser, over
// the notes the page holds. The choice is kept in the address, so that a
// link can choose a token and "Older" keeps the filters.
(function () {
  "use strict";
  const table = document.getElementById("log");
  const form = document.getElementById("log-filter");
  if (!table || !form) return;
  const count = document.getElementById("log-count");
  const entries = Array.from(table.tBodies);
  const failed = { error: true, refused: true, cut: true };

  // The calls in the log, for the "Call" choice.
  const whats = Array.from(new Set(entries.map((e) => e.dataset.what))).sort();
  for (const w of whats) form.elements.what.add(new Option(w, w));

  const numeric = { at: true, ms: true, bytes: true };
  const sortKey = { token: "tokenName" };
  let sort = { key: "at", dir: -1 };

  const params = new URLSearchParams(location.search);
  if (params.get("failed") === "on" && !params.get("status")) params.set("status", "failed"); // the old form's way
  for (const name of ["q", "token", "via", "what", "status"]) {
    const v = params.get(name);
    const el = form.elements[name];
    if (v && el) {
      if (el.tagName === "SELECT" && !Array.from(el.options).some((o) => o.value === v)) el.add(new Option(v, v));
      el.value = v;
    }
  }
  const s = /^(-?)(at|token|via|what|status|ms|bytes)$/.exec(params.get("sort") || "");
  if (s) sort = { key: s[2], dir: s[1] ? -1 : 1 };
  form.hidden = false;

  function matches(e, f) {
    const d = e.dataset;
    if (f.token && d.token !== f.token) return false;
    if (f.via && d.via !== f.via) return false;
    if (f.what && d.what !== f.what) return false;
    if (f.status === "failed" ? !failed[d.status] : f.status && d.status !== f.status) return false;
    return !f.q || e.textContent.toLowerCase().includes(f.q);
  }

  function apply() {
    const f = {
      q: form.elements.q.value.trim().toLowerCase(),
      token: form.elements.token.value,
      via: form.elements.via.value,
      what: form.elements.what.value,
      status: form.elements.status.value,
    };
    const prop = sortKey[sort.key] || sort.key;
    const sorted = entries.slice().sort((a, b) => {
      let x = a.dataset[prop], y = b.dataset[prop];
      if (numeric[sort.key]) { x = Number(x); y = Number(y); }
      const c = x < y ? -1 : x > y ? 1 : Number(b.dataset.at) - Number(a.dataset.at);
      return c * sort.dir;
    });
    let shown = 0;
    for (const e of sorted) {
      e.hidden = !matches(e, f);
      if (!e.hidden) shown++;
      table.appendChild(e);
    }
    for (const th of table.tHead.rows[0].cells) {
      if (th.dataset.key === sort.key) th.setAttribute("aria-sort", sort.dir < 0 ? "descending" : "ascending");
      else th.removeAttribute("aria-sort");
    }
    count.textContent = shown === entries.length
      ? entries.length + (entries.length === 1 ? " access" : " accesses")
      : shown + " of " + entries.length + " accesses shown";

    // Into the address, without reloading.
    const p = new URLSearchParams(location.search);
    p.delete("failed");
    for (const [k, v] of Object.entries(f)) {
      const raw = form.elements[k].value.trim();
      if (raw) p.set(k, raw); else p.delete(k);
    }
    if (sort.key === "at" && sort.dir < 0) p.delete("sort");
    else p.set("sort", (sort.dir < 0 ? "-" : "") + sort.key);
    const qs = p.toString();
    history.replaceState(null, "", location.pathname + (qs ? "?" + qs : "") + location.hash);
    for (const a of document.querySelectorAll('a[href^="/account/mcp?"][href*="before="], a[data-newest]')) {
      const u = new URL(a.href);
      const before = u.searchParams.get("before");
      const n = new URLSearchParams(p);
      n.delete("before");
      if (before) n.set("before", before);
      u.search = n.toString();
      a.href = u.pathname + u.search + u.hash;
    }
  }

  for (const th of table.tHead.rows[0].cells) {
    const b = th.querySelector("button");
    if (!b) continue;
    b.addEventListener("click", () => {
      const key = th.dataset.key;
      // Times, durations and sizes start with the largest; words from A.
      sort = sort.key === key ? { key, dir: -sort.dir } : { key, dir: numeric[key] ? -1 : 1 };
      apply();
    });
  }
  let typing;
  form.addEventListener("input", () => { clearTimeout(typing); typing = setTimeout(apply, 120); });
  form.addEventListener("change", apply);
  form.addEventListener("submit", (ev) => ev.preventDefault());
  form.addEventListener("reset", () => setTimeout(() => { sort = { key: "at", dir: -1 }; apply(); }));
  apply();
})();
