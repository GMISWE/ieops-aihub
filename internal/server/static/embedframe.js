// embedframe.js — aihub#240
//
// Parent-side counterpart to annotation-bridge.js, deliberately covering ONE message type.
//
// Agent-authored markdown on /ui/wi/:id and /ui/memories/:id is rendered inside a sandboxed
// iframe (render.SafeEmbedDocument) instead of being inlined into this page. A sandboxed
// frame carrying no allow-same-origin has an opaque origin, so the parent cannot measure its
// content; the frame reports its own height and this file applies it. Without that, every
// embedded document would sit in a fixed-height box with an inner scrollbar.
//
// Scope, stated so this is not mistaken for the annotation bridge landing: the protocol also
// carries 'selected' (frame -> parent) and 'highlight'/'clear' (parent -> frame). Those are
// the annotation closure, and wiring them requires rehoming annot.js's document-DOM walking
// across the frame boundary. That is aihub#245 and is NOT done here. Messages of those types
// are ignored by this listener rather than half-handled — a partial implementation of an
// annotation protocol drops reviewer comments silently, which is worse than not offering it.
(function () {
  'use strict';

  var PROTOCOL_VERSION = 1;
  var SELECTOR = 'iframe.pf-embed';
  // A document taller than this is a bug or an attack, not a document. The cap keeps a
  // hostile height from turning the page into an unscrollable void.
  var MAX_HEIGHT = 40000;
  var MIN_HEIGHT = 40;

  // Origin cannot be used to authenticate these frames.
  //
  // sandbox without allow-same-origin gives the srcdoc document an opaque origin, so
  // ev.origin arrives as the string "null" for every one of them — indistinguishable from
  // any other opaque-origin frame a page might contain. Identity therefore comes from
  // ev.source: the sending window must BE the contentWindow of an iframe we ourselves put
  // in this page. That is a stronger check than an origin string, because it cannot be
  // spoofed by a message forged from elsewhere.
  function frameFor(source) {
    var frames = document.querySelectorAll(SELECTOR);
    for (var i = 0; i < frames.length; i++) {
      if (frames[i].contentWindow === source) { return frames[i]; }
    }
    return null;
  }

  function isHeightMessage(d) {
    if (!d || typeof d !== 'object') { return false; }
    if (d.source !== 'pf-annot-bridge') { return false; }
    if (d.v !== PROTOCOL_VERSION) { return false; }
    if (d.type !== 'height') { return false; }
    // Reject NaN and Infinity explicitly: both are typeof 'number' and both would produce
    // a garbage style value.
    if (typeof d.height !== 'number' || !isFinite(d.height)) { return false; }
    return true;
  }

  window.addEventListener('message', function (ev) {
    if (!isHeightMessage(ev.data)) { return; }
    var frame = frameFor(ev.source);
    if (!frame) { return; }

    var h = Math.ceil(ev.data.height);
    if (h < MIN_HEIGHT) { h = MIN_HEIGHT; }
    if (h > MAX_HEIGHT) { h = MAX_HEIGHT; }
    frame.style.height = h + 'px';
  }, false);
})();
