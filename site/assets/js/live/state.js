// Frontend state model (§19) with browser-side smoothing (§20). Analytical
// values arrive at 2–10 Hz; the renderer reads smoothed values every frame.
(function (LO) {
  'use strict';

  var BANDS = 48;

  function Smooth(tau) { this.v = 0; this.target = 0; this.tau = tau; this.init = false; }
  Smooth.prototype.set = function (x) {
    if (typeof x !== 'number' || !isFinite(x)) return;
    this.target = x;
    if (!this.init) { this.v = x; this.init = true; }
  };
  Smooth.prototype.step = function (dt) {
    this.v += (this.target - this.v) * (1 - Math.exp(-dt / this.tau));
    return this.v;
  };

  function State() {
    this.connection = 'connecting';
    this.stream = null;           // last system.status payload
    this.lastTimestamp = null;    // observation time of the newest event
    this.frame = {
      energy: new Smooth(0.25), peak: new Smooth(0.08), centroid: new Smooth(0.6),
      flux: new Smooth(0.3), entropy: new Smooth(0.8), harmonicity: new Smooth(0.8)
    };
    this.bands = new Float32Array(BANDS);        // smoothed, for rendering
    this.bandTarget = new Float32Array(BANDS);   // latest received
    this.spectrumMeta = { min_hz: 20, max_hz: 11000 };
    this.peaks = [];
    this.features = null;                        // last feature.state payload
    this.resonances = new Map();                 // id -> entity
    this.motifs = new Map();                     // motif id -> {id, kind, visual, signature, occurrences, active, lastSeen, flare}
    this.memory = {};                            // latest memory.summary per window
    this.narratives = [];                        // newest first: {text, model, time}
    this.narrativeVersion = 0;
    this.spectrumListeners = [];
    this.eventListeners = [];
  }

  State.prototype.onSpectrum = function (f) { this.spectrumListeners.push(f); };
  State.prototype.onEvent = function (f) { this.eventListeners.push(f); };

  State.prototype.apply = function (env, replay) {
    var p = env.payload || {};
    if (env.timestamp) this.lastTimestamp = new Date(env.timestamp);
    switch (env.type) {
      case 'signal.frame':
        this.frame.energy.set(p.energy);
        this.frame.peak.set(p.peak);
        this.frame.centroid.set(p.spectral_centroid_hz);
        this.frame.flux.set(p.spectral_flux);
        this.frame.entropy.set(p.spectral_entropy);
        this.frame.harmonicity.set(p.harmonicity);
        this.levelDb = p.level_db;
        break;
      case 'signal.spectrum':
        if (Array.isArray(p.bands)) {
          for (var i = 0; i < BANDS && i < p.bands.length; i++) this.bandTarget[i] = p.bands[i];
        }
        this.spectrumMeta = { min_hz: p.min_hz, max_hz: p.max_hz };
        this.peaks = p.peaks || [];
        for (var j = 0; j < this.spectrumListeners.length; j++) this.spectrumListeners[j](p, env);
        break;
      case 'feature.state':
        this.features = p;
        break;
      case 'system.status':
        this.stream = p;
        break;
      case 'event.resonance':
        this.applyResonance(env, p, replay);
        if (p.motif_id) {
          var mo = this.motifs.get(p.motif_id);
          if (mo) { mo.active = p.status !== 'end'; mo.lastSeen = performance.now(); }
        }
        break;
      case 'motif.detected':
      case 'motif.returned':
        this.upsertMotif({
          motif_id: p.motif_id, kind: p.kind, visual: p.visual, signature: p.signature,
          occurrences: p.occurrences || p.occurrence, active: true
        }, !replay);
        break;
      case 'memory.summary':
        this.memory[p.window] = p;
        break;
      case 'narrative.update':
        if (!this.narratives.length || this.narratives[0].text !== p.text) {
          this.narratives.unshift({ text: p.text, model: p.model, time: new Date(env.timestamp) });
          this.narratives.length = Math.min(this.narratives.length, 20);
          this.narrativeVersion++;
        }
        break;
    }
    if (env.type.indexOf('event.') === 0 || env.type.indexOf('motif.') === 0) {
      for (var k = 0; k < this.eventListeners.length; k++) this.eventListeners[k](env, replay);
    }
  };

  State.prototype.applyResonance = function (env, p, replay) {
    var r = this.resonances.get(p.id);
    if (p.status === 'end') {
      if (r) { r.ending = true; r.data = p; }
      return;
    }
    if (!r) {
      r = { id: p.id, born: performance.now(), alpha: replay ? 1 : 0, ending: false, data: p };
      this.resonances.set(p.id, r);
    }
    r.data = p;
    r.ending = false;
  };

  // Adds or refreshes a motif; flare marks a fresh detection or return.
  State.prototype.upsertMotif = function (v, flare) {
    var mo = this.motifs.get(v.motif_id) || { id: v.motif_id, flare: 0 };
    mo.kind = v.kind || mo.kind;
    mo.visual = v.visual || mo.visual;
    mo.signature = v.signature || mo.signature;
    if (v.occurrences) mo.occurrences = v.occurrences;
    if (v.active !== undefined) mo.active = v.active;
    mo.lastSeen = v.last_seen_s != null ? performance.now() - v.last_seen_s * 1000 : performance.now();
    if (flare) mo.flare = performance.now();
    this.motifs.set(mo.id, mo);
    return mo;
  };

  // Earlier interpretations from the history API (oldest first).
  State.prototype.loadNarratives = function (envs) {
    var known = {}, self = this;
    this.narratives.forEach(function (n) { known[n.text] = true; });
    var added = 0;
    (envs || []).forEach(function (env) {
      var p = env.payload || {};
      if (!p.text || known[p.text]) return;
      known[p.text] = true;
      self.narratives.push({ text: p.text, model: p.model, time: new Date(env.timestamp), old: true });
      added++;
    });
    if (!added) return;
    this.narratives.sort(function (a, b) { return b.time - a.time; });
    this.narratives.length = Math.min(this.narratives.length, 20);
    this.narrativeVersion++;
  };

  State.prototype.loadMotifs = function (list) {
    var self = this;
    (list || []).forEach(function (v) { self.upsertMotif(v, false); });
  };

  State.prototype.applySnapshot = function (snap) {
    var self = this;
    if (snap.memory) this.memory = mapPayloads(snap.memory);
    if (snap.narrative) this.apply(snap.narrative, true);
    (snap.active_motifs || []).forEach(function (v) { self.upsertMotif(v, false); });
    this.resonances.clear();
    ['frame', 'spectrum', 'features', 'system'].forEach(function (k) {
      if (snap[k]) self.apply(snap[k], true);
    });
    (snap.active_resonances || []).forEach(function (env) { self.apply(env, true); });
  };

  // Advance smoothing and entity fades; call once per rendered frame.
  State.prototype.step = function (dt) {
    for (var k in this.frame) this.frame[k].step(dt);
    var a = 1 - Math.exp(-dt / 0.18);
    for (var i = 0; i < BANDS; i++) this.bands[i] += (this.bandTarget[i] - this.bands[i]) * a;
    var self = this;
    this.resonances.forEach(function (r, id) {
      if (r.ending) {
        r.alpha -= dt / 3;
        if (r.alpha <= 0) self.resonances.delete(id);
      } else {
        r.alpha = Math.min(1, r.alpha + dt / 1.5);
      }
    });
  };

  // Frequency <-> 0..1 position on the shared log axis.
  State.prototype.u = function (hz) {
    var m = this.spectrumMeta;
    return Math.log(hz / m.min_hz) / Math.log(m.max_hz / m.min_hz);
  };

  function mapPayloads(m) {
    var out = {};
    for (var k in m) out[k] = m[k] && m[k].payload ? m[k].payload : m[k];
    return out;
  }

  // "motif-009" -> "motif 9"
  function motifName(id) {
    var n = /(\d+)$/.exec(id || '');
    return n ? 'motif ' + parseInt(n[1], 10) : id;
  }
  // A stable, soft colour per motif from its seed.
  function motifHue(seed) { return (seed || 0) % 360; }

  LO.motifName = motifName;
  LO.motifHue = motifHue;
  LO.BANDS = BANDS;
  LO.State = State;

  // Times are shown in the source's local time zone (data-tz on the page).
  var root = document.querySelector('.observatory');
  var tz = (root && root.dataset.tz) || 'UTC';
  try { new Intl.DateTimeFormat('en', { timeZone: tz }); } catch (e) { tz = 'UTC'; }
  var clock = new Intl.DateTimeFormat('en-CA', {
    timeZone: tz, year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false, timeZoneName: 'short'
  });
  var clockShort = new Intl.DateTimeFormat('en-GB', {
    timeZone: tz, hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false
  });
  LO.fmt = {
    local: function (d) {
      var parts = {};
      clock.formatToParts(d).forEach(function (p) { parts[p.type] = p.value; });
      return parts.year + '-' + parts.month + '-' + parts.day + '  ' + parts.hour + ':' + parts.minute + ':' + parts.second + ' (' + parts.timeZoneName + ')';
    },
    localShort: function (d) { return clockShort.format(d); },
    hz: function (hz) {
      if (hz == null || !isFinite(hz)) return '—';
      return hz >= 1000 ? (hz / 1000).toFixed(2) + ' kHz' : hz.toFixed(1) + ' Hz';
    },
    num: function (x, d) { return x == null || !isFinite(x) ? '—' : x.toFixed(d == null ? 2 : d); },
    state: function (s) { return s ? s.replace(/_/g, ' ') : '—'; }
  };
})(window.LO = window.LO || {});
