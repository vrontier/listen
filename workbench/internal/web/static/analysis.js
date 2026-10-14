// The live part of an analysis page: a scrolling spectrogram, the list of
// events and the motifs. It reads the Workbench's event stream for this
// analysis, which the Workbench takes from the engine.
(function () {
  "use strict";
  const root = document.getElementById("live");
  if (!root) return;
  const livePath = root.dataset.live;
  const dataPath = root.dataset.data;
  const canvas = document.getElementById("spectrogram");
  const ctx = canvas.getContext("2d");
  const statusEl = document.getElementById("status");
  const levelEl = document.getElementById("level");
  const eventsEl = document.getElementById("events");
  const motifsBody = document.querySelector("#motifs tbody");
  let range = null;

  // Colour map from quiet (dark blue) to loud (pale yellow).
  function colour(v) {
    v = Math.max(0, Math.min(1, v));
    const r = Math.round(255 * Math.min(1, Math.max(0, 1.6 * v - 0.3)));
    const g = Math.round(255 * Math.min(1, Math.max(0, 1.4 * v - 0.15)) * 0.95);
    const b = Math.round(255 * (0.25 + 0.5 * Math.sin(Math.PI * Math.min(1, v * 1.1))));
    return "rgb(" + r + "," + g + "," + b + ")";
  }

  function fmtHz(hz) {
    return hz >= 1000 ? (hz / 1000).toFixed(hz >= 10000 ? 0 : 1) + " kHz" : Math.round(hz) + " Hz";
  }

  // One column per spectrum event, scrolled in from the right.
  function drawSpectrum(p) {
    const w = canvas.width, h = canvas.height;
    if (!range || range.min !== p.min_hz || range.max !== p.max_hz) {
      range = { min: p.min_hz, max: p.max_hz };
      document.getElementById("axis-min").textContent = fmtHz(p.min_hz);
      document.getElementById("axis-max").textContent = fmtHz(p.max_hz);
    }
    const col = 3;
    ctx.drawImage(canvas, col, 0, w - col, h, 0, 0, w - col, h);
    const n = p.bands.length;
    for (let i = 0; i < n; i++) {
      const y0 = h - Math.round(((i + 1) * h) / n);
      const y1 = h - Math.round((i * h) / n);
      ctx.fillStyle = colour(p.bands[i]);
      ctx.fillRect(w - col, y0, col, y1 - y0);
    }
    const lmin = Math.log(p.min_hz), lmax = Math.log(p.max_hz);
    ctx.fillStyle = "#ff5a5a";
    for (const pk of p.peaks || []) {
      const y = h - ((Math.log(pk.hz) - lmin) / (lmax - lmin)) * h;
      ctx.fillRect(w - col, Math.round(y) - 1, col, 2);
    }
  }

  function time(ts) {
    const d = new Date(ts);
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }

  function describe(e) {
    const p = e.payload || {};
    switch (e.type) {
      case "event.transient":
        return ["Transient", "strength " + p.strength + ", centre " + fmtHz(p.centroid_hz)];
      case "event.resonance":
        if (p.status === "update") return null;
        return ["Resonance " + p.status, fmtHz(p.fundamental_hz) + (p.motif_id ? " · " + p.motif_id : "") +
          (p.status === "end" ? " · " + p.duration_s + " s" : "")];
      case "motif.detected":
        return ["Motif found", p.motif_id + " (" + p.kind + ")", true];
      case "motif.returned":
        return ["Motif returned", p.motif_id + " after " + p.absent_s + " s", true];
      case "narrative.update":
        return ["Interpretation", p.text || ""];
    }
    return null;
  }

  function addEvent(e) {
    const d = describe(e);
    if (!d) return;
    const li = document.createElement("li");
    const t = document.createElement("time");
    t.textContent = time(e.timestamp);
    const what = document.createElement("span");
    what.textContent = d[0];
    if (d[2]) what.className = "ev-motif";
    const detail = document.createElement("span");
    detail.className = "meta";
    detail.textContent = d[1];
    li.append(t, what, detail);
    eventsEl.prepend(li);
    while (eventsEl.children.length > 150) eventsEl.lastChild.remove();
  }

  function setStatus(s) {
    statusEl.textContent = s.stream === "connected" ? "connected" : s.stream;
    statusEl.className = "conn conn-" + s.stream;
  }

  async function loadMotifs() {
    try {
      const r = await fetch(dataPath + "motifs", { credentials: "same-origin" });
      if (!r.ok) return;
      const data = await r.json();
      motifsBody.textContent = "";
      for (const m of data.motifs || []) {
        const tr = document.createElement("tr");
        const cells = [m.motif_id, m.kind, String(m.occurrences) + "×", Math.round(m.presence_s) + " s",
          m.last_seen ? time(m.last_seen) : "—"];
        for (const c of cells) {
          const td = document.createElement("td");
          td.textContent = c;
          tr.append(td);
        }
        motifsBody.append(tr);
      }
      if (!motifsBody.children.length) {
        const tr = document.createElement("tr");
        const td = document.createElement("td");
        td.colSpan = 5;
        td.className = "meta";
        td.textContent = "None yet.";
        tr.append(td);
        motifsBody.append(tr);
      }
    } catch (e) { /* the next poll tries again */ }
  }

  async function loadState() {
    try {
      const r = await fetch(dataPath + "state", { credentials: "same-origin" });
      if (!r.ok) return;
      const s = await r.json();
      for (const e of (s.recent_events || [])) addEvent(e);
      if (s.system) setStatus(s.system.payload);
    } catch (e) { /* live events follow anyway */ }
  }

  let lastSeq = 0;
  function connect() {
    const es = new EventSource(livePath);
    es.addEventListener("signal.spectrum", (m) => drawSpectrum(JSON.parse(m.data).payload));
    es.addEventListener("signal.frame", (m) => {
      const e = JSON.parse(m.data);
      levelEl.textContent = "level " + e.payload.level_db + " dB";
      if (lastSeq && e.sequence > lastSeq + 40) levelEl.textContent += " · some events were missed";
      lastSeq = e.sequence;
    });
    es.addEventListener("system.status", (m) => setStatus(JSON.parse(m.data).payload));
    for (const t of ["event.transient", "event.resonance", "motif.detected", "motif.returned", "narrative.update"]) {
      es.addEventListener(t, (m) => {
        const e = JSON.parse(m.data);
        addEvent(e);
        if (t.startsWith("motif.")) loadMotifs();
      });
    }
    es.onerror = () => { statusEl.textContent = "reconnecting…"; statusEl.className = "conn conn-reconnecting"; };
    es.onopen = () => { if (statusEl.textContent === "reconnecting…") statusEl.textContent = "connecting…"; };
  }

  ctx.fillStyle = getComputedStyle(canvas).backgroundColor;
  ctx.fillRect(0, 0, canvas.width, canvas.height);
  loadState().then(connect);
  loadMotifs();
  setInterval(loadMotifs, 15000);

  // Rule rows: offer one more empty row on demand.
  const add = document.getElementById("add-rule");
  const tbody = document.querySelector("#rules tbody");
  const count = document.querySelector("input[name=rule_count]");
  if (add && tbody && count) {
    add.hidden = false;
    add.addEventListener("click", () => {
      const i = Number(count.value);
      const row = tbody.lastElementChild.cloneNode(true);
      for (const el of row.querySelectorAll("input, select")) {
        el.name = el.name.replace(/_\d+$/, "_" + i);
        if (el.type === "checkbox") el.checked = false; else if (el.tagName === "INPUT") el.value = "";
      }
      for (const el of row.querySelectorAll(".field-error, code")) el.textContent = "";
      tbody.append(row);
      count.value = String(i + 1);
    });
  }
})();
