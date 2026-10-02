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

      // Needle: a slide-rule cursor that swings to the current dominant
      // frequency on a damped spring (a little overshoot), in log-frequency
      // space.
      var f = state.features && state.features.dominant_frequency_hz;
      if (f > 0) {
        var target = Math.log(Math.max(LO_HZ, Math.min(HI_HZ, f)));
        if (needleLog == null) { needleLog = target; needleVel = 0; }
        needleVel = (needleVel + (target - needleLog) * 0.018) * 0.86;
        needleLog += needleVel;
        drawNeedle(xOf(Math.exp(needleLog)), base, f);
      }
      requestAnimationFrame(draw);
    }

    // Slide-rule cursor: a narrow glass window over the scale with a fine
    // orange hairline, and a small tag above it reading the frequency.
    function drawNeedle(nx, base, hz) {
      var tagH = 13, top = tagH + 2, bottom = base + 4, half = 7;
      // Glass window: faintly lit, with lighter edges.
      ctx.fillStyle = 'rgba(236,235,231,0.045)';
      roundRect(nx - half, top, 2 * half, bottom - top, 2); ctx.fill();
      ctx.strokeStyle = 'rgba(236,235,231,0.2)';
      ctx.lineWidth = 1;
      roundRect(nx - half + 0.5, top + 0.5, 2 * half - 1, bottom - top - 1, 2); ctx.stroke();
      // Hairline with a soft glow.
      ctx.strokeStyle = 'rgba(240,120,84,0.25)';
      ctx.lineWidth = 3;
      ctx.beginPath(); ctx.moveTo(nx, top + 2); ctx.lineTo(nx, bottom - 2); ctx.stroke();
      ctx.strokeStyle = '#f07854';
      ctx.lineWidth = 1;
      ctx.beginPath(); ctx.moveTo(nx, top + 2); ctx.lineTo(nx, bottom - 2); ctx.stroke();
      // Readout tag, kept inside the canvas.
      var text = hz >= 1000 ? (hz / 1000).toFixed(hz >= 10000 ? 1 : 2) + ' kHz' : Math.round(hz) + ' Hz';
      ctx.font = '10px ' + mono;
      var tw = ctx.measureText(text).width + 10;
      var tx = Math.max(1, Math.min(W - tw - 1, nx - tw / 2));
      ctx.fillStyle = 'rgba(7,8,10,0.92)';
      roundRect(tx, 1, tw, tagH - 1, 3); ctx.fill();
      ctx.strokeStyle = 'rgba(240,120,84,0.7)';
      roundRect(tx + 0.5, 1.5, tw - 1, tagH - 2, 3); ctx.stroke();
      ctx.fillStyle = '#ecebe7';
      ctx.textAlign = 'center';
      ctx.textBaseline = 'middle';
      ctx.fillText(text, tx + tw / 2, 1 + (tagH - 1) / 2 + 0.5);
      ctx.textBaseline = 'alphabetic';
    }
    function roundRect(x, y, w, h, r) {
      ctx.beginPath();
      ctx.moveTo(x + r, y); ctx.arcTo(x + w, y, x + w, y + h, r); ctx.arcTo(x + w, y + h, x, y + h, r);
      ctx.arcTo(x, y + h, x, y, r); ctx.arcTo(x, y, x + w, y, r); ctx.closePath();
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
