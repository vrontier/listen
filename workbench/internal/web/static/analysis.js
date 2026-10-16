// The live part of an analysis page, once per source: the instrument (the
// spectral landscape of landscape.js, or a scrolling spectrogram), the list
// of events and the motifs, read from the Workbench's
// event stream for that source of the analysis. And two helpers of the
// settings form: adding a rule row, and copying one source's settings to the
// other sources.
(function () {
  "use strict";

  // The spectrogram's colour scale, "jet" as in the usual scientific
  // spectrograms: dark blue (0) through cyan, yellow and red to dark red (1).
  function colour(v) {
    v = Math.max(0, Math.min(1, v));
    const c = (x) => Math.round(255 * Math.max(0, Math.min(1, 1.5 - Math.abs(4 * v - x))));
    return "rgb(" + c(3) + "," + c(2) + "," + c(1) + ")";
  }

  const HZ_TICKS = [5, 10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 20000];

  // The words for a connection's state, from the page (statuses.go).
  function connWord(state) {
    const w = (window.LOW_CONN || {})[state];
    return w || { Word: state ? state.charAt(0).toUpperCase() + state.slice(1) : "Unknown", Tone: "neutral" };
  }

  function fmtHz(hz) {
    return hz >= 1000 ? (hz / 1000).toFixed(hz >= 10000 ? 0 : 1) + " kHz" : Math.round(hz) + " Hz";
  }

  // Times are shown in UTC, to the millisecond the engine stamps.
  function time(ts) {
    const d = new Date(ts);
    return isNaN(d) ? "" : d.toISOString().slice(11, 23);
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

  // One source's live view.
  function live(root) {
    const livePath = root.dataset.live, dataPath = root.dataset.data;
    const canvas = root.querySelector("canvas.spectrogram");
    const ctx = canvas.getContext("2d");
    const fig = root.querySelector(".instrument");
    const scape = window.LowLandscape ? new window.LowLandscape(fig.querySelector("canvas.landscape"), fig.querySelector(".readout"), fig.querySelector(".summary")) : null;
    const yTicks = fig.querySelector(".y-ticks"), xTicks = fig.querySelector(".x-ticks"), xNow = fig.querySelector(".x-now");
    let xSpan = 0;
    let paused = false, lastSpec = 0, frameMs = 0;

    // The view: landscape or spectrogram, remembered in this browser.
    function show(view) {
      for (const b of fig.querySelectorAll("[data-show]")) b.setAttribute("aria-pressed", String(b.dataset.show === view));
      for (const p of fig.querySelectorAll(".plot")) p.hidden = p.dataset.view !== view;
      if (scape) scape.show(view === "landscape");
    }
    let view = "landscape";
    try { view = localStorage.getItem("low-view") || view; } catch (e) {}
    if (!scape) view = "spectrogram";
    show(view);
    for (const b of fig.querySelectorAll("[data-show]")) {
      b.addEventListener("click", () => {
        show(b.dataset.show);
        try { localStorage.setItem("low-view", b.dataset.show); } catch (e) {}
      });
    }
    // Pause freezes the drawing; the analysis, and what the page collects, go on.
    const pauseBtn = fig.querySelector(".pause"), pausedNote = fig.querySelector(".paused-note");
    pauseBtn.addEventListener("click", () => {
      paused = !paused;
      pauseBtn.setAttribute("aria-pressed", String(paused));
      pauseBtn.textContent = paused ? "Resume" : "Pause";
      pausedNote.hidden = !paused;
      if (scape) scape.pause(paused);
    });
    // The colour scale of the spectrogram, as its colour bar.
    const stops = [];
    for (let i = 0; i <= 16; i++) stops.push(colour(i / 16));
    fig.querySelector(".cbar-ramp").style.background = "linear-gradient(to top, " + stops.join(", ") + ")";

    // The axes' ticks: frequencies on the log scale, seconds before now.
    function tick(box, text, prop, pct) {
      const s = document.createElement("span");
      s.textContent = text;
      s.style[prop] = pct + "%";
      box.append(s);
    }
    function frequencyTicks(min, max) {
      yTicks.textContent = "";
      const lmin = Math.log(min), lmax = Math.log(max);
      for (const hz of HZ_TICKS) {
        if (hz < min || hz > max) continue;
        tick(yTicks, hz >= 1000 ? hz / 1000 + "k" : String(hz), "top", (1 - (Math.log(hz) - lmin) / (lmax - lmin)) * 100);
      }
    }
    function timeTicks(span) {
      const step = [2, 5, 10, 15, 30, 60, 120].find((s) => span / s <= 6) || 300;
      xTicks.textContent = "";
      for (let t = 0; t <= span; t += step) tick(xTicks, t === 0 ? "0" : "−" + t, "left", (1 - t / span) * 100);
    }
    const statusEl = root.querySelector(".status");
    const levelEl = root.querySelector(".level");
    const clockEl = root.querySelector(".clock");
    const eventsEl = root.querySelector(".events");
    const motifsBody = root.querySelector(".motifs tbody");
    let range = null, lastSeq = 0;

    function drawSpectrum(p) {
      const w = canvas.width, h = canvas.height;
      if (!range || range.min !== p.min_hz || range.max !== p.max_hz) {
        range = { min: p.min_hz, max: p.max_hz };
        frequencyTicks(p.min_hz, p.max_hz);
      }
      const col = 3;
      ctx.drawImage(canvas, col, 0, w - col, h, 0, 0, w - col, h);
      const n = p.bands.length;
      for (let i = 0; i < n; i++) {
        const y0 = h - Math.round(((i + 1) * h) / n), y1 = h - Math.round((i * h) / n);
        ctx.fillStyle = colour(p.bands[i]);
        ctx.fillRect(w - col, y0, col, y1 - y0);
      }
      const lmin = Math.log(p.min_hz), lmax = Math.log(p.max_hz);
      ctx.fillStyle = "#ffffff";
      for (const pk of p.peaks || []) {
        const y = h - ((Math.log(pk.hz) - lmin) / (lmax - lmin)) * h;
        ctx.fillRect(w - col, Math.round(y) - 1, col, 2);
      }
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
      const w = connWord(s.stream);
      statusEl.textContent = w.Word;
      statusEl.className = "status conn conn-" + w.Tone;
    }

    async function loadMotifs() {
      try {
        const r = await fetch(dataPath + "motifs", { credentials: "same-origin" });
        if (!r.ok) return;
        const data = await r.json();
        motifsBody.textContent = "";
        for (const m of data.motifs || []) {
          const tr = document.createElement("tr");
          for (const c of [m.motif_id, m.kind, String(m.occurrences) + "×", Math.round(m.presence_s) + " s", m.last_seen ? time(m.last_seen) : "—"]) {
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

    function connect() {
      const es = new EventSource(livePath);
      es.addEventListener("signal.spectrum", (m) => {
        const e = JSON.parse(m.data);
        if (scape) scape.push(e.payload, e.timestamp);
        // How much time the spectrogram's width holds, from the spectra's pace.
        const now = Date.now();
        if (lastSpec) frameMs = frameMs ? frameMs * 0.95 + (now - lastSpec) * 0.05 : now - lastSpec;
        lastSpec = now;
        if (frameMs) {
          const span = Math.round((canvas.width / 3) * frameMs / 1000);
          if (span !== xSpan) { xSpan = span; timeTicks(span); }
        }
        const at = new Date(e.timestamp);
        if (!paused && !isNaN(at)) xNow.textContent = " (0 = " + at.toISOString().slice(11, 19) + " UTC)";
        if (!paused) drawSpectrum(e.payload);
      });
      es.addEventListener("signal.frame", (m) => {
        const e = JSON.parse(m.data);
        clockEl.textContent = time(e.timestamp) + " UTC" + (e.time_source ? " (" + e.time_source + " time)" : "");
        levelEl.textContent = "level " + e.payload.level_db + " dB";
        if (lastSeq && e.sequence > lastSeq + 40) levelEl.textContent += " · some events were missed";
        lastSeq = e.sequence;
      });
      es.addEventListener("system.status", (m) => setStatus(JSON.parse(m.data).payload));
      for (const t of ["event.transient", "event.resonance", "motif.detected", "motif.returned", "narrative.update"]) {
        es.addEventListener(t, (m) => {
          addEvent(JSON.parse(m.data));
          if (t.startsWith("motif.")) loadMotifs();
        });
      }
      es.onerror = () => { statusEl.textContent = "Reconnecting…"; statusEl.className = "status conn conn-warn"; };
    }

    ctx.fillStyle = getComputedStyle(canvas).backgroundColor;
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    loadState().then(connect);
    loadMotifs();
    setInterval(loadMotifs, 15000);
  }

  for (const root of document.querySelectorAll(".live[data-live]")) live(root);

  // --- the settings form ---

  const fieldsets = Array.from(document.querySelectorAll(".source-settings"));

  function emptyRow(row, prefix, i) {
    for (const el of row.querySelectorAll("input, select")) {
      el.name = el.name.replace(/^s\d+_/, prefix).replace(/_\d+$/, "_" + i);
    }
    for (const el of row.querySelectorAll(".field-error")) el.remove();
    const saved = row.querySelector(".saved-as");
    if (saved) saved.innerHTML = '<span class="meta">—</span>';
  }

  for (const fs of fieldsets) {
    const prefix = fs.dataset.prefix;
    const tbody = fs.querySelector("table.rules tbody");
    const count = fs.querySelector(".rule-count");
    const add = fs.querySelector(".add-rule");
    if (add && tbody && count) {
      add.hidden = false;
      add.addEventListener("click", () => {
        const i = Number(count.value);
        const row = tbody.lastElementChild.cloneNode(true);
        emptyRow(row, prefix, i);
        for (const el of row.querySelectorAll("input")) {
          if (el.type === "checkbox") el.checked = false; else el.value = "";
        }
        tbody.append(row);
        count.value = String(i + 1);
      });
    }

    // Copy this source's settings into every other source's part of the
    // form. Nothing is saved until the form is.
    const copy = fs.querySelector(".copy-settings");
    if (copy) {
      copy.hidden = false;
      copy.addEventListener("click", () => {
        for (const other of fieldsets) {
          if (other === fs) continue;
          const op = other.dataset.prefix;
          for (const k of ["min_hz", "max_hz", "transient_k", "transient_gap_s"]) {
            other.querySelector('[name="' + op + k + '"]').value = fs.querySelector('[name="' + prefix + k + '"]').value;
          }
          const mode = fs.querySelector('[name="' + prefix + 'memory_mode"]:checked');
          if (mode) other.querySelector('[name="' + op + 'memory_mode"][value="' + mode.value + '"]').checked = true;
          const otherBody = other.querySelector("table.rules tbody");
          otherBody.textContent = "";
          Array.from(tbody.children).forEach((src, i) => {
            const row = src.cloneNode(true);
            emptyRow(row, op, i);
            // cloneNode keeps attributes, not what was typed or chosen since.
            const from = src.querySelectorAll("input, select"), to = row.querySelectorAll("input, select");
            from.forEach((el, j) => { if (el.type === "checkbox") to[j].checked = el.checked; else to[j].value = el.value; });
            otherBody.append(row);
          });
          other.querySelector(".rule-count").value = String(tbody.children.length);
        }
        copy.textContent = "Copied; save to keep it";
        setTimeout(() => { copy.textContent = "Copy these settings to all sources"; }, 3000);
      });
    }
  }
})();
