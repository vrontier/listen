// In-sync playback of the audio relayed by the listener (/ws/audio).
//
// The relay sends fragmented-MP4 segments. Each run of ffmpeg starts at
// media time 0; its header carries the offset of that run on the listener's
// audio timeline, which becomes SourceBuffer.timestampOffset. The element's
// currentTime therefore *is* the audio position, the same axis as the
// events' position_s, and main.js holds events back until the playhead
// reaches them.
(function (LO) {
  'use strict';

  var MS = window.ManagedMediaSource || window.MediaSource;
  var TARGET = 1.2;   // seconds behind the newest audio to play at
  var CODECS = [
    { name: 'aac', mime: 'audio/mp4; codecs="mp4a.40.2"' },  // Safari, Chrome
    { name: 'opus', mime: 'audio/mp4; codecs="opus"' }       // Chromium, Firefox
  ];

  function pickCodec(offered) {
    if (!MS) return null;
    for (var i = 0; i < CODECS.length; i++) {
      var c = CODECS[i];
      if (offered.indexOf(c.name) >= 0 && MS.isTypeSupported(c.mime)) return c;
    }
    return null;
  }

  function Player(url) {
    this.url = url;           // ws(s)://…/ws/audio
    this.active = false;
    this.started = false;
    this.onchange = function () {};
  }

  Player.prototype.supported = function (offered) { return !!pickCodec(offered || []); };

  // Must be called from a user gesture (autoplay rules).
  Player.prototype.start = function (offered) {
    var self = this;
    var codec = pickCodec(offered);
    if (!codec || this.active) return;
    this.codec = codec;
    this.active = true;
    this.started = false;
    this.session = null;
    this.queue = [];
    this.backoff = 1000;

    var el = this.el = document.createElement('audio');
    el.preload = 'auto';
    var ms = this.ms = new MS();
    if (window.ManagedMediaSource && MS === window.ManagedMediaSource) {
      el.disableRemotePlayback = true;  // required for ManagedMediaSource
      try { el.srcObject = ms; } catch (e) { el.src = URL.createObjectURL(ms); }
    } else {
      el.src = this.objectURL = URL.createObjectURL(ms);
    }
    ms.addEventListener('sourceopen', function () {
      if (!self.active || self.sb) return;
      self.sb = ms.addSourceBuffer(codec.mime);
      self.sb.mode = 'segments';
      self.sb.addEventListener('updateend', function () { self.afterAppend(); self.pump(); });
      self.connect();
    });
    var p = el.play();
    if (p && p.catch) p.catch(function () { /* resumes once data is buffered */ });
    this.timer = setInterval(function () { self.maintain(); }, 1000);
    this.onchange();
  };

  Player.prototype.connect = function () {
    var self = this;
    if (!this.active) return;
    var ws = this.ws = new WebSocket(this.url + (this.url.indexOf('?') < 0 ? '?' : '&') + 'codec=' + this.codec.name);
    ws.binaryType = 'arraybuffer';
    ws.onopen = function () { self.backoff = 1000; };
    ws.onmessage = function (m) {
      var buf = m.data, n = new DataView(buf).getUint32(0);
      var hdr = JSON.parse(new TextDecoder().decode(new Uint8Array(buf, 4, n)));
      self.queue.push({ hdr: hdr, data: new Uint8Array(buf, 4 + n) });
      if (self.queue.length > 120) self.queue.splice(0, self.queue.length - 120);
      self.pump();
    };
    ws.onclose = function () {
      if (!self.active) return;
      setTimeout(function () { self.connect(); }, self.backoff);
      self.backoff = Math.min(self.backoff * 2, 15000);
    };
  };

  Player.prototype.pump = function () {
    var sb = this.sb;
    if (!sb || sb.updating || !this.queue.length || this.ms.readyState !== 'open') return;
    var item = this.queue.shift();
    if (item.hdr.kind === 'init') {
      if (item.hdr.session === this.session) { this.pump(); return; }  // already initialised
      this.session = item.hdr.session;
      sb.timestampOffset = item.hdr.offset;
    } else if (item.hdr.session !== this.session) {
      this.pump();  // fragment of a run we have no init for
      return;
    }
    try {
      sb.appendBuffer(item.data);
    } catch (e) {
      // QuotaExceeded: drop what's been played and try again later.
      this.queue.unshift(item);
      this.trim(true);
    }
  };

  Player.prototype.afterAppend = function () {
    var b = this.sb && this.sb.buffered;
    if (this.started || !b || !b.length) return;
    var end = b.end(b.length - 1);
    if (end - b.start(0) < 0.6) return;  // wait for a little audio
    this.el.currentTime = Math.max(b.start(0), end - TARGET);
    this.started = true;
    this.onchange();
  };

  // Keep the playhead near the live edge and the buffer small.
  Player.prototype.maintain = function () {
    if (!this.started || !this.sb) return;
    var b = this.sb.buffered, el = this.el;
    if (!b.length) return;
    var end = b.end(b.length - 1);
    if (end - el.currentTime > TARGET + 2.5 || el.currentTime < b.start(0)) {
      el.currentTime = end - TARGET;  // fell behind (stall, background tab) or into a gap
    }
    if (el.paused && this.active) el.play().catch(function () {});
    this.trim(false);
  };

  Player.prototype.trim = function (force) {
    var sb = this.sb;
    if (!sb || sb.updating || !sb.buffered.length) return;
    var start = sb.buffered.start(0), now = this.el.currentTime;
    if (force || now - start > 30) {
      if (now - 10 > start) sb.remove(start, now - 10);
    }
  };

  Player.prototype.stop = function () {
    if (!this.active) return;
    this.active = false;
    this.started = false;
    clearInterval(this.timer);
    if (this.ws) { this.ws.onclose = null; this.ws.close(); }
    if (this.el) { this.el.pause(); this.el.removeAttribute('src'); this.el.srcObject = null; this.el.load(); }
    if (this.objectURL) URL.revokeObjectURL(this.objectURL);
    this.el = this.ms = this.sb = this.ws = this.objectURL = null;
    this.queue = [];
    this.onchange();
  };

  // Audio position being heard now, or null when not playing.
  Player.prototype.playhead = function () {
    if (!this.active || !this.started || !this.el || this.el.paused) return null;
    return this.el.currentTime;
  };

  LO.AudioPlayer = Player;
})(window.LO = window.LO || {});
