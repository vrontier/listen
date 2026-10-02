// A studio jog wheel for a snapped scroll list: a ridged cylinder seen edge
// on, drawn on a small canvas. Rolling it (drag up/down, mouse wheel, arrow
// keys) steps one item at a time; the ridges follow the list's scroll
// position, so wheel and text always move together, whatever moved them.
(function (LO) {
  'use strict';

  var NOTCH = 22;        // degrees the wheel turns per item
  var RIDGE = 11;        // degrees between ridges
  var PX_PER_ITEM = 18;  // drag distance for one step

  function create(canvas, scroller, list, onstep) {
    var ctx = canvas.getContext('2d');
    var W = 0, H = 0, dpr = 1, residual = 0, raf = 0;

    function size() {
      dpr = Math.min(window.devicePixelRatio || 1, 2);
      W = canvas.clientWidth; H = canvas.clientHeight;
      canvas.width = Math.round(W * dpr); canvas.height = Math.round(H * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      draw();
    }

    function itemHeight() {
      var li = list.firstElementChild;
      return li ? li.offsetHeight || 1 : 1;
    }
    function index() { return Math.round(scroller.scrollTop / itemHeight()); }
    function count() { return list.children.length; }

    // While a smooth scroll is under way, quick steps add up from its target
    // rather than from the position it has reached so far.
    var target = null, settle = 0;
    function go(i) {
      i = Math.max(0, Math.min(count() - 1, i));
      var li = list.children[i];
      if (!li) return;
      target = i;
      clearTimeout(settle);
      settle = setTimeout(function () { target = null; }, 700);
      scroller.scrollTo({ top: li.offsetTop - list.offsetTop, behavior: 'smooth' });
      if (onstep) onstep(i);
    }
    function step(d) { go((target != null ? target : index()) + d); }

    function draw() {
      raf = 0;
      if (!W || !H) return;
      var cx = W / 2, R = H / 2 - 2, cy = H / 2;
      var phase = (scroller.scrollTop / itemHeight()) * NOTCH + residual * (NOTCH / PX_PER_ITEM);
      ctx.clearRect(0, 0, W, H);

      // Housing slot.
      ctx.fillStyle = '#060608';
      roundRect(1, 0, W - 2, H, 4); ctx.fill();
      ctx.strokeStyle = 'rgba(228,228,226,0.16)'; ctx.lineWidth = 1;
      roundRect(1.5, 0.5, W - 3, H - 1, 4); ctx.stroke();

      // Cylinder body: light in the middle, dark towards the top and bottom.
      var g = ctx.createLinearGradient(0, cy - R, 0, cy + R);
      g.addColorStop(0, '#0d0e10');
      g.addColorStop(0.5, '#3a3c41');
      g.addColorStop(1, '#0d0e10');
      ctx.fillStyle = g;
      ctx.fillRect(4, cy - R, W - 8, 2 * R);

      // Ridges at fixed angles on the cylinder, projected: y = R·sin(θ),
      // brighter and thicker where they face the viewer.
      var first = Math.ceil((-88 - phase) / RIDGE);
      for (var k = first; ; k++) {
        var th = phase + k * RIDGE;
        if (th > 88) break;
        var rad = th * Math.PI / 180, face = Math.cos(rad);
        var y = cy + R * Math.sin(rad);
        ctx.strokeStyle = 'rgba(236,235,231,' + (0.12 + 0.55 * face * face).toFixed(3) + ')';
        ctx.lineWidth = 0.6 + 1.1 * face;
        ctx.beginPath(); ctx.moveTo(5, y); ctx.lineTo(W - 5, y); ctx.stroke();
        ctx.strokeStyle = 'rgba(0,0,0,' + (0.5 * face).toFixed(3) + ')';
        ctx.lineWidth = 0.8;
        ctx.beginPath(); ctx.moveTo(5, y + 1.2); ctx.lineTo(W - 5, y + 1.2); ctx.stroke();
      }
      // Index marks on the housing at the wheel's centre line.
      ctx.fillStyle = '#f07854';
      ctx.fillRect(0, cy - 0.75, 2.5, 1.5);
      ctx.fillRect(W - 2.5, cy - 0.75, 2.5, 1.5);
    }
    function roundRect(x, y, w, h, r) {
      ctx.beginPath();
      ctx.moveTo(x + r, y); ctx.arcTo(x + w, y, x + w, y + h, r); ctx.arcTo(x + w, y + h, x, y + h, r);
      ctx.arcTo(x, y + h, x, y, r); ctx.arcTo(x, y, x + w, y, r); ctx.closePath();
    }
    function redraw() {
      // Hidden at first (one item): measure again once it is shown.
      if (canvas.clientWidth !== W || canvas.clientHeight !== H) { size(); return; }
      if (!raf) raf = requestAnimationFrame(draw);
    }

    // Drag: down rolls on to earlier items, like a thumb on a jog wheel.
    var drag = null;
    canvas.addEventListener('pointerdown', function (e) {
      drag = { y: e.clientY };
      canvas.setPointerCapture(e.pointerId);
      canvas.focus();
      e.preventDefault();
    });
    canvas.addEventListener('pointermove', function (e) {
      if (!drag) return;
      residual += e.clientY - drag.y;
      drag.y = e.clientY;
      while (residual >= PX_PER_ITEM) { residual -= PX_PER_ITEM; step(1); }
      while (residual <= -PX_PER_ITEM) { residual += PX_PER_ITEM; step(-1); }
      redraw();
    });
    function end() { drag = null; residual = 0; redraw(); }
    canvas.addEventListener('pointerup', end);
    canvas.addEventListener('pointercancel', end);
    var wheelAcc = 0;
    canvas.addEventListener('wheel', function (e) {
      e.preventDefault();
      wheelAcc += e.deltaY;
      if (Math.abs(wheelAcc) >= 40) { step(wheelAcc > 0 ? 1 : -1); wheelAcc = 0; }
    }, { passive: false });
    canvas.addEventListener('keydown', function (e) {
      var d = { ArrowDown: 1, ArrowUp: -1, PageDown: 5, PageUp: -5 }[e.key];
      if (d) { step(d); e.preventDefault(); }
      else if (e.key === 'Home') { go(0); e.preventDefault(); }
      else if (e.key === 'End') { go(count() - 1); e.preventDefault(); }
    });

    scroller.addEventListener('scroll', redraw, { passive: true });
    window.addEventListener('resize', size);
    size();
    return { redraw: redraw, index: index };
  }

  LO.jog = { create: create };
})(window.LO = window.LO || {});
