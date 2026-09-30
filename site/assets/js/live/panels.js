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

  function logPos(hz, lo, hi) { return Math.log(hz / lo) / Math.log(hi / lo); }

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
    this.hmRange = [40, 5000];
    this.eventsEl = $('events');
    this.events = [];
    this.axesFor = null;

    axisY($('hm-yaxis'), [[40, '40 Hz'], [160, '160 Hz'], [640, '640 Hz'], [2560, '2.5 kHz']], this.hmRange[0], this.hmRange[1]);
  }

  Panels.prototype.axes = function (meta) {
    var key = meta.min_hz + ':' + meta.max_hz;
    if (this.axesFor === key) return;
    this.axesFor = key;
    axisY($('sg-yaxis'), [[40, '40 Hz'], [150, '150 Hz'], [600, '600 Hz'], [2400, '2.4 kHz'], [9000, '9 kHz']], meta.min_hz, meta.max_hz);
    axisX($('sp-xaxis'), [[30, '30'], [100, '100'], [300, '300'], [1000, '1k'], [3000, '3k'], [9000, '9k Hz']], meta.min_hz, meta.max_hz);
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
    $('f-harmonics').textContent = strongest && strongest.data.harmonics.length
      ? strongest.data.harmonics.map(function (h) { return fmt.hz(h.hz); }).join(', ')
      : '—';
    $('f-centroid').textContent = fmt.hz(fr.centroid.target);
    $('f-flux').textContent = fmt.num(fr.flux.target);
    $('f-entropy').textContent = fmt.num(fr.entropy.target);
    $('f-energy').textContent = st.levelDb != null ? fmt.num(fr.energy.target) + '  (' + st.levelDb.toFixed(1) + ' dBFS)' : '—';
    $('f-harmonicity').textContent = fmt.num(fr.harmonicity.target);
    $('f-state').textContent = f ? fmt.state(f.state) : '—';

    $('ro-state').textContent = f ? fmt.state(f.state) : '—';
    $('ro-harmonicity').textContent = f ? fmt.num(f.harmonicity) : '—';
    $('ro-entropy').textContent = f ? fmt.num(f.entropy) : '—';
    $('ro-novelty').textContent = f ? fmt.num(f.novelty) : '—';
    if (st.lastTimestamp) $('ro-time').textContent = fmt.local(st.lastTimestamp);
  };

  Panels.prototype.connection = function (s, stream) {
    var el = $('conn');
    var shown = s === 'connected' && stream && stream.stream !== 'connected' ? stream.stream : s;
    el.dataset.state = shown;
    var text = shown === 'connected' ? 'live' : shown;
    if (shown === 'connected' && stream && stream.input && stream.input.indexOf('file:') === 0) text = 'replay · ' + stream.input.slice(5);
    el.textContent = text;
  };

  // Events list: transients, resonance starts and ends.
  Panels.prototype.event = function (env) {
    var p = env.payload, text, kind;
    if (env.type === 'event.transient') {
      kind = 'transient';
      text = (p.bandwidth_hz > 1500 ? 'Broadband transient' : 'Transient') + ' (strength ' + p.strength.toFixed(2) + ')';
    } else if (env.type === 'event.resonance') {
      if (p.status === 'start') {
        kind = 'resonance';
        text = 'Resonant structure (' + Math.round(p.fundamental_hz) + ' Hz' + (p.harmonics.length ? ', ' + p.harmonics.length + ' harmonic' + (p.harmonics.length > 1 ? 's' : '') : '') + ')';
      } else if (p.status === 'end') {
        kind = 'end';
        text = 'Resonance ' + Math.round(p.fundamental_hz) + ' Hz faded after ' + Math.round(p.duration_s) + ' s';
      }
    }
    if (env.type === 'event.resonance') this.harmonicPoint(env);
    if (!text) return;
    // Short-lived resonances churn; only list those that held for a while.
    if (kind === 'end' && p.duration_s < 20) return;
    this.events.unshift({ time: new Date(env.timestamp), text: text, kind: kind });
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
      li.appendChild(tm); li.appendChild(sp);
      el.appendChild(li);
    });
  };

  Panels.prototype.harmonicPoint = function (env) {
    var p = env.payload, t = Date.parse(env.timestamp);
    if (p.status === 'end') return;
    this.hmPoints.push({ t: t, hz: p.fundamental_hz, s: p.strength, h: false });
    var self = this;
    p.harmonics.forEach(function (h) { self.hmPoints.push({ t: t, hz: h.hz, s: h.strength, h: true }); });
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
    [40, 160, 640, 2560].forEach(function (hz) {
      var y = h - logPos(hz, lo, hi) * h;
      x.beginPath(); x.moveTo(0, y); x.lineTo(w, y); x.stroke();
    });
    x.globalCompositeOperation = 'lighter';
    this.hmPoints.forEach(function (pt) {
      var px = w - (now - pt.t) / span * w;
      var py = h - logPos(pt.hz, lo, hi) * h;
      var r = pt.h ? 1 + pt.s * 1.5 : 1.5 + pt.s * 4;
      x.fillStyle = pt.h ? 'rgba(156,200,242,0.35)' : 'rgba(240,178,110,0.35)';
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
