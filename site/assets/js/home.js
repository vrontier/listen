// Landing page: live status on each stream card, from the stream's snapshot,
// and how many stream listeners there are (live page open), and how many of
// them with sound on.
(function () {
  'use strict';

  var words = function (s) { return (s || '').replace(/_/g, ' '); };
  var hz = function (v) { return v >= 1000 ? (v / 1000).toFixed(2) + ' kHz' : Math.round(v) + ' Hz'; };

  function audience(watching, listening) {
    if (!watching) return '';
    return watching + (watching === 1 ? ' listener' : ' listeners') + (listening ? ' · ' + listening + ' with sound' : '');
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
        var idle = sys && sys.stream === 'idle';  // on-demand node: connects when the page opens
        var parts = [up ? (sys.input && sys.input.indexOf('file:') === 0 ? 'replay' : 'live') : idle ? 'idle · opens when you visit' : (sys ? sys.stream : 'offline')];
        if (up && f) {
          parts.push(words(f.state));
          if (f.dominant_frequency_hz) parts.push(hz(f.dominant_frequency_hz));
        }
        var motifs = (s.active_motifs || []).length;
        if (up && motifs) parts.push(motifs + ' motif' + (motifs > 1 ? 's' : '') + ' sounding');
        status.dataset.state = up ? 'connected' : idle ? 'idle' : 'offline';
        text.textContent = parts.join(' · ');
        var watching = (sys && sys.listeners) || 0, listening = (sys && sys.audio_listeners) || 0;
        aud.textContent = audience(watching, listening);
        return { up: up, idle: idle, watching: watching, listening: listening };
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
      var live = 0, idle = 0, watching = 0, listening = 0, busy = 0;
      rs.forEach(function (r) {
        if (r.up) live++;
        if (r.idle) idle++;
        watching += r.watching; listening += r.listening;
        if (r.watching) busy++;
      });
      if (!now) return;
      now.hidden = false;
      var avail = live + ' of ' + rs.length + ' streams live' + (idle ? ' · ' + idle + ' on demand' : '');
      now.textContent = watching
        ? 'Right now: ' + watching + (watching === 1 ? ' stream listener' : ' stream listeners') +
          (listening ? ' (' + listening + ' with sound)' : '') +
          ' across ' + busy + (busy === 1 ? ' stream' : ' streams') + ' · ' + avail
        : avail + ' · no stream listeners right now; be the first';
    });
  }
  refresh();
  setInterval(refresh, 15000);
})();
