// The field (§21 layers A–C) as a p5.js sketch in instance mode.
//   A  terrain: a perspective ridge field whose rows are the recent spectrum,
//      warm at low frequencies, cool at high; particles rise from strong bands
//   B  resonances: luminous spires with rings and frequency labels
//   C  events: transients send a shockwave and a ripple through the terrain
(function (LO) {
  'use strict';

  var ROWS = 120;         // depth rows of spectrum history
  var ROW_DT = 0.065;     // seconds per row → ~8 s of history in depth
  var COLS = 200;         // vertices per ridge
  var WEAVE = 5;          // every n-th column also runs in depth: the carpet's warp
  var RES_DEPTH = 0.3;    // depth at which resonance spires stand

  // Warm → pale gold → cool, along the log-frequency axis (docs/visuals.png).
  var STOPS = [
    [0.00, [226, 118, 112]],
    [0.30, [246, 170, 96]],
    [0.50, [252, 206, 140]],
    [0.66, [214, 214, 222]],
    [1.00, [120, 176, 246]]
  ];
  function colour(u) {
    u = u < 0 ? 0 : u > 1 ? 1 : u;
    for (var i = 1; i < STOPS.length; i++) {
      if (u <= STOPS[i][0]) {
        var a = STOPS[i - 1], b = STOPS[i], t = (u - a[0]) / (b[0] - a[0]);
        return [a[1][0] + (b[1][0] - a[1][0]) * t, a[1][1] + (b[1][1] - a[1][1]) * t, a[1][2] + (b[1][2] - a[1][2]) * t];
      }
    }
    return STOPS[STOPS.length - 1][1];
  }
  function rgba(c, a) { return 'rgba(' + (c[0] | 0) + ',' + (c[1] | 0) + ',' + (c[2] | 0) + ',' + a.toFixed(3) + ')'; }

  // Catmull-Rom sample of a band array at position u (0..1).
  function sample(bands, u) {
    var n = bands.length, x = u * (n - 1), i = Math.floor(x), t = x - i;
    var p0 = bands[Math.max(i - 1, 0)], p1 = bands[i], p2 = bands[Math.min(i + 1, n - 1)], p3 = bands[Math.min(i + 2, n - 1)];
    if (p1 === undefined) return 0;
    return 0.5 * ((2 * p1) + (-p0 + p2) * t + (2 * p0 - 5 * p1 + 4 * p2 - p3) * t * t + (-p0 + 3 * p1 - 3 * p2 + p3) * t * t * t);
  }

  function create(container, state, opts) {
    var reduced = !!opts.reducedMotion;
    var maxParticles = reduced ? 150 : 1400;

    var MONO = getComputedStyle(document.body).getPropertyValue('--mono') || 'monospace';
    var perf = LO.perf = { carpet: 0, resonances: 0, rest: 0, spires: 0, density: 0, rowStep: 1 };
    var STOP_CSS = STOPS.map(function (st) { return rgba(st[1], 1); });
    var BG_FILL = 'rgb(7,8,10)';
    var rowX = [], rowY = [], rowV = [];
    for (var ri = 0; ri < ROWS; ri++) {
      rowX.push(new Float32Array(COLS + 1)); rowY.push(new Float32Array(COLS + 1)); rowV.push(new Float32Array(COLS + 1));
    }
    var rowStep = 1;
    return new p5(function (p) {
      var ctx, W, H;
      var history = [];
      var rowClock = 0;
      var particles = [];
      var shocks = [];
      var stars = [];
      var flash = 0;
      var t = 0;

      function geometry(d) {
        var persp = 1 / (1 + d * 3.2);
        var back = 1 / (1 + 3.2);
        var horizon = H * 0.36, front = H * 1.02;
        return {
          y: horizon + (front - horizon) * (persp - back) / (1 - back),
          half: W * (0.36 + 0.34 * persp),
          amp: H * 0.4 * persp,
          persp: persp
        };
      }

      // Spectral peaks as a smoothed field over the columns: a peak rises
      // within ~50 ms and decays over ~0.4 s, so a peak that detection
      // loses for one update doesn't blink out of the terrain.
      var peakTarget = new Float32Array(COLS + 1), peakField = new Float32Array(COLS + 1);
      var peaksSeen = null;
      function updatePeakField(dt) {
        if (state.peaks !== peaksSeen) {
          peaksSeen = state.peaks;
          peakTarget.fill(0);
          for (var j = 0; j < state.peaks.length; j++) {
            var pk = state.peaks[j];
            var pc = state.u(pk.hz) * COLS, w = 0.01 * COLS;
            var lo = Math.max(0, Math.floor(pc - 4 * w)), hi = Math.min(COLS, Math.ceil(pc + 4 * w));
            for (var c = lo; c <= hi; c++) {
              var dx = (c - pc) / w;
              peakTarget[c] = Math.max(peakTarget[c], pk.amplitude * 0.45 * Math.exp(-dx * dx));
            }
          }
        }
        var up = 1 - Math.exp(-dt / 0.05), down = 1 - Math.exp(-dt / 0.4);
        for (var k = 0; k <= COLS; k++) {
          var tg = peakTarget[k];
          peakField[k] += (tg - peakField[k]) * (tg > peakField[k] ? up : down);
        }
      }

      // Static shape of a history row, computed once when the row is born:
      // band contour with extra contrast plus the current peak field.
      var rowSerial = 0;
      function rowShape() {
        var base = new Float32Array(COLS + 1);
        for (var c = 0; c <= COLS; c++) {
          var bv = sample(state.bands, c / COLS);
          base[c] = 0.25 * bv + 0.9 * Math.max(0, (bv - 0.35) / 0.65) + peakField[c];
        }
        return { base: base, serial: rowSerial++ };
      }

      // Slow undulation that keeps flat stretches alive. sin(a+b) is
      // expanded so the per-vertex work is two multiply-adds, not trig.
      var S1 = new Float32Array(COLS + 1), C1 = new Float32Array(COLS + 1);
      var S2 = new Float32Array(COLS + 1), C2 = new Float32Array(COLS + 1);
      for (var ci = 0; ci <= COLS; ci++) {
        S1[ci] = Math.sin(ci / COLS * 9.1); C1[ci] = Math.cos(ci / COLS * 9.1);
        S2[ci] = Math.sin(ci / COLS * 23.7); C2[ci] = Math.cos(ci / COLS * 23.7);
      }

      // Heights of one row at depth d into out[] (0..COLS).
      function rowHeights(row, d, out) {
        var p1 = d * 4.3 + t * 0.35, p2 = -d * 7.1 + t * 0.21;
        var s1 = Math.sin(p1) * 0.03, c1 = Math.cos(p1) * 0.03;
        var s2 = Math.sin(p2) * 0.02, c2 = Math.cos(p2) * 0.02;
        var base = row ? row.base : null;
        for (var c = 0; c <= COLS; c++) {
          out[c] = (base ? base[c] : 0) + 0.05 + S1[c] * c1 + C1[c] * s1 + S2[c] * c2 + C2[c] * s2;
        }
        for (var i = 0; i < shocks.length; i++) {
          var sh = shocks[i], age = t - sh.born, reach = age * 0.55;
          var dd = d - 0.05, amp = sh.strength * 0.35 * Math.exp(-age * 1.1);
          if (Math.abs(dd) >= reach) continue;
          for (c = 0; c <= COLS; c++) {
            var du = (c / COLS - sh.u) * 1.6;
            var dist = Math.sqrt(du * du + dd * dd);
            if (dist < reach) out[c] += amp * Math.exp(-dist * 2.2) * Math.sin((reach - dist) * 26);
          }
        }
      }

      var scratch = new Float32Array(COLS + 1);

      function spawnSpray(dt) {
        var row = history[0];
        if (!row || reduced && Math.random() > 0.3) return;
        var energy = state.frame.energy.v, entropy = state.frame.entropy.v;
        var rate = (160 + 700 * energy) * dt;
        var g = geometry(0.04);
        rowHeights(row, 0.04, scratch);
        for (var i = 0; i < rate && particles.length < maxParticles; i++) {
          var u = Math.random();
          var v = scratch[Math.round(u * COLS)];
          if (v < 0.3 || Math.random() > v) continue;
          particles.push({
            x: W / 2 + (u - 0.5) * 2 * g.half + (Math.random() - 0.5) * 6,
            y: g.y - v * g.amp,
            vx: (Math.random() - 0.5) * (8 + 60 * entropy),
            vy: -(12 + 70 * v * (0.4 + energy)),
            life: 1.5 + Math.random() * 2.5, age: 0,
            c: colour(u), size: 0.6 + Math.random() * 1.4
          });
        }
      }

      function burst(u, strength) {
        var g = geometry(0.08);
        var x = W / 2 + (u - 0.5) * 2 * g.half, y = g.y - g.amp * 0.4;
        var n = Math.round((reduced ? 30 : 220) * (0.4 + strength));
        for (var i = 0; i < n && particles.length < maxParticles + 300; i++) {
          var a = Math.random() * Math.PI * 2, sp = 40 + Math.random() * 220 * (0.5 + strength);
          particles.push({
            x: x, y: y, vx: Math.cos(a) * sp, vy: Math.sin(a) * sp * 0.6 - 40,
            life: 1 + Math.random() * 1.8, age: 0, c: [210, 230, 255], size: 0.8 + Math.random() * 1.6
          });
        }
      }

      // Screen boxes of the title and readout, in canvas coordinates.
      var overlayBoxes = null, overlayAt = 0;
      function overlays() {
        if (overlayBoxes && t - overlayAt < 2) return overlayBoxes;
        var base = container.getBoundingClientRect();
        overlayBoxes = Array.prototype.map.call(
          container.parentNode.querySelectorAll('.stage__title, .stage__readout'),
          function (el) {
            var r = el.getBoundingClientRect();
            return { left: r.left - base.left, right: r.right - base.left, top: r.top - base.top, bottom: r.bottom - base.top };
          });
        overlayAt = t;
        return overlayBoxes;
      }

      p.setup = function () {
        W = container.clientWidth; H = container.clientHeight;
        density = Math.min(window.devicePixelRatio || 1, 2);
        perf.density = density;
        p.pixelDensity(density);
        p.createCanvas(W, H);
        ctx = p.drawingContext;
        p.noiseSeed(7);
        for (var i = 0; i < 140; i++) stars.push({ x: Math.random(), y: Math.random() * 0.55, a: Math.random() * 0.5, f: Math.random() * 3 });
        if (reduced) p.frameRate(20);
      };

      p.windowResized = function () {
        W = container.clientWidth; H = container.clientHeight;
        p.resizeCanvas(W, H);
      };

      // Adaptive quality: if frames stay slow for a few seconds, first lower
      // the pixel density, then draw every other row.
      var density, slowFor = 0, frameAvg = 16;
      function adapt(ms) {
        frameAvg += (ms - frameAvg) * 0.05;
        slowFor = frameAvg > 24 ? slowFor + ms : 0;
        if (slowFor < 3000) return;
        slowFor = 0; frameAvg = 16;
        if (density > 1) {
          density = Math.max(1, density - 0.5);
          p.pixelDensity(density);
        } else if (rowStep === 1) {
          rowStep = 2;
        }
        perf.density = density; perf.rowStep = rowStep;
      }

      p.draw = function () {
        adapt(p.deltaTime);
        var dt = Math.min(p.deltaTime / 1000, 0.1);
        t += dt;
        state.step(dt);

        updatePeakField(dt);
        rowClock += dt;
        while (rowClock >= ROW_DT) {
          rowClock -= ROW_DT;
          history.unshift(rowShape());
          if (history.length > ROWS + 1) history.pop();
        }
        var frac = rowClock / ROW_DT;

        ctx.save();
        ctx.globalCompositeOperation = 'source-over';
        ctx.fillStyle = '#07080a';
        ctx.fillRect(0, 0, W, H);

        // Sky: faint stars.
        ctx.globalCompositeOperation = 'lighter';
        for (var s = 0; s < stars.length; s++) {
          var st = stars[s];
          ctx.fillStyle = 'rgba(220,225,235,' + (st.a * (0.6 + 0.4 * Math.sin(t * 0.5 + st.f))).toFixed(3) + ')';
          ctx.fillRect(st.x * W, st.y * H, 1, 1);
        }
        ctx.globalCompositeOperation = 'source-over';

        var energy = state.frame.energy.v;
        var bright = 0.55 + 0.45 * energy + flash;

        var perf0 = performance.now();
        // Layer A: a woven carpet, drawn back to front. Each row is a ridge
        // (weft) whose fill hides what lies behind it; every WEAVE-th column
        // also runs in depth (warp); a dotted mesh sits on the surface.
        // One gradient per row; brightness via globalAlpha; every pass is a
        // single path, so the cost doesn't grow with the number of peaks.
        var prevX = null, prevY = null;
        for (var r = ROWS - 1; r >= 0; r -= rowStep) {
          var row = history[r];
          var d = (r + frac) / ROWS;
          var g = geometry(d);
          var xs = rowX[r], ys = rowY[r], vs = rowV[r];
          rowHeights(row, d, vs);
          var x0 = W / 2 - g.half, xw = 2 * g.half / COLS;
          for (var c = 0; c <= COLS; c++) {
            xs[c] = x0 + c * xw;
            ys[c] = g.y - vs[c] * g.amp;
          }
          var depthAlpha = Math.pow(1 - d, 1.25);
          var grad = ctx.createLinearGradient(xs[0], 0, xs[COLS], 0);
          for (var k = 0; k < STOPS.length; k++) grad.addColorStop(STOPS[k][0], STOP_CSS[k]);
          ctx.strokeStyle = grad;
          ctx.fillStyle = grad;

          // Warp threads from the row behind, under this row's fill.
          if (prevX) {
            ctx.globalAlpha = Math.min(1, 0.22 * depthAlpha * bright);
            ctx.lineWidth = 0.5 + 0.5 * g.persp;
            ctx.beginPath();
            for (c = 0; c <= COLS; c += WEAVE) { ctx.moveTo(prevX[c], prevY[c]); ctx.lineTo(xs[c], ys[c]); }
            ctx.stroke();
          }

          // Occlusion: from the ridge down to this row's own baseline.
          ctx.globalAlpha = 1;
          ctx.beginPath();
          ctx.moveTo(xs[0], g.y + 2);
          for (c = 0; c <= COLS; c++) ctx.lineTo(xs[c], ys[c]);
          ctx.lineTo(xs[COLS], g.y + 2);
          ctx.closePath();
          ctx.fillStyle = BG_FILL;
          ctx.fill();

          // Weft: the ridge line.
          ctx.globalAlpha = Math.min(1, 0.06 + 0.6 * depthAlpha * bright);
          ctx.lineWidth = (0.45 + 0.8 * g.persp) * rowStep;
          ctx.beginPath();
          ctx.moveTo(xs[0], ys[0]);
          for (c = 1; c <= COLS; c++) ctx.lineTo(xs[c], ys[c]);
          ctx.stroke();

          // Dotted mesh, staggered, only where the surface rises.
          if (d < 0.9) {
            ctx.globalCompositeOperation = 'lighter';
            ctx.globalAlpha = Math.min(1, 0.85 * depthAlpha * bright);
            ctx.fillStyle = grad;
            ctx.beginPath();
            var step = 2;
            for (c = row ? row.serial % 2 : 0; c <= COLS; c += step) {
              var v = vs[c];
              if (v < 0.22) continue;
              var sz = (0.5 + 1.4 * v) * (0.5 + g.persp);
              ctx.rect(xs[c] - sz / 2, ys[c] - sz / 2, sz, sz);
            }
            ctx.fill();
            ctx.globalCompositeOperation = 'source-over';
          }
          ctx.globalAlpha = 1;
          prevX = xs; prevY = ys;
        }

        var perf1 = performance.now();
        // Layer B: resonances.
        drawResonances(dt);
        var perf2 = performance.now();

        // Layer C: shockwaves.
        ctx.globalCompositeOperation = 'lighter';
        for (var i = shocks.length - 1; i >= 0; i--) {
          var sh = shocks[i], age = t - sh.born;
          if (age > 3) { shocks.splice(i, 1); continue; }
          var gs = geometry(0.05);
          var cx = W / 2 + (sh.u - 0.5) * 2 * gs.half;
          var rad = age * W * 0.32;
          ctx.strokeStyle = 'rgba(200,225,255,' + (0.7 * sh.strength * (1 - age / 3)).toFixed(3) + ')';
          ctx.lineWidth = 1.2;
          ctx.beginPath();
          ctx.ellipse(cx, gs.y - gs.amp * 0.3, rad, rad * 0.22, 0, 0, Math.PI * 2);
          ctx.stroke();
        }

        // Particles.
        spawnSpray(dt);
        for (i = particles.length - 1; i >= 0; i--) {
          var pt = particles[i];
          pt.age += dt;
          if (pt.age > pt.life) { particles.splice(i, 1); continue; }
          pt.vx += (p.noise(pt.x * 0.01, pt.y * 0.01, t * 0.2) - 0.5) * 40 * dt;
          pt.vx *= 1 - 0.8 * dt; pt.vy *= 1 - 0.5 * dt;
          pt.x += pt.vx * dt; pt.y += pt.vy * dt;
          var fade = 1 - pt.age / pt.life;
          ctx.fillStyle = rgba(pt.c, 0.7 * fade * fade);
          ctx.fillRect(pt.x, pt.y, pt.size, pt.size);
        }
        ctx.globalCompositeOperation = 'source-over';

        if (!history[0] || state.connection !== 'connected') {
          ctx.fillStyle = 'rgba(169,172,176,0.85)';
          ctx.font = '14px ' + MONO;
          ctx.textAlign = 'center';
          ctx.fillText(state.connection === 'connected' ? 'waiting for signal' : state.connection, W / 2, H * 0.3);
        }

        var perf3 = performance.now();
        perf.carpet += (perf1 - perf0 - perf.carpet) * 0.05;
        perf.resonances += (perf2 - perf1 - perf.resonances) * 0.05;
        perf.rest += (perf3 - perf2 - perf.rest) * 0.05;
        perf.spires = state.resonances.size;
        flash *= Math.exp(-dt * 3);
        ctx.restore();
      };

      // Resonances with hysteresis. A spire becomes "main" (thick, rings,
      // harmonics, label) in the top 4 by strength and only drops out below
      // 6th; the lead ("resonant structure") changes only when another is
      // clearly stronger. Every change fades over ~0.4 s and labels glide,
      // so near-equal strengths don't make lines blink on and off.
      var lead = null;
      function drawResonances(dt) {
        var list = Array.from(state.resonances.values());
        list.sort(function (a, b) { return b.data.strength - a.data.strength; });
        var ease = 1 - Math.exp(-dt / 0.4), glide = 1 - Math.exp(-dt / 0.25);
        list.forEach(function (r, idx) {
          if (r.mainW === undefined) { r.mainW = 0; r.isMain = false; r.labelY = null; }
          if (r.ending) r.isMain = false;
          else if (idx < 4) r.isMain = true;
          else if (idx >= 6) r.isMain = false;
          r.mainW += ((r.isMain ? 1 : 0) - r.mainW) * ease;
        });
        var top = list.filter(function (r) { return !r.ending; })[0] || null;
        if (!lead || lead.ending || !state.resonances.has(lead.id)) lead = top;
        else if (top && top !== lead && top.data.strength > lead.data.strength * 1.25) lead = top;

        var g = geometry(RES_DEPTH);
        var row = history[Math.round(RES_DEPTH * ROWS)];
        var resRow = scratch;
        rowHeights(row, RES_DEPTH, resRow);
        var avoid = overlays();
        var placed = [], labelled = {};

        function spire(hz, strength, alpha, w) {
          var u = state.u(hz);
          if (u < 0 || u > 1) return null;
          var x = W / 2 + (u - 0.5) * 2 * g.half;
          var ground = g.y - resRow[Math.round(u * COLS)] * g.amp;
          var top = Math.max(H * 0.06, ground - H * ((0.15 + 0.2 * strength) + w * (0.15 + 0.15 * strength)));
          var c = colour(u);
          ctx.globalCompositeOperation = 'lighter';
          var lg = ctx.createLinearGradient(0, ground, 0, top);
          lg.addColorStop(0, rgba(c, 0.95 * alpha));
          lg.addColorStop(1, rgba(c, 0.05 * alpha));
          ctx.strokeStyle = lg;
          ctx.beginPath(); ctx.moveTo(x, ground); ctx.lineTo(x, top);
          ctx.globalAlpha = 0.18;             // soft glow: a wide faint pass…
          ctx.lineWidth = 3 + 4 * w;
          ctx.stroke();
          ctx.globalAlpha = 1;                // …under the bright core
          ctx.lineWidth = 1 + 0.8 * w;
          ctx.stroke();
          ctx.globalCompositeOperation = 'source-over';
          return { x: x, ground: ground, top: top, c: c };
        }

        function rings(sp, hz, n, alpha) {
          ctx.globalCompositeOperation = 'lighter';
          var ry = sp.ground - (sp.ground - sp.top) * 0.42;
          ctx.lineWidth = 0.8;
          for (var i = 0; i < n; i++) {
            var rw = (26 + i * 30) * (1 + 0.05 * Math.sin(t * 0.8 + i + hz));
            ctx.strokeStyle = rgba(sp.c, (0.35 - i * 0.05) * alpha);
            ctx.beginPath(); ctx.ellipse(sp.x, ry, rw, rw * 0.2, 0, 0, Math.PI * 2); ctx.stroke();
          }
          ctx.globalCompositeOperation = 'source-over';
        }

        function label(owner, key, sp, text, sub, alpha, big) {
          if (!sp || alpha < 0.02 || labelled[text]) return;
          labelled[text] = true;
          var ly = sp.top - 8;
          for (var pass = 0; pass < 2; pass++) {
            for (var j = 0; j < placed.length; j++) {
              if (Math.abs(placed[j].x - sp.x) < 110 && Math.abs(placed[j].y - ly) < 36) ly = placed[j].y + 36;
            }
            for (var o = 0; o < avoid.length; o++) {
              var a = avoid[o];
              if (sp.x + 120 > a.left && sp.x < a.right && ly + 22 > a.top && ly - 16 < a.bottom) ly = a.bottom + 18;
            }
          }
          if (ly > g.y - g.amp * 0.6) alpha = 0;  // would sit in the terrain: fade instead
          placed.push({ x: sp.x, y: ly });
          owner.labelYs = owner.labelYs || {};
          var prev = owner.labelYs[key];
          ly = prev == null ? ly : prev + (ly - prev) * glide;
          owner.labelYs[key] = ly;
          if (alpha < 0.02) return;
          ctx.textAlign = 'left';
          ctx.lineJoin = 'round';
          ctx.strokeStyle = 'rgba(7,8,10,' + (0.85 * alpha).toFixed(3) + ')';
          ctx.lineWidth = 4;
          ctx.font = (big ? 15 : 13) + 'px ' + MONO;
          ctx.strokeText(text, sp.x + 7, ly);
          ctx.fillStyle = 'rgba(236,235,231,' + alpha.toFixed(3) + ')';
          ctx.fillText(text, sp.x + 7, ly);
          if (sub) {
            ctx.font = '13px ' + MONO;
            ctx.strokeText(sub, sp.x + 7, ly + 17);
            ctx.fillStyle = 'rgba(169,172,176,' + alpha.toFixed(3) + ')';
            ctx.fillText(sub, sp.x + 7, ly + 17);
          }
        }

        // Spires, strongest last so they sit on top.
        var drawn = [];
        for (var i = list.length - 1; i >= 0; i--) {
          var r = list[i], d = r.data;
          var sp = spire(d.fundamental_hz, d.strength, r.alpha, r.mainW);
          if (!sp) continue;
          if (r.mainW > 0.01) {
            rings(sp, d.fundamental_hz, 2 + Math.min(3, d.harmonics.length), r.alpha * r.mainW);
            r.harmonicSpires = d.harmonics.map(function (h) {
              return { hz: h.hz, sp: spire(h.hz, h.strength, r.alpha * 0.6 * r.mainW, 0) };
            });
          } else {
            r.harmonicSpires = [];
          }
          drawn.push({ r: r, sp: sp });
        }

        // Labels in a stable order (oldest first), so placement doesn't
        // reshuffle when strengths wobble.
        drawn.sort(function (a, b) { return a.r.born - b.r.born; });
        if (lead) {
          drawn.sort(function (a, b) { return (b.r === lead) - (a.r === lead); });
        }
        drawn.forEach(function (e) {
          var r = e.r, d = r.data, a = r.alpha * r.mainW;
          var sub = r === lead ? 'resonant structure · ' + Math.round(d.duration_s) + ' s' : Math.round(d.duration_s) + ' s';
          label(r, 'f', e.sp, Math.round(d.fundamental_hz) + ' Hz', sub, a, true);
        });
        if (lead && lead.harmonicSpires) {
          lead.harmonicSpires.slice(0, 2).forEach(function (h, k) {
            label(lead, 'h' + k, h.sp, Math.round(h.hz) + ' Hz', null, lead.alpha * lead.mainW * 0.9, false);
          });
        }
      }

      // Hooks for the event layer.
      state.onEvent(function (env, replay) {
        if (replay || env.type !== 'event.transient') return;
        var pl = env.payload;
        var u = state.u(Math.max(40, pl.centroid_hz || 1000));
        shocks.push({ u: u, born: t, strength: Math.max(0.3, pl.strength) });
        burst(u, pl.strength);
        flash = Math.min(0.5, flash + 0.35 * pl.strength);
      });
    }, container);
  }

  LO.field = { create: create, colour: colour };
})(window.LO = window.LO || {});
