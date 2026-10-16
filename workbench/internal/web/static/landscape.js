// The spectral landscape of one source: the last 60 seconds of spectra as
// lines, one a second, the newest in front and the older ones
// stacked behind, flat rather than in perspective so that the frequency
// axis stays readable. Height is the relative level the engine sends (0 to
// 1, not dB), on a fixed scale. Detected peaks are marked in copper;
// seconds without audio stay empty. Values can be read with the pointer or
// the arrow keys; pausing freezes the drawing only.
(function () {
  "use strict";

  const SPAN_S = 60, STEP_MS = 1000, LINES = (SPAN_S * 1000) / STEP_MS;
  const TICKS = [10, 20, 50, 100, 200, 500, 1000, 2000, 5000, 10000, 20000];

  function fmtHz(hz) {
    return hz >= 1000 ? (hz / 1000).toFixed(hz >= 10000 ? 0 : 1).replace(/\.0$/, "") + " kHz" : Math.round(hz) + " Hz";
  }
  function utc(ms) { return new Date(ms).toISOString().slice(11, 19); }
  function css(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }

  function Landscape(canvas, readout, summary) {
    this.canvas = canvas;
    this.ctx = canvas.getContext("2d");
    this.readout = readout;
    this.summary = summary;
    this.lines = new Map(); // slot (seconds since 1970) → {bands, peaks}
    this.latest = 0;
    this.first = 0; // the first second seen: earlier is not a gap
    this.lastPush = 0; // when the page last received a spectrum
    this.cur = null;
    this.range = null;
    this.frozen = null; // {lines, latest} while paused
    this.cursor = null; // {band, age}
    this.visible = true;
    this.lastDraw = 0;
    this.reduced = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    this.bind();
  }

  // push adds one spectrum (the payload of signal.spectrum) at its time.
  Landscape.prototype.push = function (p, ts) {
    if (!p || !p.bands || !p.bands.length) return;
    let t = Date.parse(ts);
    if (isNaN(t)) t = Date.now();
    this.lastPush = Date.now();
    const slot = Math.floor(t / STEP_MS);
    if (!this.range || this.range.min !== p.min_hz || this.range.max !== p.max_hz || this.range.n !== p.bands.length) {
      this.range = { min: p.min_hz, max: p.max_hz, n: p.bands.length };
      this.lines.clear();
      this.cur = null;
      this.first = 0;
    }
    if (this.cur && slot !== this.cur.slot) this.finish();
    if (!this.cur) this.cur = { slot: slot, sum: new Float32Array(p.bands.length), n: 0, peaks: new Map() };
    const c = this.cur;
    for (let i = 0; i < p.bands.length; i++) c.sum[i] += p.bands[i];
    c.n++;
    for (const pk of p.peaks || []) {
      const k = Math.round(pk.hz);
      if (!c.peaks.has(k) || c.peaks.get(k) < pk.amplitude) c.peaks.set(k, pk.amplitude);
    }
  };

  // finish closes the second being collected: its line is the mean of
  // its spectra, with every peak seen in it.
  Landscape.prototype.finish = function () {
    const c = this.cur;
    this.cur = null;
    const bands = new Float32Array(c.sum.length);
    for (let i = 0; i < bands.length; i++) bands[i] = c.sum[i] / c.n;
    this.lines.set(c.slot, { bands: bands, peaks: Array.from(c.peaks, ([hz, a]) => ({ hz: hz, a: a })) });
    this.latest = Math.max(this.latest, c.slot);
    if (!this.first) this.first = c.slot;
    for (const k of this.lines.keys()) if (k <= this.latest - LINES) this.lines.delete(k);
    const now = Date.now();
    if (!this.frozen && this.visible && (!this.reduced || now - this.lastDraw >= 5000)) this.draw();
    this.summarize();
  };

  Landscape.prototype.pause = function (on) {
    this.frozen = on ? { lines: new Map(this.lines), latest: this.latest, first: this.first } : null;
    if (!on) this.draw();
  };

  Landscape.prototype.show = function (on) {
    this.visible = on;
    if (on) this.draw();
  };

  Landscape.prototype.geometry = function () {
    const dpr = window.devicePixelRatio || 1;
    const w = this.canvas.clientWidth, h = this.canvas.clientHeight;
    if (this.canvas.width !== Math.round(w * dpr) || this.canvas.height !== Math.round(h * dpr)) {
      this.canvas.width = Math.round(w * dpr);
      this.canvas.height = Math.round(h * dpr);
    }
    const g = { dpr: dpr, w: w, h: h, l: 46, r: 58, t: 14, b: 28 };
    g.pw = w - g.l - g.r;
    g.ph = h - g.t - g.b;
    g.amp = g.ph * 0.36;
    g.dy = (g.ph - g.amp) / (LINES - 1);
    g.base = (age) => g.t + g.ph - age * g.dy;
    return g;
  };

  Landscape.prototype.xOfHz = function (g, hz) {
    const r = this.range;
    return g.l + ((Math.log(hz) - Math.log(r.min)) / (Math.log(r.max) - Math.log(r.min))) * g.pw;
  };

  Landscape.prototype.hzOfBand = function (i) {
    const r = this.range, lmin = Math.log(r.min), lmax = Math.log(r.max);
    return Math.exp(lmin + ((i + 0.5) / r.n) * (lmax - lmin));
  };

  Landscape.prototype.draw = function () {
    this.lastDraw = Date.now();
    const g = this.geometry(), ctx = this.ctx;
    const bg = css("--instrument"), fg = css("--instrument-text"), muted = css("--instrument-muted"), copper = css("--copper");
    ctx.setTransform(g.dpr, 0, 0, g.dpr, 0, 0);
    ctx.fillStyle = bg;
    ctx.fillRect(0, 0, g.w, g.h);
    ctx.font = "11px 'IBM Plex Mono', ui-monospace, monospace";
    ctx.textBaseline = "middle";
    if (!this.range) {
      ctx.fillStyle = muted;
      ctx.fillText("Waiting for the first spectrum…", g.l, g.t + g.ph / 2);
      return;
    }
    const src = this.frozen || this;
    const n = this.range.n, latest = src.latest;
    const xb = (i) => g.l + ((i + 0.5) / n) * g.pw;

    // Back to front: each line hides what lies behind it.
    let gapRun = 0;
    for (let age = LINES - 1; age >= 0; age--) {
      const line = src.lines.get(latest - age), y0 = g.base(age);
      if (latest - age < src.first) continue;
      if (!line) {
        gapRun++;
        if (age === 0 || src.lines.get(latest - age + 1)) this.gapLabel(ctx, g, age, gapRun, muted, latest);
        continue;
      }
      gapRun = 0;
      ctx.beginPath();
      ctx.moveTo(g.l, y0);
      for (let i = 0; i < n; i++) ctx.lineTo(xb(i), y0 - Math.min(1, Math.max(0, line.bands[i])) * g.amp);
      ctx.lineTo(g.l + g.pw, y0);
      ctx.closePath();
      ctx.fillStyle = bg;
      ctx.fill();
      ctx.globalAlpha = 0.25 + 0.75 * (1 - age / LINES);
      ctx.strokeStyle = fg;
      ctx.lineWidth = age === 0 ? 1.4 : 1;
      ctx.stroke();
      ctx.fillStyle = copper;
      for (const pk of line.peaks) {
        if (pk.hz < this.range.min || pk.hz > this.range.max) continue;
        ctx.beginPath();
        ctx.arc(this.xOfHz(g, pk.hz), y0 - Math.min(1, pk.a) * g.amp, age === 0 ? 2.4 : 1.6, 0, 2 * Math.PI);
        ctx.fill();
      }
      ctx.globalAlpha = 1;
    }

    // Nothing heard for a while: say since when, rather than go on as if live.
    if (!this.frozen && this.lastPush && Date.now() - this.lastPush > 3000) {
      ctx.fillStyle = copper;
      ctx.fillText("No audio since " + utc((latest + 1) * STEP_MS) + " UTC", g.l, g.t + 4);
    }

    // The value under the cursor.
    const c = this.cursor;
    if (c) {
      const line = src.lines.get(latest - c.age), y0 = g.base(c.age), x = xb(c.band);
      ctx.strokeStyle = fg;
      ctx.setLineDash([3, 3]);
      ctx.beginPath(); ctx.moveTo(x, g.t); ctx.lineTo(x, g.t + g.ph); ctx.stroke();
      ctx.setLineDash([]);
      if (line) {
        ctx.beginPath();
        ctx.moveTo(g.l, y0);
        for (let i = 0; i < n; i++) ctx.lineTo(xb(i), y0 - Math.min(1, Math.max(0, line.bands[i])) * g.amp);
        ctx.lineWidth = 2;
        ctx.stroke();
        ctx.lineWidth = 1;
        ctx.fillStyle = fg;
        ctx.beginPath(); ctx.arc(x, y0 - Math.min(1, line.bands[c.band]) * g.amp, 3.5, 0, 2 * Math.PI); ctx.fill();
      }
      const at = (latest - c.age) * STEP_MS;
      const what = line ? "level " + line.bands[c.band].toFixed(2) : "no audio";
      this.readout.textContent = fmtHz(this.hzOfBand(c.band)) + " · " + what + " · " + utc(at) + " UTC";
      this.readout.hidden = false;
      this.readout.style.left = Math.min(g.w - 220, Math.max(0, x + 10)) + "px";
      this.readout.style.top = Math.max(0, y0 - g.amp - 30) + "px";
    } else {
      this.readout.hidden = true;
    }

    // Axes: frequency below, age to the right, the level's scale to the left.
    ctx.fillStyle = muted;
    ctx.strokeStyle = muted;
    ctx.globalAlpha = 0.9;
    const yb = g.t + g.ph;
    ctx.textAlign = "center";
    let right = -Infinity; // where the last label ends: labels never overlap
    for (const hz of TICKS) {
      if (hz < this.range.min || hz > this.range.max) continue;
      const x = this.xOfHz(g, hz), label = fmtHz(hz), half = ctx.measureText(label).width / 2;
      ctx.beginPath(); ctx.moveTo(x, yb + 2); ctx.lineTo(x, yb + 6); ctx.stroke();
      if (x - half < right + 8) continue;
      ctx.fillText(label, x, yb + 16);
      right = x + half;
    }
    ctx.textAlign = "left";
    for (const [age, label] of [[0, "now"], [LINES / 2, "−30 s"], [LINES - 1, "−60 s"]]) {
      const y = g.base(age);
      ctx.beginPath(); ctx.moveTo(g.l + g.pw + 4, y); ctx.lineTo(g.l + g.pw + 8, y); ctx.stroke();
      ctx.fillText(label, g.l + g.pw + 12, y);
    }
    ctx.textAlign = "right";
    ctx.beginPath(); ctx.moveTo(g.l - 8, yb); ctx.lineTo(g.l - 8, yb - g.amp); ctx.stroke();
    ctx.fillText("1", g.l - 12, yb - g.amp);
    ctx.fillText("0", g.l - 12, yb);
    ctx.save();
    ctx.translate(12, yb - g.amp / 2);
    ctx.rotate(-Math.PI / 2);
    ctx.textAlign = "center";
    ctx.fillText("level", 0, 0);
    ctx.restore();
    ctx.globalAlpha = 1;
    ctx.textAlign = "left";
  };

  // gapLabel marks a run of seconds without audio, ending at age.
  Landscape.prototype.gapLabel = function (ctx, g, age, run, muted) {
    if (run < 2) return;
    const y = (g.base(age) + g.base(age + run - 1)) / 2;
    ctx.save();
    ctx.globalAlpha = 0.8;
    ctx.strokeStyle = muted;
    ctx.setLineDash([2, 4]);
    ctx.beginPath(); ctx.moveTo(g.l, y); ctx.lineTo(g.l + g.pw, y); ctx.stroke();
    ctx.setLineDash([]);
    ctx.fillStyle = muted;
    ctx.fillText("no audio for " + run + " s", g.l + 6, y - 8);
    ctx.restore();
  };

  // summarize says in words what the newest line shows, for everyone and
  // for screen readers (not announced: it changes every second).
  Landscape.prototype.summarize = function () {
    if (!this.summary || this.frozen) return;
    const line = this.lines.get(this.latest);
    if (!line) return;
    let best = 0;
    for (let i = 1; i < line.bands.length; i++) if (line.bands[i] > line.bands[best]) best = i;
    const peaks = line.peaks.length;
    this.summary.textContent = "Now: strongest near " + fmtHz(this.hzOfBand(best)) + ", level " + line.bands[best].toFixed(2) + "; " +
      peaks + " peak" + (peaks === 1 ? "" : "s") + " detected (copper). Levels are relative, 0 to 1, as the engine sends them. " +
      utc(this.latest * STEP_MS) + " UTC.";
  };

  Landscape.prototype.bind = function () {
    const cv = this.canvas;
    const at = (ev) => {
      if (!this.range) return;
      const g = this.geometry(), r = cv.getBoundingClientRect();
      const x = ev.clientX - r.left, y = ev.clientY - r.top;
      if (x < g.l || x > g.l + g.pw) { this.cursor = null; this.draw(); return; }
      const band = Math.min(this.range.n - 1, Math.max(0, Math.floor(((x - g.l) / g.pw) * this.range.n)));
      const age = Math.min(LINES - 1, Math.max(0, Math.round((g.t + g.ph - y) / g.dy)));
      this.cursor = { band: band, age: age };
      this.draw();
    };
    cv.addEventListener("pointermove", at);
    cv.addEventListener("pointerdown", at);
    cv.addEventListener("pointerleave", () => { this.cursor = null; this.draw(); });
    cv.addEventListener("focus", () => {
      if (this.range && !this.cursor) { this.cursor = { band: Math.floor(this.range.n / 2), age: 0 }; this.draw(); }
    });
    cv.addEventListener("blur", () => { this.cursor = null; this.draw(); });
    cv.addEventListener("keydown", (ev) => {
      const c = this.cursor;
      if (!c || !this.range) return;
      const step = ev.shiftKey ? 10 : 1;
      if (ev.key === "ArrowLeft") c.band = Math.max(0, c.band - step);
      else if (ev.key === "ArrowRight") c.band = Math.min(this.range.n - 1, c.band + step);
      else if (ev.key === "ArrowUp") c.age = Math.min(LINES - 1, c.age + step);
      else if (ev.key === "ArrowDown") c.age = Math.max(0, c.age - step);
      else if (ev.key === "Escape") { this.cursor = null; this.draw(); return; }
      else return;
      ev.preventDefault();
      this.draw();
    });
    window.addEventListener("resize", () => { if (this.visible) this.draw(); });
    setInterval(() => {
      if (this.visible && !this.frozen && this.lastPush && Date.now() - this.lastPush > 3000) this.draw();
    }, 1000);
    document.addEventListener("low-theme", () => { if (this.visible) this.draw(); });
  };

  window.LowLandscape = Landscape;
})();
