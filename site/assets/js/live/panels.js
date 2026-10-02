// Lower analysis strip: spectrogram, spectrum, feature table, recent events
// and the harmonic map. Plain canvas 2D and DOM; redrawn when data arrives,
// not every animation frame.
(function (LO) {
  'use strict';

  var $ = function (id) { return document.getElementById(id); };
  var fmt = LO.fmt;

  // Inferno-like map for the spectrogram: black → violet → orange → pale yellow.
  var HEAT = [[0, [8, 6, 16]], [0.3, [62, 20, 96]], [0.55, [168, 52, 96]], [0.75, [236, 118, 50]], [1, [252, 236, 164]]];
  function heat(v) {
    v = v < 0 ? 0 : v > 1 ? 1 : v;
    for (var i = 1; i < HEAT.length; i++) {
      if (v <= HEAT[i][0]) {
        var a = HEAT[i - 1], b = HEAT[i], t = (v - a[0]) / (b[0] - a[0]);
        return [a[1][0] + (b[1][0] - a[1][0]) * t, a[1][1] + (b[1][1] - a[1][1]) * t, a[1][2] + (b[1][2] - a[1][2]) * t];
      }
    }
    return HEAT[HEAT.length - 1][1];
  }

  function ago(s) {
    if (s == null) return '—';
    if (s < 90) return Math.round(s) + ' s';
    if (s < 5400) return Math.round(s / 60) + ' min';
    return (s / 3600).toFixed(1) + ' h';
  }

  function logPos(hz, lo, hi) { return Math.log(hz / lo) / Math.log(hi / lo); }

  // Frequency ticks for lo..hi: 1-3 per decade, or 1-2-5 when that gives
  // fewer than four (a narrow range such as VLF's 0.8–12 kHz).
  function freqTicks(lo, hi) {
    function pick(steps) {
      var out = [];
      for (var dec = 10; dec <= 100000; dec *= 10) {
        steps.forEach(function (k) {
          var f = dec * k;
          if (f >= lo * 1.08 && f <= hi * 0.92) out.push(f);
        });
      }
      return out;
    }
    var t = pick([1, 3]);
    return t.length >= 4 ? t : pick([1, 2, 5]);
  }
  function hzLabel(f, unit) {
    var s = f >= 1000 ? (f / 1000) + (unit ? ' kHz' : 'k') : String(f) + (unit ? ' Hz' : '');
    return s;
  }

  function axisY(el, ticks, lo, hi) {
    el.innerHTML = '';
    ticks.forEach(function (t) {
      var li = document.createElement('li');
      li.textContent = t[1];
      li.style.top = (100 - logPos(t[0], lo, hi) * 100) + '%';
      el.appendChild(li);
    });
  }

  function axisX(el, ticks, lo, hi) {
    el.innerHTML = '';
    ticks.forEach(function (t) {
      var li = document.createElement('li');
      li.textContent = t[1];
      li.style.left = (logPos(t[0], lo, hi) * 100) + '%';
      el.appendChild(li);
    });
  }

  function Panels(state) {
    this.state = state;
    this.sg = $('spectrogram');
    this.sgx = this.sg.getContext('2d');
    this.sgx.fillStyle = '#060509';
    this.sgx.fillRect(0, 0, this.sg.width, this.sg.height);
    this.sp = $('spectrum');
    this.spx = this.sp.getContext('2d');
    this.hm = $('harmonic-map');
    this.hmx = this.hm.getContext('2d');
    this.hmPoints = [];        // {t: ms, hz, s, h: bool}
    this.hmRange = [40, 5000];  // follows a narrowed analysis range (axes())
    this.eventsEl = $('events');
    this.events = [];
    this.axesFor = null;

    this.hmTicks = [40, 160, 640, 2560];
    axisY($('hm-yaxis'), this.hmTicks.map(function (f) { return [f, hzLabel(f, true)]; }), this.hmRange[0], this.hmRange[1]);
  }

  Panels.prototype.axes = function (meta) {
    var key = meta.min_hz + ':' + meta.max_hz;
    if (this.axesFor === key) return;
    this.axesFor = key;
    var ticks = freqTicks(meta.min_hz, meta.max_hz);
    axisY($('sg-yaxis'), ticks.map(function (f) { return [f, hzLabel(f, true)]; }), meta.min_hz, meta.max_hz);
    axisX($('sp-xaxis'), ticks.map(function (f, i) {
      return [f, hzLabel(f, false) + (i === ticks.length - 1 ? ' Hz' : '')];
    }), meta.min_hz, meta.max_hz);
    // The harmonic map shows fundamentals: 40 Hz–5 kHz by default, the
    // whole range when the source narrows it (e.g. VLF radio).
    if (meta.min_hz > 40) {
      this.hmRange = [meta.min_hz, meta.max_hz];
      this.hmTicks = ticks;
      axisY($('hm-yaxis'), ticks.map(function (f) { return [f, hzLabel(f, true)]; }), meta.min_hz, meta.max_hz);
    }
  };

  // signal.spectrum, 10 Hz: one spectrogram column per message (600 px = 60 s).
  Panels.prototype.spectrum = function (p) {
    this.axes(p);
    var c = this.sg, x = this.sgx, w = c.width, h = c.height, bands = p.bands, n = bands.length;
    x.drawImage(c, 1, 0, w - 1, h, 0, 0, w - 1, h);
    for (var i = 0; i < n; i++) {
      var y0 = h - Math.round((i + 1) * h / n), y1 = h - Math.round(i * h / n);
      var col = heat(Math.pow(bands[i], 1.3));
      x.fillStyle = 'rgb(' + (col[0] | 0) + ',' + (col[1] | 0) + ',' + (col[2] | 0) + ')';
      x.fillRect(w - 1, y0, 1, y1 - y0);
    }

    // Spectrum: the band contour plus the peaks as bright needles.
    var s = this.sp, sx = this.spx, sw = s.width, sh = s.height;
    sx.clearRect(0, 0, sw, sh);
    sx.beginPath();
    for (i = 0; i < n; i++) {
      var px = (i + 0.5) / n * sw, py = sh - 4 - bands[i] * (sh * 0.55);
      if (i === 0) sx.moveTo(px, py); else sx.lineTo(px, py);
    }
    sx.strokeStyle = 'rgba(156,200,242,0.55)';
    sx.lineWidth = 1.2;
    sx.stroke();
    sx.globalCompositeOperation = 'lighter';
    (p.peaks || []).forEach(function (pk) {
      var u = logPos(pk.hz, p.min_hz, p.max_hz);
      var px = u * sw, top = sh - 4 - pk.amplitude * (sh * 0.92);
      var g = sx.createLinearGradient(0, sh, 0, top);
      g.addColorStop(0, 'rgba(210,230,255,0.9)');
      g.addColorStop(1, 'rgba(210,230,255,0.1)');
      sx.strokeStyle = g;
      sx.lineWidth = 1.5;
      sx.beginPath(); sx.moveTo(px, sh - 2); sx.lineTo(px, top); sx.stroke();
    });
    sx.globalCompositeOperation = 'source-over';
  };

  // feature.state (2 Hz) and signal.frame (10 Hz) → tables and readout.
  Panels.prototype.features = function () {
    var st = this.state, f = st.features;
    var fr = st.frame;
    var strongest = null;
    st.resonances.forEach(function (r) {
      if (!r.ending && (!strongest || r.data.strength > strongest.data.strength)) strongest = r;
    });
    $('f-dominant').textContent = f ? fmt.hz(f.dominant_frequency_hz) : '—';
    var harm = strongest && strongest.data.harmonics.length
      ? strongest.data.harmonics.map(function (h) { return fmt.hz(h.hz); }).join(', ')
      : '—';
    $('f-harmonics').textContent = harm;
    $('f-harmonics').title = harm;
    $('f-centroid').textContent = fmt.hz(fr.centroid.target);
    $('f-flux').textContent = fmt.num(fr.flux.target);
    $('f-entropy').textContent = fmt.num(fr.entropy.target);
    $('f-energy').textContent = st.levelDb != null ? fmt.num(fr.energy.target) + '  (' + st.levelDb.toFixed(1) + ' dBFS)' : '—';
    $('f-harmonicity').textContent = fmt.num(fr.harmonicity.target);
    $('f-state').textContent = f ? fmt.state(f.state) : '—';
    var mem = st.memory && st.memory['1h'];
    $('f-motifs').textContent = st.motifs.size ? String(st.motifs.size) : (mem ? String(mem.known_motifs) : '—');
    $('f-dominant-motifs').textContent = mem && mem.dominant_motifs.length
      ? mem.dominant_motifs.map(LO.motifName).join(', ') : '—';

    $('ro-state').textContent = f ? fmt.state(f.state) : '—';
    $('ro-harmonicity').textContent = f ? fmt.num(f.harmonicity) : '—';
    $('ro-entropy').textContent = f ? fmt.num(f.entropy) : '—';
    $('ro-novelty').textContent = f ? fmt.num(f.novelty) : '—';
    if (st.lastTimestamp) $('ro-time').textContent = fmt.local(st.lastTimestamp);
  };

  // Interpretation: a teleprinter line above the dial. The newest text types
  // in on top; earlier ones sit below and can be scrolled (wheel, trackpad,
  // swipe) one at a time.
  var reducedMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  Panels.prototype.interpretation = function () {
    var st = this.state, self = this;
    if (!this.tpInit) this.initTeleprinter();
    if (st.narrativeVersion === this.shownNarrative || !st.narratives.length) return;
    this.shownNarrative = st.narrativeVersion;

    var scroll = $('tp-scroll'), list = $('tp-list');
    var atTop = scroll.scrollTop < 4;
    var firstBefore = list.firstElementChild;
    var heightBefore = list.scrollHeight;
    var newest = st.narratives[0];
    var isNew = newest.text !== this.lastTyped && !newest.old;

    list.innerHTML = '';
    st.narratives.forEach(function (n) {
      var li = document.createElement('li');
      var tm = document.createElement('time');
      tm.dateTime = n.time.toISOString();
      tm.textContent = fmt.localShort(n.time);
      var tx = document.createElement('span');
      tx.textContent = n.text;
      li.appendChild(tm); li.appendChild(tx);
      list.appendChild(li);
    });

    if (isNew) {
      this.lastTyped = newest.text;
      $('tp-live').textContent = newest.text;  // announced once, in full
      if (atTop || !firstBefore || firstBefore.classList.contains('teleprinter__wait')) {
        scroll.scrollTop = 0;
        this.teletype(list.firstElementChild.querySelector('span'), newest.text);
      } else {
        // Reading older ones: keep the place, offer the new one.
        scroll.scrollTop += list.scrollHeight - heightBefore;
        this.pendingNew = true;
      }
    }
    this.updateMore();
  };

  Panels.prototype.initTeleprinter = function () {
    this.tpInit = true;
    var self = this, scroll = $('tp-scroll'), more = $('tp-more');
    // The jog wheel replaces the scrollbar (see jog.js).
    this.jog = LO.jog.create($('tp-jog'), scroll, $('tp-list'), function (i) {
      if (i === 0) self.pendingNew = false;
    });
    scroll.addEventListener('scroll', function () {
      if (scroll.scrollTop < 4) self.pendingNew = false;
      self.updateMore();
    }, { passive: true });
    more.addEventListener('click', function () {
      scroll.scrollTo({ top: 0, behavior: reducedMotion ? 'auto' : 'smooth' });
      self.pendingNew = false;
      self.updateMore();
    });
  };

  // "↑ new" while reading older texts, otherwise the position in the list.
  Panels.prototype.updateMore = function () {
    var scroll = $('tp-scroll'), list = $('tp-list'), more = $('tp-more');
    var n = list.children.length;
    if (this.pendingNew) {
      more.hidden = false; more.textContent = '↑ new'; more.classList.add('is-new');
      return;
    }
    more.classList.remove('is-new');
    var jog = $('tp-jog');
    if (n < 2) { more.hidden = true; jog.hidden = true; return; }
    var idx = 0, top = scroll.scrollTop;
    for (var i = 0; i < n; i++) if (list.children[i].offsetTop - list.offsetTop <= top + 2) idx = i;
    if (jog.hidden) { jog.hidden = false; this.jog.redraw(); }
    jog.setAttribute('aria-valuemin', '1');
    jog.setAttribute('aria-valuemax', String(n));
    jog.setAttribute('aria-valuenow', String(idx + 1));
    jog.setAttribute('aria-valuetext', idx === 0 ? 'newest of ' + n : (idx + 1) + ' of ' + n);
    more.hidden = false;
    more.textContent = idx === 0 ? '↓ ' + (n - 1) + ' earlier' : (idx + 1) + ' / ' + n;
  };

  // Types text into el (about 40 letters per second); instant with reduced motion.
  Panels.prototype.teletype = function (el, text) {
    clearInterval(this.typing);
    if (reducedMotion) { el.textContent = text; return; }
    var self = this, i = 0;
    el.textContent = '';
    el.classList.add('is-typing');
    this.typing = setInterval(function () {
      i = Math.min(text.length, i + 2);
      el.textContent = text.slice(0, i);
      if (i >= text.length) {
        clearInterval(self.typing);
        setTimeout(function () { el.classList.remove('is-typing'); }, 1500);
      }
    }, 50);
  };

  Panels.prototype.connection = function (s, stream) {
    var el = $('conn');
    var shown = s === 'connected' && stream && stream.stream !== 'connected' ? stream.stream : s;
    el.dataset.state = shown;
    var text = shown === 'connected' ? 'live' : shown;
    if (shown === 'connected' && stream && stream.input && stream.input.indexOf('file:') === 0) text = 'replay · ' + stream.input.slice(5);
    el.textContent = text;
  };

  // Events list: transients, resonance starts and ends, motif detections
  // and returns. Each has a short line (fits a narrow column) and the full
  // sentence on hover.
  Panels.prototype.event = function (env) {
    var p = env.payload, text, full, kind;
    var hz = function (v) { return Math.round(v) + ' Hz'; };
    var cap = function (s) { return s.charAt(0).toUpperCase() + s.slice(1); };
    if (env.type === 'event.transient') {
      kind = 'transient';
      var what = p.bandwidth_hz > 1500 ? 'Broadband transient' : 'Transient';
      text = what + ' · ' + p.strength.toFixed(2);
      full = what + ' (strength ' + p.strength.toFixed(2) + ')';
    } else if (env.type === 'event.resonance') {
      var n = p.harmonics.length;
      if (p.status === 'start') {
        kind = 'resonance';
        text = 'Resonance ' + hz(p.fundamental_hz) + (n ? ' · ' + n + ' harm.' : '');
        full = 'Resonant structure at ' + hz(p.fundamental_hz) + (n ? ' with ' + n + ' harmonic' + (n > 1 ? 's' : '') : '') +
          (p.motif_id ? ' (' + LO.motifName(p.motif_id) + ')' : '');
      } else if (p.status === 'end') {
        kind = 'end';
        text = hz(p.fundamental_hz) + ' faded · ' + Math.round(p.duration_s) + ' s';
        full = 'Resonance at ' + hz(p.fundamental_hz) + ' faded after ' + Math.round(p.duration_s) + ' s';
      }
    }
    if (env.type === 'motif.detected') {
      kind = 'motif';
      var at = p.kind === 'texture' ? 'texture ~' + fmt.hz(p.signature.centroid_hz) : fmt.hz(p.signature.fundamental_hz);
      text = 'New ' + LO.motifName(p.motif_id) + ' · ' + at;
      full = 'New ' + LO.motifName(p.motif_id) + ': ' + (p.kind === 'texture' ? 'a texture around ' + fmt.hz(p.signature.centroid_hz)
        : 'a resonance at ' + fmt.hz(p.signature.fundamental_hz)) + ', heard ' + p.occurrences + ' times';
    } else if (env.type === 'motif.returned') {
      kind = 'motif';
      var name = cap(LO.motifName(p.motif_id));
      text = name + ' back · ' + ago(p.last_seen_s) + ' · ' + p.similarity.toFixed(2);
      full = name + ' returned after ' + ago(p.last_seen_s) + ' (similarity ' + p.similarity.toFixed(2) + ')';
    }
    if (env.type === 'event.resonance') this.harmonicPoint(env);
    if (!text) return;
    // Short-lived resonances churn; only list those that held for a while.
    if (kind === 'end' && p.duration_s < 20) return;
    this.events.unshift({ time: new Date(env.timestamp), text: text, full: full || text, kind: kind });
    this.events.length = Math.min(this.events.length, 8);
    this.renderEvents();
  };

  Panels.prototype.renderEvents = function () {
    var el = this.eventsEl;
    el.innerHTML = '';
    this.events.forEach(function (e) {
      var li = document.createElement('li');
      li.dataset.kind = e.kind;
      var tm = document.createElement('time');
      tm.dateTime = e.time.toISOString();
      tm.textContent = fmt.localShort(e.time);
      var sp = document.createElement('span');
      sp.textContent = e.text;
      sp.title = e.full;
      li.appendChild(tm); li.appendChild(sp);
      el.appendChild(li);
    });
  };

  Panels.prototype.harmonicPoint = function (env) {
    var p = env.payload, t = Date.parse(env.timestamp);
    if (p.status === 'end') return;
    var mo = p.motif_id ? this.state.motifs.get(p.motif_id) : null;
    var hue = mo && mo.visual ? LO.motifHue(mo.visual.visual_seed) : null;
    this.hmPoints.push({ t: t, hz: p.fundamental_hz, s: p.strength, h: false, hue: hue });
    var self = this;
    p.harmonics.forEach(function (h) { self.hmPoints.push({ t: t, hz: h.hz, s: h.strength, h: true, hue: hue }); });
  };

  // Harmonic map: resonance fundamentals (gold) and their overtones over the
  // last five minutes. Motif recognition will add identity here in phase 2.
  Panels.prototype.drawHarmonicMap = function () {
    var c = this.hm, x = this.hmx, w = c.width, h = c.height;
    var now = this.state.lastTimestamp ? this.state.lastTimestamp.getTime() : Date.now();
    var span = 5 * 60 * 1000, lo = this.hmRange[0], hi = this.hmRange[1];
    this.hmPoints = this.hmPoints.filter(function (pt) { return pt.t > now - span; });
    x.clearRect(0, 0, w, h);
    x.strokeStyle = 'rgba(228,228,226,0.07)';
    x.lineWidth = 1;
    this.hmTicks.forEach(function (hz) {
      var y = h - logPos(hz, lo, hi) * h;
      x.beginPath(); x.moveTo(0, y); x.lineTo(w, y); x.stroke();
    });
    x.globalCompositeOperation = 'lighter';
    this.hmPoints.forEach(function (pt) {
      var px = w - (now - pt.t) / span * w;
      var py = h - logPos(pt.hz, lo, hi) * h;
      var r = pt.h ? 1 + pt.s * 1.5 : 1.5 + pt.s * 4;
      if (pt.hue != null) x.fillStyle = 'hsla(' + pt.hue + ',60%,' + (pt.h ? 72 : 66) + '%,' + (pt.h ? 0.4 : 0.55) + ')';
      else x.fillStyle = pt.h ? 'rgba(156,200,242,0.25)' : 'rgba(240,178,110,0.25)';
      x.beginPath(); x.arc(px, py, r, 0, Math.PI * 2); x.fill();
    });
    x.globalCompositeOperation = 'source-over';
  };

  Panels.prototype.replayRecent = function (recent) {
    var self = this;
    this.events = [];
    (recent || []).forEach(function (env) { self.event(env); });
  };

  LO.Panels = Panels;
})(window.LO = window.LO || {});
