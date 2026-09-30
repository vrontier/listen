// The field (§21 layers A–C) as a p5.js sketch in instance mode.
//   A  terrain: a perspective ridge field whose rows are the recent spectrum,
//      warm at low frequencies, cool at high; particles rise from strong bands
//   B  resonances: luminous spires with rings and frequency labels
//   C  events: transients send a shockwave and a ripple through the terrain
(function (LO) {
  'use strict';

  var ROWS = 60;          // depth rows of spectrum history
  var ROW_DT = 0.12;      // seconds per row → ~7 s of history in depth
  var COLS = 220;         // vertices per ridge
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

      // Terrain height at u for a history row: band contour with extra
      // contrast, narrow ridges where spectral peaks were, and a slow
      // undulation that keeps flat stretches alive.
      function heightAt(row, u, d) {
        var v = 0;
        if (row) {
          var b = sample(row.b, u);
          v = 0.25 * b + 0.9 * Math.max(0, (b - 0.35) / 0.65);
          for (var j = 0; j < row.pk.length; j++) {
            var dx = (u - row.pk[j].u) / 0.01;
            if (dx > -4 && dx < 4) v += row.pk[j].a * 0.45 * Math.exp(-dx * dx);
          }
        }
        v += 0.1 * p.noise(u * 3.2, d * 1.4 + t * 0.03);
        for (var i = 0; i < shocks.length; i++) {
          var s = shocks[i], age = t - s.born;
          var dist = Math.hypot((u - s.u) * 1.6, d - 0.05);
          var front = age * 0.55;
          if (dist < front) {
            v += s.strength * 0.35 * Math.exp(-age * 1.1) * Math.exp(-dist * 2.2) * Math.sin((front - dist) * 26);
          }
        }
        return v;
      }

      function spawnSpray(dt) {
        var row = history[0];
        if (!row || reduced && Math.random() > 0.3) return;
        var energy = state.frame.energy.v, entropy = state.frame.entropy.v;
        var rate = (160 + 700 * energy) * dt;
        var g = geometry(0.04);
        for (var i = 0; i < rate && particles.length < maxParticles; i++) {
          var u = Math.random();
          var v = heightAt(row, u, 0.04);
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
        p.pixelDensity(Math.min(window.devicePixelRatio || 1, 2));
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

      p.draw = function () {
        var dt = Math.min(p.deltaTime / 1000, 0.1);
        t += dt;
        state.step(dt);

        rowClock += dt;
        while (rowClock >= ROW_DT) {
          rowClock -= ROW_DT;
          history.unshift({
            b: Float32Array.from(state.bands),
            pk: state.peaks.map(function (pk) { return { u: state.u(pk.hz), a: pk.amplitude }; })
          });
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

        // Layer A: ridges, back to front, each occluding the ones behind it.
        for (var r = ROWS - 1; r >= 0; r--) {
          var row = history[r];
          var d = (r + frac) / ROWS;
          var g = geometry(d);
          var xs = [], ys = [];
          for (var c = 0; c <= COLS; c++) {
            var u = c / COLS;
            xs.push(W / 2 + (u - 0.5) * 2 * g.half);
            ys.push(g.y - heightAt(row, u, d) * g.amp);
          }
          ctx.beginPath();
          ctx.moveTo(xs[0], H);
          for (c = 0; c <= COLS; c++) ctx.lineTo(xs[c], ys[c]);
          ctx.lineTo(xs[COLS], H);
          ctx.closePath();
          ctx.fillStyle = 'rgba(7,8,10,0.9)';
          ctx.fill();

          var depthAlpha = Math.pow(1 - d, 1.4);
          var grad = ctx.createLinearGradient(xs[0], 0, xs[COLS], 0);
          for (var k = 0; k < STOPS.length; k++) grad.addColorStop(STOPS[k][0], rgba(STOPS[k][1], Math.min(1, 0.08 + 0.75 * depthAlpha * bright)));
          ctx.beginPath();
          ctx.moveTo(xs[0], ys[0]);
          for (c = 1; c <= COLS; c++) ctx.lineTo(xs[c], ys[c]);
          ctx.strokeStyle = grad;
          ctx.lineWidth = 0.6 + 0.9 * g.persp;
          ctx.stroke();

          // Glints on the crests of nearer rows.
          if (r % 2 === 0 && d < 0.7) {
            ctx.globalCompositeOperation = 'lighter';
            for (c = 0; c <= COLS; c += 2) {
              var v = row ? heightAt(row, c / COLS, d) : 0;
              if (v < 0.35) continue;
              ctx.fillStyle = rgba(colour(c / COLS), Math.min(0.9, 0.7 * v) * depthAlpha * bright);
              ctx.fillRect(xs[c], ys[c] - 1, 1.5, 1.5);
            }
            ctx.globalCompositeOperation = 'source-over';
          }
        }

        // Layer B: resonances.
        drawResonances();

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
          ctx.font = '14px ' + getComputedStyle(document.body).getPropertyValue('--mono');
          ctx.textAlign = 'center';
          ctx.fillText(state.connection === 'connected' ? 'waiting for signal' : state.connection, W / 2, H * 0.3);
        }

        flash *= Math.exp(-dt * 3);
        ctx.restore();
      };

      function drawResonances() {
        var list = Array.from(state.resonances.values());
        list.sort(function (a, b) { return b.data.strength - a.data.strength; });
        var labels = [];
        var g = geometry(RES_DEPTH);
        var row = history[Math.round(RES_DEPTH * ROWS)];
        var mono = getComputedStyle(document.body).getPropertyValue('--mono');
        var avoid = overlays();
        var labelled = {};

        function spire(hz, strength, alpha, main, text, sub) {
          var u = state.u(hz);
          if (u < 0 || u > 1) return;
          var x = W / 2 + (u - 0.5) * 2 * g.half;
          var ground = g.y - heightAt(row, u, RES_DEPTH) * g.amp;
          var top = Math.max(H * 0.06, ground - H * (main ? 0.3 + 0.35 * strength : 0.15 + 0.2 * strength));
          var c = colour(u);

          ctx.globalCompositeOperation = 'lighter';
          var lg = ctx.createLinearGradient(0, ground, 0, top);
          lg.addColorStop(0, rgba(c, 0.95 * alpha));
          lg.addColorStop(1, rgba(c, 0.05 * alpha));
          ctx.strokeStyle = lg;
          ctx.lineWidth = main ? 2 : 1;
          ctx.shadowColor = rgba(c, 0.8 * alpha);
          ctx.shadowBlur = main ? 14 : 6;
          ctx.beginPath(); ctx.moveTo(x, ground); ctx.lineTo(x, top); ctx.stroke();
          ctx.shadowBlur = 0;

          if (main) {
            var rings = 2 + Math.min(3, (text.harmonics || 0));
            var ry = ground - (ground - top) * 0.42;
            for (var i = 0; i < rings; i++) {
              var rw = (26 + i * 30) * (1 + 0.05 * Math.sin(t * 0.8 + i + hz));
              ctx.strokeStyle = rgba(c, (0.35 - i * 0.05) * alpha);
              ctx.lineWidth = 0.8;
              ctx.beginPath(); ctx.ellipse(x, ry, rw, rw * 0.2, 0, 0, Math.PI * 2); ctx.stroke();
            }
          }
          ctx.globalCompositeOperation = 'source-over';

          // Label, nudged down if it would collide with one already placed.
          // A partial shared by two resonances is labelled once.
          if (!text.label || labelled[text.label]) return;
          labelled[text.label] = true;
          var ly = top - 8;
          for (var pass = 0; pass < 2; pass++) {
            for (var j = 0; j < labels.length; j++) {
              if (Math.abs(labels[j].x - x) < 110 && Math.abs(labels[j].y - ly) < 36) ly = labels[j].y + 36;
            }
            for (var o = 0; o < avoid.length; o++) {
              var a = avoid[o];
              if (x + 120 > a.left && x < a.right && ly + 22 > a.top && ly - 16 < a.bottom) ly = a.bottom + 18;
            }
          }
          labels.push({ x: x, y: ly });
          ctx.lineJoin = 'round';
          ctx.strokeStyle = 'rgba(7,8,10,' + (0.85 * alpha).toFixed(3) + ')';
          ctx.lineWidth = 4;
          ctx.font = (main ? 15 : 13) + 'px ' + mono;
          ctx.textAlign = 'left';
          ctx.strokeText(text.label, x + 7, ly);
          ctx.fillStyle = 'rgba(236,235,231,' + alpha.toFixed(3) + ')';
          ctx.fillText(text.label, x + 7, ly);
          if (sub) {
            ctx.font = '13px ' + mono;
            ctx.strokeText(sub, x + 7, ly + 17);
            ctx.fillStyle = 'rgba(169,172,176,' + alpha.toFixed(3) + ')';
            ctx.fillText(sub, x + 7, ly + 17);
          }
        }

        list.forEach(function (r, idx) {
          var d = r.data;
          var main = idx < 4;
          var sub = null;
          if (idx === 0) sub = 'resonant structure · ' + Math.round(d.duration_s) + ' s';
          else if (main) sub = Math.round(d.duration_s) + ' s';
          spire(d.fundamental_hz, d.strength, r.alpha, main,
            { label: Math.round(d.fundamental_hz) + ' Hz' + (d.motif_id ? ' · ' + LO.motifName(d.motif_id) : ''), harmonics: d.harmonics.length }, sub);
          if (main) {
            d.harmonics.forEach(function (h, k) {
              var label = idx < 2 && k < 2 ? Math.round(h.hz) + ' Hz' : '';
              spire(h.hz, h.strength, r.alpha * 0.6, false, { label: label }, null);
            });
          }
        });
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
