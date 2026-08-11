// annotation-bridge.js — aihub#240
//
// Runs INSIDE the sandboxed artifact iframe (safeembed.go). This file is first-party,
// server-injected, security-reviewed code — it is not part of the agent's document, which
// is why it is allowed to execute under sandbox="allow-scripts" while the agent's own
// script is stripped by the sanitizer and refused by the CSP.
//
// It exists so annotation does not cost isolation. The alternative designs both lose:
// dropping the sandbox to let the parent reach into the document trades away the whole
// containment story, and letting agent content carry its own JS is the wrong granularity
// (01-static-html-render-engine-research.md §0.2, §2.5).
//
// Anchoring is quote-based with surrounding context, following Hypothesis, rather than a
// DOM path or character offset. Agents regenerate documents: the same content comes back
// with different whitespace, attribute order and element nesting, so a structural anchor
// breaks on regeneration while a text quote survives it.
//
// Protocol (both directions are validated, never trusted):
//   iframe -> parent   {source:'pf-annot-bridge', v:1, type:'selected',  anchor}
//   iframe -> parent   {source:'pf-annot-bridge', v:1, type:'height',    height}
//   parent -> iframe   {source:'pf-annot-host',   v:1, type:'highlight', anchor, commitId}
//   parent -> iframe   {source:'pf-annot-host',   v:1, type:'clear'}
(function () {
  'use strict';

  var CFG = window.__PF_BRIDGE_CONFIG__ || {};
  var PARENT_ORIGIN = typeof CFG.parentOrigin === 'string' ? CFG.parentOrigin : '';
  var PROTOCOL_VERSION = 1;
  var CTX = 32; // characters of context captured either side of a quote

  // The frame is sandboxed without allow-same-origin, so it has an opaque origin and
  // cannot read the parent's. The server knows it and injects it; without it we refuse
  // to post rather than fall back to '*', which would broadcast document text to any
  // window that happens to be listening.
  function postToParent(msg) {
    if (!PARENT_ORIGIN) { return; }
    try {
      parent.postMessage(msg, PARENT_ORIGIN);
    } catch (e) { /* parent gone; nothing useful to do from inside a sandbox */ }
  }

  // ---- anchoring -----------------------------------------------------------

  function docText() {
    return document.body ? (document.body.innerText || document.body.textContent || '') : '';
  }

  // An anchor is the quote plus its neighbouring text. The context disambiguates repeated
  // phrases ("see below" appearing nine times) without pinning to structure.
  function buildAnchor(sel) {
    var quote = String(sel.toString());
    if (!quote.trim()) { return null; }

    var whole = docText();
    var at = whole.indexOf(quote);
    var prefix = '';
    var suffix = '';
    if (at >= 0) {
      prefix = whole.slice(Math.max(0, at - CTX), at);
      suffix = whole.slice(at + quote.length, at + quote.length + CTX);
    }
    return { quote: quote, prefix: prefix, suffix: suffix };
  }

  // Resolve an anchor back to a range. Exact-with-context first; then exact anywhere;
  // then give up. Deliberately no fuzzy/edit-distance matching: a near-miss highlight
  // lands the reader's comment on text they did not write it about, which is worse than
  // an honest "could not locate".
  function findRange(anchor) {
    if (!anchor || typeof anchor.quote !== 'string' || !anchor.quote) { return null; }

    var whole = docText();
    var candidates = [];
    var from = 0;
    for (;;) {
      var i = whole.indexOf(anchor.quote, from);
      if (i < 0) { break; }
      candidates.push(i);
      from = i + 1;
      if (candidates.length > 200) { break; } // pathological document guard
    }
    if (!candidates.length) { return null; }

    var best = candidates[0];
    if (candidates.length > 1) {
      var bestScore = -1;
      for (var c = 0; c < candidates.length; c++) {
        var idx = candidates[c];
        var pre = whole.slice(Math.max(0, idx - CTX), idx);
        var suf = whole.slice(idx + anchor.quote.length, idx + anchor.quote.length + CTX);
        var score = commonTail(pre, anchor.prefix || '') + commonHead(suf, anchor.suffix || '');
        if (score > bestScore) { bestScore = score; best = idx; }
      }
    }
    return rangeAtTextOffset(best, anchor.quote.length);
  }

  function commonTail(a, b) {
    var n = 0;
    while (n < a.length && n < b.length && a[a.length - 1 - n] === b[b.length - 1 - n]) { n++; }
    return n;
  }

  function commonHead(a, b) {
    var n = 0;
    while (n < a.length && n < b.length && a[n] === b[n]) { n++; }
    return n;
  }

  // Walk text nodes to convert a plain-text offset into a DOM Range.
  function rangeAtTextOffset(offset, length) {
    var walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT, null);
    var seen = 0, startNode = null, startOff = 0, endNode = null, endOff = 0, node;
    while ((node = walker.nextNode())) {
      var len = node.nodeValue.length;
      if (!startNode && seen + len > offset) {
        startNode = node;
        startOff = offset - seen;
      }
      if (startNode && seen + len >= offset + length) {
        endNode = node;
        endOff = offset + length - seen;
        break;
      }
      seen += len;
    }
    if (!startNode || !endNode) { return null; }
    var r = document.createRange();
    try {
      r.setStart(startNode, startOff);
      r.setEnd(endNode, endOff);
    } catch (e) { return null; }
    return r;
  }

  // ---- highlighting --------------------------------------------------------

  function highlight(anchor, commitId) {
    var r = findRange(anchor);
    if (!r) { return false; }
    var mark = document.createElement('mark');
    mark.className = 'pf-annot-mark';
    if (commitId) { mark.setAttribute('data-commit-id', String(commitId)); }
    try {
      r.surroundContents(mark);
    } catch (e) {
      // surroundContents throws when the range straddles element boundaries. Fall back
      // to extract+insert, which handles partial selections across inline elements.
      try {
        mark.appendChild(r.extractContents());
        r.insertNode(mark);
      } catch (e2) { return false; }
    }
    return true;
  }

  function clearHighlights() {
    var marks = document.querySelectorAll('mark.pf-annot-mark');
    for (var i = 0; i < marks.length; i++) {
      var m = marks[i];
      var parentNode = m.parentNode;
      if (!parentNode) { continue; }
      while (m.firstChild) { parentNode.insertBefore(m.firstChild, m); }
      parentNode.removeChild(m);
      parentNode.normalize();
    }
  }

  // ---- inbound messages ----------------------------------------------------

  // Every inbound message is checked on three axes before anything is done with it:
  // where it came from, that it is shaped like our protocol, and that its fields have
  // the types we expect. The agent's document shares this window, so an unvalidated
  // handler would be a way for embedded content to drive our own privileged code.
  function isTrustedMessage(ev) {
    // Fail closed. An unconfigured origin previously skipped this check entirely, so the
    // one deployment state where we know least about our surroundings was also the one
    // where we validated least. If we cannot name the peer, we do not trust any peer.
    if (!PARENT_ORIGIN) { return false; }
    if (ev.origin !== PARENT_ORIGIN) { return false; }
    if (ev.source !== parent) { return false; }
    var d = ev.data;
    if (!d || typeof d !== 'object') { return false; }
    if (d.source !== 'pf-annot-host') { return false; }
    if (d.v !== PROTOCOL_VERSION) { return false; }
    if (typeof d.type !== 'string') { return false; }
    return true;
  }

  window.addEventListener('message', function (ev) {
    if (!isTrustedMessage(ev)) { return; }
    var d = ev.data;

    if (d.type === 'highlight') {
      var a = d.anchor;
      if (!a || typeof a !== 'object' || typeof a.quote !== 'string') { return; }
      if (a.quote.length > 5000) { return; }
      highlight({
        quote: a.quote,
        prefix: typeof a.prefix === 'string' ? a.prefix : '',
        suffix: typeof a.suffix === 'string' ? a.suffix : ''
      }, typeof d.commitId === 'string' ? d.commitId : '');
      return;
    }

    if (d.type === 'clear') {
      clearHighlights();
    }
  }, false);

  // ---- outbound ------------------------------------------------------------

  document.addEventListener('mouseup', function () {
    var sel = window.getSelection();
    if (!sel || sel.isCollapsed) { return; }
    var anchor = buildAnchor(sel);
    if (!anchor) { return; }
    postToParent({ source: 'pf-annot-bridge', v: PROTOCOL_VERSION, type: 'selected', anchor: anchor });
  }, false);

  // The parent cannot measure a cross-origin frame's content, so the frame reports its
  // own height. Without this the iframe keeps its default size and long documents get an
  // inner scrollbar instead of flowing in the page.
  function reportHeight() {
    var h = 0;
    if (document.documentElement) { h = document.documentElement.scrollHeight; }
    if (document.body && document.body.scrollHeight > h) { h = document.body.scrollHeight; }
    postToParent({ source: 'pf-annot-bridge', v: PROTOCOL_VERSION, type: 'height', height: h });
  }

  if (document.readyState === 'complete' || document.readyState === 'interactive') {
    reportHeight();
  } else {
    document.addEventListener('DOMContentLoaded', reportHeight, false);
  }
  window.addEventListener('load', reportHeight, false);
  if (typeof ResizeObserver === 'function' && document.body) {
    new ResizeObserver(reportHeight).observe(document.body);
  }
})();
