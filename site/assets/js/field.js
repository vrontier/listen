// Ambient background: slow concentric "listening" rings and a drifting spectrum line.
// Purely decorative placeholder until the live visualization exists. No audio is used.
(function () {
  'use strict';

  var canvas = document.getElementById('field');
  if (!canvas || !canvas.getContext) return;
  var ctx = canvas.getContext('2d');
  var reduced = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  var w = 0, h = 0, dpr = 1;

  function resize() {
    dpr = Math.min(window.devicePixelRatio || 1, 2);
    w = canvas.clientWidth;
    h = canvas.clientHeight;
    canvas.width = Math.round(w * dpr);
    canvas.height = Math.round(h * dpr);
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  }

  function draw(t) {
    var s = t / 1000;
    ctx.clearRect(0, 0, w, h);

    // Rings expanding from an off-centre point, fading as they grow.
    var cx = w * 0.78, cy = h * 0.22;
    var maxR = Math.hypot(w, h) * 0.6;
    for (var i = 0; i < 7; i++) {
      var r = ((s * 18 + i * (maxR / 7)) % maxR);
      var a = 0.2 * (1 - r / maxR);
      ctx.beginPath();
      ctx.arc(cx, cy, r, 0, Math.PI * 2);
      ctx.strokeStyle = 'rgba(143, 211, 200,' + a.toFixed(3) + ')';
      ctx.lineWidth = 2.5;
      ctx.stroke();
    }

    // A faint spectrum-like contour along the lower third.
    var base = h * 0.82;
    ctx.beginPath();
    for (var x = 0; x <= w; x += 4) {
      var k = x / w;
      var y = base
        - Math.sin(k * 11 + s * 0.6) * 14 * (1 - k * 0.6)
        - Math.sin(k * 37 - s * 1.1) * 6 * (1 - k)
        - Math.sin(k * 3 + s * 0.2) * 22;
      if (x === 0) ctx.moveTo(x, y); else ctx.lineTo(x, y);
    }
    ctx.strokeStyle = 'rgba(228, 228, 226, 0.08)';
    ctx.lineWidth = 1;
    ctx.stroke();

    if (!reduced) requestAnimationFrame(draw);
  }

  resize();
  window.addEventListener('resize', function () { resize(); if (reduced) draw(0); });
  requestAnimationFrame(draw);
})();
