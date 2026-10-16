// The comparison of an analysis's sources: one lane per source on one time
// axis, the last ten minutes of activity per second, the group's status and
// the latest correlations, read from the Workbench's event stream of the
// analysis's engine group.
(function () {
  "use strict";

  const root = document.querySelector(".compare[data-live]");
  if (!root) return;
  const WINDOW = 600; // seconds shown
  const canvas = root.querySelector("canvas.lanes");
  const ctx = canvas.getContext("2d");
  const statusEl = root.querySelector(".status");
  const infoEl = root.querySelector(".compare-status");
  const corrBody = root.querySelector(".correlations tbody");
  const lanes = Array.from(root.querySelectorAll(".lane-names li")).map((li) => ({ stream: li.dataset.stream, name: li.textContent }));
  const names = Object.fromEntries(lanes.map((l) => [l.stream, l.name]));
  const seconds = new Map(); // ms → {stream: values or null}
  let latest = 0, metric = "impulse_rate";

  const laneH = 56, axisH = 22;
  canvas.height = lanes.length * laneH + axisH;
  canvas.style.height = canvas.height + "px";

  function css(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }

  function utc(ms) { return new Date(ms).toISOString().slice(11, 16); }

  function addActivity(e) {
    const p = e.payload || {};
    const t = Date.parse(p.second || e.timestamp);
    if (isNaN(t)) return;
    const row = {};
    for (const m of p.members || []) row[m.stream] = m.present ? m : null;
    seconds.set(t, row);
    if (t > latest) latest = t;
    for (const k of seconds.keys()) if (k <= latest - WINDOW * 1000) seconds.delete(k);
  }

  function draw() {
    const w = canvas.width, h = canvas.height;
    const text = css("--text"), muted = css("--muted"), line = css("--line"), accent = css("--accent");
    ctx.clearRect(0, 0, w, h);
    ctx.font = "11px 'IBM Plex Mono', ui-monospace, monospace";
    const end = latest || Date.now();
    const start = end - WINDOW * 1000;
    const x = (t) => ((t - start) / (WINDOW * 1000)) * w;
    const bw = Math.max(1, w / WINDOW);
    lanes.forEach((lane, i) => {
      const top = i * laneH;
      let lo = Infinity, hi = -Infinity;
      for (const row of seconds.values()) {
        const v = row[lane.stream];
        if (v && v[metric] != null) { lo = Math.min(lo, v[metric]); hi = Math.max(hi, v[metric]); }
      }
      ctx.strokeStyle = line;
      ctx.beginPath(); ctx.moveTo(0, top + laneH - 0.5); ctx.lineTo(w, top + laneH - 0.5); ctx.stroke();
      if (hi > -Infinity) {
        const span = hi - lo || 1;
        ctx.fillStyle = accent;
        for (const [t, row] of seconds) {
          const v = row[lane.stream];
          if (!v || v[metric] == null) continue;
          const bh = 4 + ((v[metric] - lo) / span) * (laneH - 22);
          ctx.fillRect(x(t) - bw, top + laneH - 1 - bh, bw, bh);
        }
      }
      ctx.fillStyle = text;
      ctx.fillText(lane.name, 6, top + 13);
      ctx.fillStyle = muted;
      if (hi > -Infinity) ctx.fillText(fmt(lo) + " – " + fmt(hi), 6, top + 26);
    });
    ctx.fillStyle = muted;
    ctx.strokeStyle = line;
    for (let t = Math.ceil(start / 60000) * 60000; t <= end; t += 60000) {
      const px = Math.round(x(t)) + 0.5;
      ctx.beginPath(); ctx.moveTo(px, 0); ctx.lineTo(px, h - axisH); ctx.stroke();
      if (px > 20 && px < w - 20) ctx.fillText(utc(t), px - 16, h - 6);
    }
    ctx.fillText("UTC", w - 26, h - 6);
  }

  function fmt(v) {
    if (metric === "level_db") return v.toFixed(1) + " dB";
    if (metric === "flux") return v.toFixed(2);
    return v.toFixed(0) + "/s";
  }

  function showStatus(p) {
    if (!p) return;
    const parts = [];
    if (p.behind_s != null) parts.push(p.behind_s + " s behind");
    parts.push(p.time_source === "stream" ? "station time" : "wall time");
    for (const m of p.members || []) if (m.state !== "present") parts.push((names[m.stream] || m.stream) + " " + m.state);
    infoEl.textContent = parts.join(" · ");
  }

  function showCorrelation(p) {
    if (!p || !p.pairs) return;
    corrBody.textContent = "";
    for (const c of p.pairs) {
      const tr = document.createElement("tr");
      const above = c.r == null || c.baseline_r == null ? "not yet" : Math.abs(c.r) - Math.abs(c.baseline_r) >= 0.2 ? "yes" : "no";
      const cells = [
        (names[c.a] || c.a) + " – " + (names[c.b] || c.b),
        c.r == null ? "—" : c.r.toFixed(2),
        c.lag_s == null ? "—" : (c.lag_s > 0 ? "+" : "") + c.lag_s + " s",
        c.baseline_r == null ? "—" : c.baseline_r.toFixed(2),
        above,
      ];
      cells.forEach((v, i) => {
        const td = document.createElement("td");
        td.textContent = v;
        if (i === 4 && v === "yes") td.className = "above";
        tr.append(td);
      });
      corrBody.append(tr);
    }
  }

  async function loadState() {
    try {
      const r = await fetch(root.dataset.state, { credentials: "same-origin" });
      if (!r.ok) return;
      const s = await r.json();
      for (const e of s.activity || []) addActivity(e);
      if (s.status) showStatus(s.status.payload);
      if (s.correlation) showCorrelation(s.correlation.payload);
      draw();
    } catch (e) { /* live events follow anyway */ }
  }

  function connect() {
    const es = new EventSource(root.dataset.live);
    es.onopen = () => { statusEl.textContent = "Live"; statusEl.className = "status conn conn-ok"; };
    es.addEventListener("group.activity", (m) => { addActivity(JSON.parse(m.data)); draw(); });
    es.addEventListener("group.status", (m) => showStatus(JSON.parse(m.data).payload));
    es.addEventListener("group.correlation", (m) => showCorrelation(JSON.parse(m.data).payload));
    es.onerror = () => { statusEl.textContent = "Reconnecting…"; statusEl.className = "status conn conn-warn"; };
  }

  // --- the map: stations, pairs, night ---

  const RAD = Math.PI / 180;

  // Great-circle distance in km.
  function distance(a, b) {
    const dLat = (b[0] - a[0]) * RAD, dLon = (b[1] - a[1]) * RAD;
    const h = Math.sin(dLat / 2) ** 2 + Math.cos(a[0] * RAD) * Math.cos(b[0] * RAD) * Math.sin(dLon / 2) ** 2;
    return 2 * 6371.0088 * Math.asin(Math.min(1, Math.sqrt(h)));
  }

  // The night side of the earth at a time, as a polygon, after the usual
  // low-precision solar position (as in the Leaflet.Terminator plugin).
  function night(date) {
    const jd = date.getTime() / 86400000 + 2440587.5, n = jd - 2451545.0;
    const gst = (18.697374558 + 24.06570982441908 * n) % 24;
    const L = (280.46 + 0.9856474 * n) % 360, g = ((357.528 + 0.9856003 * n) % 360) * RAD;
    const lambda = (L + 1.915 * Math.sin(g) + 0.02 * Math.sin(2 * g)) * RAD;
    const eps = (23.43929111 - (n / 36525) * (46.836769 / 3600)) * RAD;
    let alpha = Math.atan(Math.cos(eps) * Math.tan(lambda)) / RAD;
    const delta = Math.asin(Math.sin(eps) * Math.sin(lambda)) / RAD;
    alpha += Math.floor(lambda / RAD / 90) * 90 - Math.floor(alpha / 90) * 90;
    const pts = [];
    for (let lng = -180; lng <= 180; lng += 0.5) {
      const ha = ((gst + lng / 15) * 15 - alpha) * RAD;
      pts.push([Math.atan(-Math.cos(ha) / Math.tan(delta * RAD)) / RAD, lng]);
    }
    const pole = delta < 0 ? 90 : -90;
    pts.push([pole, 180], [pole, -180]);
    return pts;
  }

  function drawMap() {
    const box = root.querySelector(".compare-map");
    const placed = Array.from(root.querySelectorAll(".lane-names li[data-lat][data-lon]"))
      .map((li) => ({ name: li.textContent, at: [Number(li.dataset.lat), Number(li.dataset.lon)] }));
    if (!box || !placed.length || typeof L === "undefined") return;
    box.hidden = false;
    root.querySelector(".compare-map-note").hidden = false;
    const map = L.map(box, { worldCopyJump: true, scrollWheelZoom: false });
    L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
      maxZoom: 12, referrerPolicy: "strict-origin-when-cross-origin",
      attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a>',
    }).addTo(map);
    let shade = L.polygon(night(new Date()), { stroke: false, fillColor: "#0d1117", fillOpacity: 0.25, interactive: false }).addTo(map);
    setInterval(() => { shade.setLatLngs(night(new Date())); }, 60000);
    for (const p of placed) L.circleMarker(p.at, { radius: 6, weight: 2 }).bindTooltip(p.name, { permanent: true, direction: "right" }).addTo(map);
    for (let i = 0; i < placed.length; i++) {
      for (let j = i + 1; j < placed.length; j++) {
        const km = distance(placed[i].at, placed[j].at);
        const line = L.polyline([placed[i].at, placed[j].at], { weight: 1.5, dashArray: "4 4" }).addTo(map);
        line.bindTooltip(Math.round(km) + " km · " + (km / 299.792458).toFixed(1) + " ms", { permanent: true, className: "pair-label", direction: "center" });
      }
    }
    if (placed.length === 1) map.setView(placed[0].at, 5);
    else map.fitBounds(L.latLngBounds(placed.map((p) => p.at)).pad(0.25));
  }

  try { drawMap(); } catch (e) { /* the lanes work without the map */ }

  for (const r of root.querySelectorAll('input[name="lane-metric"]')) {
    r.addEventListener("change", () => { metric = r.value; draw(); });
  }
  draw();
  loadState().then(connect);
})();
