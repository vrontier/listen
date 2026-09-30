// Landing page: live status on each stream card, from the stream's snapshot.
(function () {
  'use strict';

  var words = function (s) { return (s || '').replace(/_/g, ' '); };
  var hz = function (v) { return v >= 1000 ? (v / 1000).toFixed(2) + ' kHz' : Math.round(v) + ' Hz'; };

  function check(card) {
    var status = card.querySelector('.stream__status'), text = card.querySelector('.stream__text');
    fetch(card.dataset.api + '/state/current', { cache: 'no-store' })
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
      })
      .catch(function () {
        status.dataset.state = 'offline';
        text.textContent = 'offline';
      });
  }

  var cards = document.querySelectorAll('.stream[data-api]');
  Array.prototype.forEach.call(cards, check);
  setInterval(function () { Array.prototype.forEach.call(cards, check); }, 15000);
})();
