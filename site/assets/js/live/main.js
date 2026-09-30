// Wires the stream, state, field and panels together.
(function (LO) {
  'use strict';

  var root = document.querySelector('.observatory');
  if (!root || typeof p5 === 'undefined') return;

  var reduced = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  var state = new LO.State();
  var panels = new LO.Panels(state);

  LO.field.create(document.getElementById('terrain'), state, { reducedMotion: reduced });

  state.onSpectrum(function (p) { panels.spectrum(p); });
  state.onEvent(function (env, replay) { if (!replay) panels.event(env); });

  LO.stream.connect(root.dataset.ws, {
    onEvent: function (env) {
      state.apply(env, false);
      if (env.type === 'system.status') panels.connection(state.connection, state.stream);
    },
    onSnapshot: function (snap) {
      state.applySnapshot(snap);
      panels.replayRecent(snap.recent_events);
      panels.connection(state.connection, state.stream);
    },
    onConnection: function (s) {
      state.connection = s === 'connecting' || s === 'reconnecting' || s === 'offline' ? s : 'connected';
      panels.connection(state.connection, state.stream);
    }
  });

  // The socket's onopen isn't surfaced as a state; the first message is.
  var markConnected = function () {
    if (state.connection !== 'connected') {
      state.connection = 'connected';
      panels.connection(state.connection, state.stream);
    }
  };
  state.onSpectrum(markConnected);

  setInterval(function () { panels.features(); }, 250);
  setInterval(function () { panels.drawHarmonicMap(); }, 1000);
})(window.LO = window.LO || {});
