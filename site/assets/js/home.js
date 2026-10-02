// Landing page: live status on each stream card, from the stream's snapshot,
// and how many people are watching (live page open) and listening (audio on).
(function () {
  'use strict';

  var words = function (s) { return (s || '').replace(/_/g, ' '); };
  var hz = function (v) { return v >= 1000 ? (v / 1000).toFixed(2) + ' kHz' : Math.round(v) + ' Hz'; };

  function audience(watching, listening) {
    if (!watching) return '';
    return watching + ' watching' + (listening ? ' · ' + listening + ' listening' : '');
  }

  // Resolves to {up, watching, listening} for the total line.
  function check(card) {
    var status = card.querySelector('.stream__status'), text = card.querySelector('.stream__text');
    var aud = card.querySelector('.stream__audience');
    return fetch(card.dataset.api + '/state/current', { cache: 'no-store' })
      .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
      .then(function (s) {
        var sys = s.system && s.system.payload, f = s.features && s.features.payload;
        var up = sys && sys.stream === 'connected';
        var parts = [up ? (sys.input && sys.input.indexOf('file:') === 0 ? 'replay' : 'live') : (sys ? sys.stream : 'offline')];
        if (up && f) {
          parts.push(words(f.state));
          if (f.dominant_frequency_hz) parts.push(hz(f.dominant_frequency_hz));
        }
        var motifs = (s.active_motifs || []).length;
        if (up && motifs) parts.push(motifs + ' motif' + (motifs > 1 ? 's' : '') + ' sounding');
        status.dataset.state = up ? 'connected' : 'offline';
        text.textContent = parts.join(' · ');
        var watching = (sys && sys.listeners) || 0, listening = (sys && sys.audio_listeners) || 0;
        aud.textContent = audience(watching, listening);
        return { up: up, watching: watching, listening: listening };
      })
      .catch(function () {
        status.dataset.state = 'offline';
        text.textContent = 'offline';
        aud.textContent = '';
        return { up: false, watching: 0, listening: 0 };
      });
  }

  var cards = Array.prototype.slice.call(document.querySelectorAll('.stream[data-api]'));
  var now = document.getElementById('streams-now');
  function refresh() {
    Promise.all(cards.map(check)).then(function (rs) {
      var live = 0, watching = 0, listening = 0, busy = 0;
      rs.forEach(function (r) {
        if (r.up) live++;
        watching += r.watching; listening += r.listening;
        if (r.watching) busy++;
      });
      if (!now) return;
      now.hidden = false;
      now.textContent = watching
        ? 'Right now: ' + watching + (watching === 1 ? ' person' : ' people') + ' watching' +
          (listening ? ', ' + listening + ' of them listening' : '') +
          ', across ' + busy + (busy === 1 ? ' stream' : ' streams') + ' · ' + live + ' of ' + rs.length + ' streams live'
        : live + ' of ' + rs.length + ' streams live · nobody watching right now; be the first';
    });
  }
  refresh();
  setInterval(refresh, 15000);
})();
