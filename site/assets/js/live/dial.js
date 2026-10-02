// The memory dial: remembered motifs as stations on an old radio's tuning
// scale. A log frequency scale with printed markings; each motif is a mark
// at its frequency (taller = heard more often, brighter = more recent,
// glowing = sounding now), textures are shaded bands, and a needle follows
// the current dominant frequency. A small canvas of its own, so the terrain
// drawing is untouched.
(function (LO) {
  'use strict';

  var LO_HZ = 40, HI_HZ = 9000;  // both follow the source's analysed range
  var MARKINGS = [[50, '50'], [100, '100'], [200, '200'], [500, '500'], [1000, '1k'], [2000, '2k'], [5000, '5k'], [10000, '10k']];

  function create(canvas, state) {
    var ctx = canvas.getContext('2d');
    var W = 0, H = 0, dpr = 1;
    var needleLog = null, needleVel = 0;  // log Hz and its velocity
    var marks = [];     // drawn marks, for hover lookup
    var hover = null;
    var mono = getComputedStyle(document.body).getPropertyValue('--mono') || 'monospace';

    function resize() {
      dpr = Math.min(window.devicePixelRatio || 1, 2);
      W = canvas.clientWidth; H = canvas.clientHeight;
      canvas.width = Math.round(W * dpr); canvas.height = Math.round(H * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    }

    var PAD = 24;
    function xOf(hz) {
      var u = Math.log(hz / LO_HZ) / Math.log(HI_HZ / LO_HZ);
      return PAD + Math.max(0, Math.min(1, u)) * (W - 2 * PAD);
    }

    function draw() {
      var now = performance.now();
      var top = state.spectrumMeta && state.spectrumMeta.max_hz;
      if (top > 1000) HI_HZ = Math.min(top, 20000);
      var bottom = state.spectrumMeta && state.spectrumMeta.min_hz;
      LO_HZ = bottom > 40 && bottom < HI_HZ / 4 ? bottom : 40;
      ctx.clearRect(0, 0, W, H);
      var base = H - 26;  // room for the printed markings below

      // Glass: a faint warm glow towards the middle.
      var glass = ctx.createLinearGradient(0, 0, 0, H);
      glass.addColorStop(0, 'rgba(240,178,110,0.00)');
      glass.addColorStop(0.62, 'rgba(240,178,110,0.035)');
      glass.addColorStop(1, 'rgba(240,178,110,0.00)');
      ctx.fillStyle = glass;
      ctx.fillRect(0, 0, W, H);

      // Scale: baseline, decade ticks, printed markings.
      ctx.strokeStyle = 'rgba(228,228,226,0.28)';
      ctx.lineWidth = 1;
      ctx.beginPath(); ctx.moveTo(PAD, base + 0.5); ctx.lineTo(W - PAD, base + 0.5); ctx.stroke();
      ctx.beginPath();
      for (var dec = 10; dec <= 10000; dec *= 10) {
        for (var k = 1; k <= 9; k++) {
          var f = dec * k;
          if (f < LO_HZ || f > HI_HZ) continue;
          var x = Math.round(xOf(f)) + 0.5, len = k === 1 || k === 5 ? 6 : 3;
          ctx.moveTo(x, base); ctx.lineTo(x, base + len);
        }
      }
      ctx.stroke();
      ctx.fillStyle = 'rgba(169,172,176,0.9)';
      ctx.font = '11px ' + mono;
      ctx.textAlign = 'center';
      var shown = MARKINGS.filter(function (m) { return m[0] > LO_HZ * 1.05 && m[0] < HI_HZ * 0.9; });
      shown.forEach(function (m, i) { ctx.fillText(m[1] + (i === shown.length - 1 ? ' Hz' : ''), xOf(m[0]), base + 18); });

      // Stations.
      marks.length = 0;
      var list = [];
      state.motifs.forEach(function (mo) {
        var v = mo.visual;
        if (v && v.frequency_anchor) list.push(mo);
      });
      // Textures first (underneath), then the rarer marks, frequent ones on top.
      list.sort(function (a, b) {
        if (a.kind !== b.kind) return a.kind === 'texture' ? -1 : 1;
        return (a.occurrences || 0) - (b.occurrences || 0);
      });
      list.forEach(function (mo) {
        var v = mo.visual, hue = LO.motifHue(v.visual_seed);
        var x = xOf(v.frequency_anchor);
        var ageMin = (now - (mo.lastSeen || now)) / 60000;
        var recent = mo.active ? 1 : Math.max(0.3, Math.exp(-ageMin / 20));
        var flare = Math.max(0, 1 - (now - (mo.flare || 0)) / 2500);
        var isHover = hover === mo.id;
        if (mo.kind === 'texture') {
          var x0 = xOf(v.frequency_anchor / 1.3), x1 = xOf(v.frequency_anchor * 1.3);
          ctx.fillStyle = 'hsla(' + hue + ',50%,70%,' + (0.06 + 0.1 * recent + 0.2 * flare + (isHover ? 0.1 : 0)).toFixed(3) + ')';
          ctx.fillRect(x0, base - 20, x1 - x0, 20);
          marks.push({ x: x, x0: x0, x1: x1, mo: mo, top: base - 20 });
          return;
        }
        var h = 7 + Math.min(17, 4 * Math.log(1 + (mo.occurrences || 1)));
        var a = 0.35 + 0.6 * recent;
        if (mo.active || flare > 0 || isHover) {
          // Glow: a wide faint pass under the mark.
          ctx.strokeStyle = 'hsla(' + hue + ',70%,75%,' + (0.25 + 0.4 * flare).toFixed(3) + ')';
          ctx.lineWidth = 5;
          ctx.beginPath(); ctx.moveTo(x, base); ctx.lineTo(x, base - h - 2 * flare * 6); ctx.stroke();
        }
        ctx.strokeStyle = 'hsla(' + hue + ',60%,' + (mo.active ? 82 : 70) + '%,' + Math.min(1, a + flare).toFixed(3) + ')';
        ctx.lineWidth = isHover ? 2.5 : 1.6;
        ctx.beginPath(); ctx.moveTo(x, base); ctx.lineTo(x, base - h); ctx.stroke();
        marks.push({ x: x, mo: mo, top: base - h });
      });

      // Station names for the ones sounding now and the most frequent,
      // staggered in two rows and skipped where they would collide.
      var named = list.filter(function (mo) { return mo.kind !== 'texture'; })
        .sort(function (a, b) { return (b.active - a.active) || ((b.occurrences || 0) - (a.occurrences || 0)); })
        .slice(0, 10);
      var rows = [[], []];
      ctx.font = '11px ' + mono;
      named.forEach(function (mo) {
        var label = LO.motifName(mo.id).replace('motif ', 'M');
        var x = xOf(mo.visual.frequency_anchor), w = ctx.measureText(label).width + 8;
        for (var r = 0; r < 2; r++) {
          var free = rows[r].every(function (s) { return x + w / 2 < s.a || x - w / 2 > s.b; });
          if (!free) continue;
          rows[r].push({ a: x - w / 2, b: x + w / 2 });
          var y = r === 0 ? base - 29 : base - 42;
          ctx.fillStyle = 'rgba(236,235,231,' + (mo.active ? 0.95 : 0.6).toFixed(2) + ')';
          ctx.fillText(label, x, y);
          return;
        }
      });

      // Needle: a pointer on a cord-driven carriage, as on a 1920s radio. It
      // swings to the current dominant frequency on a damped spring (a little
      // overshoot), in log-frequency space.
      var f = state.features && state.features.dominant_frequency_hz;
      if (f > 0) {
        var target = Math.log(Math.max(LO_HZ, Math.min(HI_HZ, f)));
        if (needleLog == null) { needleLog = target; needleVel = 0; }
        needleVel = (needleVel + (target - needleLog) * 0.018) * 0.86;
        needleLog += needleVel;
        drawNeedle(xOf(Math.exp(needleLog)), base);
      }
      requestAnimationFrame(draw);
    }

    function drawNeedle(nx, base) {
      var top = 3, tip = base + 3;
      // Lamp glow behind the dial glass.
      var glow = ctx.createRadialGradient(nx, base - 14, 0, nx, base - 14, 34);
      glow.addColorStop(0, 'rgba(255,170,90,0.16)');
      glow.addColorStop(1, 'rgba(255,170,90,0)');
      ctx.fillStyle = glow;
      ctx.fillRect(nx - 34, 0, 68, H);
      // Brass rail the carriage runs on.
      ctx.strokeStyle = 'rgba(196,160,98,0.35)';
      ctx.lineWidth = 1;
      ctx.beginPath(); ctx.moveTo(PAD - 6, top + 3); ctx.lineTo(W - PAD + 6, top + 3); ctx.stroke();
      // Shadow on the glass, slightly offset.
      ctx.fillStyle = 'rgba(0,0,0,0.45)';
      ctx.beginPath();
      ctx.moveTo(nx - 1.9 + 1.8, top + 6); ctx.lineTo(nx + 1.9 + 1.8, top + 6); ctx.lineTo(nx + 1.8, tip + 1.5);
      ctx.closePath(); ctx.fill();
      // Tapered pointer: broad under the carriage, fine at the tip.
      var red = ctx.createLinearGradient(nx - 2, 0, nx + 2, 0);
      red.addColorStop(0, '#8e2414');
      red.addColorStop(0.5, '#e0563a');
      red.addColorStop(1, '#8e2414');
      ctx.fillStyle = red;
      ctx.beginPath();
      ctx.moveTo(nx - 2, top + 6); ctx.lineTo(nx + 2, top + 6); ctx.lineTo(nx + 0.3, tip); ctx.lineTo(nx - 0.3, tip);
      ctx.closePath(); ctx.fill();
      // Carriage: a small brass block with a highlight and a dark edge.
      var bx = nx - 6, by = top, bw = 12, bh = 7;
      var brass = ctx.createLinearGradient(0, by, 0, by + bh);
      brass.addColorStop(0, '#f1d9a0');
      brass.addColorStop(0.45, '#c09452');
      brass.addColorStop(1, '#6e4f22');
      ctx.fillStyle = brass;
      ctx.beginPath();
      ctx.moveTo(bx + 1.5, by); ctx.arcTo(bx + bw, by, bx + bw, by + bh, 1.5); ctx.arcTo(bx + bw, by + bh, bx, by + bh, 1.5);
      ctx.arcTo(bx, by + bh, bx, by, 1.5); ctx.arcTo(bx, by, bx + bw, by, 1.5); ctx.closePath(); ctx.fill();
      ctx.strokeStyle = 'rgba(40,28,10,0.8)';
      ctx.lineWidth = 0.6;
      ctx.stroke();
      // A rivet where the pointer is fixed.
      ctx.fillStyle = '#3a2a12';
      ctx.beginPath(); ctx.arc(nx, by + bh / 2, 1.1, 0, Math.PI * 2); ctx.fill();
    }

    // Nearest mark to a point, for hover and tap.
    function markAt(x, y) {
      var best = null, bd = 10;
      marks.forEach(function (m) {
        var d = m.x0 != null && x >= m.x0 && x <= m.x1 ? 6 : Math.abs(m.x - x);
        if (d < bd || (d === bd && best && best.mo.kind === 'texture')) { bd = d; best = m; }
      });
      return best;
    }

    resize();
    window.addEventListener('resize', resize);
    requestAnimationFrame(draw);
    return {
      markAt: markAt,
      setHover: function (id) { hover = id; }
    };
  }

  LO.dial = { create: create };
})(window.LO = window.LO || {});
