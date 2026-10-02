// A studio-style rotary volume knob: a 0–10 scale over 270°, a warm mark at
// unity gain (0 dB), and a cap with an indicator line. Turned by dragging up
// or down, the mouse wheel or the arrow keys; double-click returns to unity.
// role="slider", so screen readers announce the gain in dB.
(function (LO) {
  'use strict';

  var MIN_DB = -40, MAX_DB = 24;          // position 0 is silence
  var UNITY = -MIN_DB / (MAX_DB - MIN_DB); // 0.625: 0 dB
  var SWEEP = 270, START = -135;           // degrees, 0 = up
  var NS = 'http://www.w3.org/2000/svg';

  function dbOf(p) { return p <= 0 ? -Infinity : MIN_DB + p * (MAX_DB - MIN_DB); }
  function gainOf(p) { var db = dbOf(p); return db === -Infinity ? 0 : Math.pow(10, db / 20); }
  function label(p) {
    var db = dbOf(p);
    if (db === -Infinity) return 'off';
    var r = Math.round(db);
    return (r > 0 ? '+' : r < 0 ? '−' : '') + Math.abs(r) + ' dB';
  }

  function el(name, attrs, parent) {
    var n = document.createElementNS(NS, name);
    for (var k in attrs) n.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(n);
    return n;
  }
  function polar(deg, r) {
    var a = (deg - 90) * Math.PI / 180;
    return [32 + r * Math.cos(a), 32 + r * Math.sin(a)];
  }

  function create(root, opts) {
    var p = opts.value != null ? opts.value : UNITY;
    var valueEl = root.querySelector('.knob__value');
    var svg = el('svg', { viewBox: '0 0 64 64', 'aria-hidden': 'true', focusable: 'false' });
    root.insertBefore(svg, root.firstChild);

    // Scale: 11 numbered ticks with minor ticks between, and the unity mark.
    for (var i = 0; i <= 20; i++) {
      var deg = START + SWEEP * i / 20, major = i % 2 === 0;
      var a = polar(deg, major ? 30 : 29.5), b = polar(deg, 27);
      el('line', { x1: a[0], y1: a[1], x2: b[0], y2: b[1], class: major ? 'knob__tick knob__tick--major' : 'knob__tick' }, svg);
    }
    var u = polar(START + SWEEP * UNITY, 31.2);
    el('circle', { cx: u[0], cy: u[1], r: 1.4, class: 'knob__unity' }, svg);
    // Arc from zero to the current setting.
    var arc = el('path', { class: 'knob__arc' }, svg);
    // Cap: a ridged outer ring and a smooth top with the indicator.
    el('circle', { cx: 32, cy: 32, r: 22, class: 'knob__skirt' }, svg);
    el('circle', { cx: 32, cy: 32, r: 17, class: 'knob__cap' }, svg);
    var cap = el('g', {}, svg);
    el('line', { x1: 32, y1: 18, x2: 32, y2: 26, class: 'knob__pointer' }, cap);

    function arcPath(to) {
      if (to <= 0) return '';
      var r = 25, a0 = polar(START, r), a1 = polar(START + SWEEP * to, r);
      var large = SWEEP * to > 180 ? 1 : 0;
      return 'M' + a0[0] + ' ' + a0[1] + ' A' + r + ' ' + r + ' 0 ' + large + ' 1 ' + a1[0] + ' ' + a1[1];
    }

    function render() {
      cap.setAttribute('transform', 'rotate(' + (START + SWEEP * p) + ' 32 32)');
      arc.setAttribute('d', arcPath(p));
      var t = label(p);
      if (valueEl) valueEl.textContent = t;
      root.setAttribute('aria-valuenow', String(Math.round(p * 100)));
      root.setAttribute('aria-valuetext', t === 'off' ? 'silent' : t);
    }
    function set(v, quiet) {
      v = Math.max(0, Math.min(1, v));
      // Snap to unity when close, like a detented studio pot.
      if (Math.abs(v - UNITY) < 0.012) v = UNITY;
      if (v === p && quiet) return;
      p = v;
      render();
      if (!quiet && opts.onchange) opts.onchange(gainOf(p), p);
    }

    // Drag: up/right turns up; 160 px for the full sweep, finer with Shift.
    var drag = null;
    root.addEventListener('pointerdown', function (e) {
      drag = { y: e.clientY, x: e.clientX, p: p };
      root.setPointerCapture(e.pointerId);
      root.focus();
      e.preventDefault();
    });
    root.addEventListener('pointermove', function (e) {
      if (!drag) return;
      var d = (drag.y - e.clientY) + (e.clientX - drag.x) * 0.5;
      set(drag.p + d / (e.shiftKey ? 640 : 160));
    });
    function end() { drag = null; }
    root.addEventListener('pointerup', end);
    root.addEventListener('pointercancel', end);
    root.addEventListener('wheel', function (e) {
      e.preventDefault();
      set(p + (e.deltaY < 0 ? 1 : -1) * (e.shiftKey ? 0.005 : 0.02));
    }, { passive: false });
    root.addEventListener('dblclick', function () { set(UNITY); });
    root.addEventListener('keydown', function (e) {
      var step = { ArrowUp: 0.02, ArrowRight: 0.02, ArrowDown: -0.02, ArrowLeft: -0.02, PageUp: 0.1, PageDown: -0.1 }[e.key];
      if (step != null) { set(p + step); e.preventDefault(); }
      else if (e.key === 'Home') { set(0); e.preventDefault(); }
      else if (e.key === 'End') { set(1); e.preventDefault(); }
    });

    render();
    return { gain: function () { return gainOf(p); }, value: function () { return p; }, set: set };
  }

  LO.knob = { create: create, UNITY: UNITY };
})(window.LO = window.LO || {});
