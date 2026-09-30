// WebSocket client for the listener's /ws/live stream (event model §3, §25).
// Reconnects with backoff and loads /api/state/current after every
// (re)connect so the page can rebuild its state.
(function (LO) {
  'use strict';

  // The WebSocket URL comes from ?ws=, the page (data-ws, e.g.
  // "/my-stream/ws/live") or defaults to /ws/live on this host. The
  // snapshot, API and audio endpoints sit next to it under the same prefix.
  function endpoints(configured) {
    var q = new URLSearchParams(location.search).get('ws');
    var u = new URL(q || configured || '/ws/live', location.href);
    if (u.protocol === 'http:') u.protocol = 'ws:';
    if (u.protocol === 'https:') u.protocol = 'wss:';
    var prefix = u.pathname.replace(/\/ws\/live\/?$/, '');
    var http = (u.protocol === 'wss:' ? 'https:' : 'http:') + '//' + u.host + prefix;
    return {
      ws: u.href,
      snapshot: http + '/api/state/current',
      api: http + '/api',
      audio: u.protocol + '//' + u.host + prefix + '/ws/audio'
    };
  }

  // handlers: onEvent(env), onSnapshot(snap), onConnection(state)
  function connect(configured, handlers) {
    var ep = endpoints(configured);
    var backoff = 1000;
    var lastSeq = 0;
    var lastMessage = 0;
    var socket = null;
    var stats = { dropped: 0, received: 0 };

    function state(s) { handlers.onConnection(s); }

    function open() {
      state(lastMessage ? 'reconnecting' : 'connecting');
      try {
        socket = new WebSocket(ep.ws);
      } catch (e) {
        retry();
        return;
      }
      socket.onopen = function () {
        backoff = 1000;
        lastSeq = 0;
        fetch(ep.snapshot, { cache: 'no-store' })
          .then(function (r) { return r.ok ? r.json() : null; })
          .then(function (snap) { if (snap) handlers.onSnapshot(snap); })
          .catch(function () { /* the live stream alone is enough */ });
      };
      socket.onmessage = function (m) {
        var env;
        try { env = JSON.parse(m.data); } catch (e) { return; }
        lastMessage = performance.now();
        stats.received++;
        if (lastSeq && env.sequence > lastSeq + 1) stats.dropped += env.sequence - lastSeq - 1;
        if (env.sequence > lastSeq) lastSeq = env.sequence;
        handlers.onEvent(env);
      };
      socket.onclose = function () { retry(); };
      socket.onerror = function () { /* onclose follows */ };
    }

    function retry() {
      socket = null;
      state(lastMessage ? 'reconnecting' : 'offline');
      setTimeout(open, backoff);
      backoff = Math.min(backoff * 2, 15000);
    }

    // A silent socket (e.g. a stalled proxy) is treated as lost.
    setInterval(function () {
      if (socket && socket.readyState === 1 && lastMessage && performance.now() - lastMessage > 12000) {
        socket.close();
      }
    }, 3000);

    open();
    return { stats: stats, audio: ep.audio, api: ep.api };
  }

  LO.stream = { connect: connect, endpoints: endpoints };
})(window.LO = window.LO || {});
