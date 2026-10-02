// Wires the stream, state, field, panels and (optional) audio together.
(function (LO) {
  'use strict';

  var root = document.querySelector('.observatory');
  if (!root || typeof p5 === 'undefined') return;

  var reduced = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  var state = new LO.State();
  var panels = new LO.Panels(state);

  var terrainEl = document.getElementById('terrain');
  var field = LO.field.create(terrainEl, state, { reducedMotion: reduced });
  if (window.ResizeObserver) {
    var pending = false;
    new ResizeObserver(function () {
      if (pending) return;
      pending = true;
      requestAnimationFrame(function () {
        pending = false;
        if (field && field.windowResized) field.windowResized();
      });
    }).observe(terrainEl);
  }

  state.onSpectrum(function (p) { panels.spectrum(p); });
  state.onEvent(function (env, replay) { if (!replay) panels.event(env); });

  function markConnected() {
    if (state.connection !== 'connected') {
      state.connection = 'connected';
      panels.connection(state.connection, state.stream);
    }
  }
  state.onSpectrum(markConnected);

  var lastDelivered = null;
  function deliver(env) {
    if (typeof env.position_s === 'number') lastDelivered = env.position_s;
    state.apply(env, false);
    if (env.type === 'system.status') {
      panels.connection(state.connection, state.stream);
      updateListen();
    }
  }

  // While audio plays, events wait until the playhead reaches their
  // position, so what is seen is what is heard. Otherwise they apply at once.
  var player = null;
  var pending = [];

  function schedule(env) {
    if (!player || player.playhead() == null || typeof env.position_s !== 'number') {
      deliver(env);
      return;
    }
    var i = pending.length;
    while (i > 0 && pending[i - 1].position_s > env.position_s) i--;
    pending.splice(i, 0, env);
    if (pending.length > 4000) deliver(pending.shift());
  }

  setInterval(function () {
    var ph = player ? player.playhead() : null;
    if (ph == null) {
      while (pending.length) deliver(pending.shift());
      return;
    }
    while (pending.length && pending[0].position_s <= ph) deliver(pending.shift());
  }, 20);

  var endpoints = LO.stream.connect(root.dataset.ws, {
    onEvent: schedule,
    onSnapshot: function (snap) {
      state.applySnapshot(snap);
      fetch(endpoints.api + '/history/events?type=narrative.update', { cache: 'no-store' })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (j) { if (j) state.loadNarratives(j.events); })
        .catch(function () { /* no memory on this listener */ });
      fetch(endpoints.api + '/motifs', { cache: 'no-store' })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (j) { if (j) state.loadMotifs(j.motifs); })
        .catch(function () { /* no memory on this listener */ });
      panels.replayRecent(snap.recent_events);
      panels.connection(state.connection, state.stream);
      updateListen();
    },
    onConnection: function (s) {
      state.connection = s === 'connecting' || s === 'reconnecting' || s === 'offline' ? s : 'connected';
      panels.connection(state.connection, state.stream);
    }
  });

  // Sound switch and volume knob: rendered only when the site allows audio
  // (data-audio), shown only when the listener relays a codec this browser
  // can play. The knob's setting is kept per browser.
  var button = document.getElementById('listen');
  var strip = document.getElementById('strip');
  var statusEl = document.getElementById('listen-status');
  var KEY = 'lo.volume';
  var saved = null;
  try { saved = parseFloat(localStorage.getItem(KEY)); } catch (e) { /* storage off */ }
  var knob = LO.knob.create(document.getElementById('volume') || document.createElement('div'), {
    value: isFinite(saved) ? saved : LO.knob.UNITY,
    onchange: function (gain, p) {
      if (player) player.setGain(gain);
      try { localStorage.setItem(KEY, String(p)); } catch (e) { /* storage off */ }
    }
  });
  function offered() { return (state.stream && state.stream.audio) || []; }
  function updateListen() {
    if (!button) return;
    if (!player) {
      player = new LO.AudioPlayer(endpoints.audio);
      player.onchange = updateListen;
      player.setGain(knob.gain());
    }
    strip.hidden = !player.supported(offered());
    if (strip.hidden && player.active) player.stop();
    var on = player.active;
    button.setAttribute('aria-checked', on ? 'true' : 'false');
    button.dataset.state = !on ? 'off' : player.started ? 'on' : 'buffering';
    statusEl.textContent = !on ? '' : player.started ? 'listening · ' + player.codec.name : 'buffering…';
  }
  if (button && root.dataset.audio) {
    button.addEventListener('click', function () {
      if (player.active) player.stop(); else player.start(offered());
    });
  }

  // Motif export: the remembered motifs of this source as CSV, fetched fresh
  // when the button is pressed. Sorted by frequency.
  var exportBtn = document.getElementById('motif-export');
  if (exportBtn) {
    exportBtn.addEventListener('click', function () {
      exportBtn.disabled = true;
      fetch(endpoints.api + '/motifs', { cache: 'no-store' })
        .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
        .then(function (data) { download(motifCSV(data.motifs || [])); })
        .catch(function () { exportBtn.textContent = 'Export failed'; setTimeout(function () { exportBtn.textContent = '⤓ Export CSV'; }, 3000); })
        .then(function () { exportBtn.disabled = false; });
    });
  }
  function motifCSV(list) {
    var cols = ['source', 'motif_id', 'name', 'kind', 'frequency_hz', 'occurrences', 'presence_s', 'confidence',
                'harmonicity', 'entropy', 'stability', 'active', 'created', 'last_seen'];
    var esc = function (v) {
      v = v == null ? '' : String(v);
      return /[",\n]/.test(v) ? '"' + v.replace(/"/g, '""') + '"' : v;
    };
    var rows = list.map(function (m) {
      var sig = m.signature || {}, vis = m.visual || {};
      var hz = sig.fundamental_hz || sig.centroid_hz || vis.frequency_anchor || '';
      return { hz: +hz || 0, cells: [root.dataset.source, m.motif_id, LO.motifName(m.motif_id), m.kind, hz, m.occurrences,
        m.presence_s, m.confidence, sig.harmonicity, sig.entropy, vis.stability, m.active, m.created, m.last_seen] };
    }).sort(function (a, b) { return a.hz - b.hz; });
    return '\ufeff' + [cols].concat(rows.map(function (r) { return r.cells; }))
      .map(function (r) { return r.map(esc).join(','); }).join('\r\n') + '\r\n';
  }
  function download(csv) {
    var d = new Date(), pad = function (n) { return (n < 10 ? '0' : '') + n; };
    var name = root.dataset.source + '-motifs-' + d.getFullYear() + pad(d.getMonth() + 1) + pad(d.getDate()) +
      '-' + pad(d.getHours()) + pad(d.getMinutes()) + '.csv';
    var url = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8' }));
    var a = document.createElement('a');
    a.href = url; a.download = name;
    document.body.appendChild(a); a.click(); a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
  }

  // Read-only diagnostics for the console: audio/visual sync.
  LO.live = {
    playhead: function () { return player ? player.playhead() : null; },
    lastDelivered: function () { return lastDelivered; },
    pending: function () { return pending.length; },
    codec: function () { return player && player.codec ? player.codec.name : null; },
    element: function () { return player ? player.el : null; },
    motifs: function () {
      var now = performance.now(), out = [];
      state.motifs.forEach(function (m) { out.push({ id: m.id, kind: m.kind, flareAgo: m.flare ? Math.round(now - m.flare) : null, occ: m.occurrences, active: m.active }); });
      return out;
    }
  };

  // The memory dial; hover (or tap) a mark for its details.
  var dialEl = document.getElementById('dial');
  var dial = LO.dial.create(dialEl, state);
  var dialBox = document.querySelector('.dial');
  var tip = document.getElementById('motif-tip');
  function ago(ms) {
    var s = ms / 1000;
    if (s < 60) return 'just now';
    if (s < 5400) return Math.round(s / 60) + ' min ago';
    return (s / 3600).toFixed(1) + ' h ago';
  }
  function showTip(ev) {
    var c = dialEl.getBoundingClientRect(), box = dialBox.getBoundingClientRect();
    var m = dial.markAt(ev.clientX - c.left, ev.clientY - c.top);
    dial.setHover(m ? m.mo.id : null);
    dialEl.style.cursor = m ? 'pointer' : '';
    if (!m) { tip.hidden = true; return; }
    var mo = m.mo, v = mo.visual || {}, sig = mo.signature || {};
    var hz = mo.kind === 'texture' ? 'texture around ' + LO.fmt.hz(sig.centroid_hz || v.frequency_anchor)
      : LO.fmt.hz(sig.fundamental_hz || v.frequency_anchor);
    var when = mo.active ? 'sounding now' : 'heard ' + ago(performance.now() - (mo.lastSeen || performance.now()));
    var name = LO.motifName(mo.id);
    tip.innerHTML = '';
    var b = document.createElement('b'); b.textContent = name.charAt(0).toUpperCase() + name.slice(1) + ' · ' + hz;
    var sp = document.createElement('span'); sp.textContent = ' · ' + (mo.occurrences || 1) + '× · ' + when;
    tip.appendChild(b); tip.appendChild(sp);
    tip.hidden = false;
    var x = c.left - box.left + m.x;
    tip.style.left = Math.max(8, Math.min(x - tip.offsetWidth / 2, box.width - tip.offsetWidth - 8)) + 'px';
    tip.style.top = (c.top - box.top + m.top - tip.offsetHeight - 8) + 'px';
  }
  dialEl.addEventListener('pointermove', showTip);
  dialEl.addEventListener('pointerdown', showTip);
  dialEl.addEventListener('pointerleave', function () { tip.hidden = true; dial.setHover(null); });

  setInterval(function () { panels.features(); panels.interpretation(); }, 250);
  setInterval(function () { panels.drawHarmonicMap(); }, 1000);
})(window.LO = window.LO || {});
