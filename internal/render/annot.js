// annot.js — polyforge text-selection annotation glue (aihub#125)
// Vanilla JS, no deps, IIFE. Loaded defer after annotator.js (which exposes
// window.AnnotatorDom). Requires a wide viewport (≥1100px) and the
// pf-annot-data JSON island produced by buildAnnotationHTML (Go).
//
// API summary (from vendored @apache-annotator/dom v0.2.0 bundle):
//   describeTextQuote(range, scope?)       → Promise<{exact,prefix,suffix}>
//   createTextQuoteSelectorMatcher(sel)    → (scope) → AsyncIterable<Range>
//   highlightText(range, tagName?, attrs?) → removeHighlights (synchronous)
(function () {
  'use strict';

  // chromeEl resolves one of OUR OWN elements, never one the artifact supplied.
  //
  // aihub#240: the viewer inlines the sanitized agent body BEFORE this chrome, and the sanitizer
  // allows `id` globally — it must, because d2 figures reference their own gradients and clip
  // paths by fragment. getElementById returns the first element in document order with a given
  // id regardless of tag, so an artifact carrying <div id="pf-selform"> was handed to the code
  // below, which then wrote position:fixed / z-index onto it. That is our own JS granting
  // attacker-chosen content the placement properties the sanitizer exists to withhold — the
  // whitelist defeated from the inside rather than bypassed.
  //
  // data-pf-chrome is unforgeable because `data-*` is not on the sanitizer's attribute
  // allowlist: it is stripped from agent content in every form, including valueless and
  // upper-case. Only the server-rendered chrome carries it.
  //
  // chromeElIn takes the root explicitly because aihub#131 resolves the same
  // chrome inside a DOMParser document built from a fetched response. That
  // document contains the sanitized agent body too, so the lookup there needs
  // the marker for exactly the same reason the live one does — arguably more,
  // since anything found in it is about to be imported into the real page.
  function chromeElIn(root, id) {
    return root.querySelector('[data-pf-chrome][id="' + id + '"]');
  }

  function chromeEl(id) {
    return chromeElIn(document, id);
  }

  // ─── Guards ────────────────────────────────────────────────────────────────

  // Bail early on /share or /v1 paths (island won't exist, but be explicit).
  var path = window.location.pathname;
  if (path.indexOf('/share/') === 0 || path.indexOf('/v1/') === 0) return;

  // Must have the AnnotatorDom bundle loaded.
  if (!window.AnnotatorDom) return;

  var _dtq  = window.AnnotatorDom.describeTextQuote;
  var _ctqm = window.AnnotatorDom.createTextQuoteSelectorMatcher;
  var _hl   = window.AnnotatorDom.highlightText;

  // ─── Utilities ─────────────────────────────────────────────────────────────

  function el(tag, attrs, children) {
    var node = document.createElement(tag);
    if (attrs) {
      for (var k in attrs) {
        if (Object.prototype.hasOwnProperty.call(attrs, k)) {
          node.setAttribute(k, attrs[k]);
        }
      }
    }
    if (children) {
      for (var i = 0; i < children.length; i++) {
        var c = children[i];
        if (typeof c === 'string') {
          node.appendChild(document.createTextNode(c));
        } else if (c) {
          node.appendChild(c);
        }
      }
    }
    return node;
  }

  // Relative time label: "3m ago", "2h ago", "5d ago", etc.
  function relTime(isoStr) {
    if (!isoStr) return '';
    var d = new Date(isoStr);
    if (isNaN(d.getTime())) return isoStr.slice(0, 10) || isoStr;
    var diff = Math.max(0, Date.now() - d.getTime()) / 1000;
    if (diff < 60)          return 'just now';
    if (diff < 3600)        return Math.floor(diff / 60) + 'm ago';
    if (diff < 86400)       return Math.floor(diff / 3600) + 'h ago';
    if (diff < 86400 * 30)  return Math.floor(diff / 86400) + 'd ago';
    return isoStr.slice(0, 10);
  }

  // Debounce helper.
  function debounce(fn, ms) {
    var t;
    return function () {
      clearTimeout(t);
      var args = arguments;
      var ctx = this;
      t = setTimeout(function () { fn.apply(ctx, args); }, ms);
    };
  }

  function setDisabled(nodes, on) {
    for (var i = 0; i < nodes.length; i++) nodes[i].disabled = !!on;
  }

  function wideEnough() {
    return window.matchMedia('(min-width:1100px)').matches;
  }

  // ─── Module state (aihub#131) ──────────────────────────────────────────────
  //
  // Everything below used to be built exactly once, on page load, and torn down
  // by navigating away: every write POSTed a form and took the 303 as a full-page
  // reload. aihub#131 submits those forms with fetch and re-renders the annotation
  // layer in place, so the layer now has a LIFECYCLE, and every artefact it puts
  // into the page has to be findable again in order to be removed.
  //
  // 🔴 The failure mode this exists for is silent. A cleanup that is never
  // registered does not throw and does not blank the page — it leaves a <mark>
  // wrapped around text that is about to be wrapped again, or a click listener on
  // a heading that survives its own commit object. Nothing is visibly wrong until
  // a dozen interactions in, when highlights have drifted off their quotes.
  //
  // There is deliberately NO "sweep any stray mark/marker" safety net here. A
  // sweep would repair exactly the leak this registry exists to prevent, which
  // would make the registry's correctness unobservable — the only check this
  // repo can run on it is counting the nodes by hand in a browser, and that count
  // measures nothing if something else is quietly tidying up.

  // _memID / _scope are read at CALL time, never captured in a closure: the
  // content Range is rebuilt on every re-render, and a handler installed once at
  // startup must not go on consulting the Range from the first render.
  var _memID = '';
  var _scope = null;

  // _layer holds everything one render of the annotation layer added to the page.
  //   cleanups     — highlightText's removeHighlights, one per anchored quote.
  //   anchorClicks — {el, fn} pairs. Needed for HEADING anchors in particular:
  //                  a mark disappears with its own unwrap, but a heading is part
  //                  of the document and outlives every render, so its listener
  //                  accumulates one per cycle if it is not removed by hand.
  //   markers      — the numbered buttons. These are SIBLINGS inserted after the
  //                  mark, so unwrapping the mark does not take them with it.
  //   pop          — the single shared popover element.
  //   popDocClick  — the document-level click listener that closes it.
  //   hidden       — flat-list entries buildMarkers hid because a bubble now
  //                  represents them. Usually moot, since swapFlatList replaces
  //                  those nodes outright — but not on swapFlatList's early
  //                  return, where an entry whose commit has stopped anchoring
  //                  would otherwise stay hidden for the life of the page.
  var _layer = {
    cleanups: [], anchorClicks: [], markers: [], hidden: [], pop: null, popDocClick: null
  };

  // _generation is bumped by every teardown. anchorAll is async — it awaits the
  // text-quote matcher once per commit — so two writes submitted close together
  // can interleave: render A can be suspended between creating a highlight and
  // registering that highlight's undo, and a teardown for render B can run in
  // the gap. A's undo would then be filed against a registry that has already
  // been swept, and the mark it belongs to would survive every future teardown.
  // That is precisely the silent leak _layer exists to prevent, arrived at from
  // the other direction, so a superseded render undoes its own work instead.
  var _generation = 0;

  // Selection flow is installed ONCE and never rebuilt: it appends _selbtn to
  // <body> and installs four document-level listeners, so a second call would
  // duplicate all five.
  var _selectionFlowReady = false;
  var _pjaxReady = false;

  // ─── Content scope ─────────────────────────────────────────────────────────
  // Build a Range covering only the document content — excluding
  // .pf-annotations, .pf-version-history, #pf-margin-rail, #pf-selform.
  // These elements follow the article content at the bottom of <body>.

  function buildContentScope() {
    // Preferred: the server-emitted content column (/ui path) — cleanly
    // excludes the annotation chrome without walking body children.
    var col = chromeEl('pf-doc-col');
    if (col) {
      var colRange = document.createRange();
      colRange.selectNodeContents(col);
      return colRange;
    }

    var body = document.body;
    if (!body) return null;

    var EXCLUDED_IDS = { 'pf-margin-rail': true, 'pf-selform': true };
    var EXCLUDED_CLASSES = ['pf-annotations', 'pf-version-history'];

    function isExcluded(node) {
      if (node.nodeType !== 1) return false;
      if (EXCLUDED_IDS[node.id]) return true;
      for (var i = 0; i < EXCLUDED_CLASSES.length; i++) {
        if (node.classList && node.classList.contains(EXCLUDED_CLASSES[i])) {
          return true;
        }
      }
      return false;
    }

    var children = body.childNodes;
    var boundaryIdx = children.length; // default: whole body
    for (var i = 0; i < children.length; i++) {
      if (isExcluded(children[i])) {
        boundaryIdx = i;
        break;
      }
    }

    var range = document.createRange();
    range.setStart(body, 0);
    range.setEnd(body, boundaryIdx);
    return range;
  }

  // ─── Main entry ─────────────────────────────────────────────────────────────

  function main() {
    // Type-qualified, NOT getElementById — the id alone is forgeable by artifact content.
    //
    // aihub#240: the artifact viewer inlines the sanitized agent body into this page, and the
    // sanitizer allows `id` globally (it has to: d2 figures reference their own gradients and
    // clip paths by fragment). getElementById returns the FIRST element in document order with
    // the id regardless of tag, and the agent's body is emitted BEFORE this chrome — so an
    // artifact containing <div id="pf-annot-data">{"mem_id":"…"}</div> was read instead of the
    // real island, and payload.mem_id flows into the reply/resolve POST targets below. That let
    // an artifact author choose which artifact a reviewer's comment landed on.
    //
    // What makes this selector unforgeable is that <script> is not on the sanitizer's element
    // allowlist, so agent content cannot produce a <script id="pf-annot-data"> at all — only
    // the server-rendered island matches.
    var payload = readIsland();
    if (!payload) return;
    _memID = payload.mem_id;

    // aihub#131: form interception is installed at EVERY width, unlike the
    // highlight/marker layer below.
    //
    // Below 1041px the flat list under #pf-annot-list IS the annotation UI:
    // viewer.css hides it at `body.pf-annot-active .pf-annotations` and restores
    // `display:block` inside `@media (max-width:1040px)`, and pf-annot-active is
    // set server-side on every non-review artifact page, not by buildMarkers. So
    // its POST forms are the only way to annotate on a narrow viewport — and
    // gating interception on the marker layer's 1100px threshold would leave the
    // full-page refresh in place in precisely that case.
    initPjax();

    // Seeded into the write chain rather than fired and forgotten: a submit made
    // while the first anchoring pass is still running would otherwise be the one
    // case the chain does not order.
    _writeChain = syncLayerToViewport();
    _writeChain.catch(function () {});
  }

  // syncLayerToViewport is the SINGLE owner of everything whose presence depends
  // on the viewport width. Both entry points go through it: the initial load
  // (main) and every in-place update (applyResponseDocument).
  //
  // 🔴 It exists because doing this by hand went wrong in both directions, and
  // neither direction threw:
  //
  //   * The in-place path called renderLayer() and nothing else, so after a
  //     narrow load that was later widened, a write produced highlights, markers
  //     and the popover but NOT the floating "+ Annotate" button — quote-anchored
  //     annotation was simply dead until the reader reloaded. Before aihub#131
  //     that write was a full-page POST→303→reload, so main() re-ran and
  //     installed it; the regression came in with the in-place update.
  //   * Earlier in the same work item, renderLayer had no width check of its own,
  //     so the first in-place write on a NARROW viewport installed a marker layer
  //     the initial load had deliberately withheld.
  //
  // Same seam, opposite signs. Both are the same mistake: a caller reproducing a
  // subset of another caller's setup. The fix is not two more width checks, it is
  // that there is now one function to call and each piece it calls carries its own
  // precondition — so the composition cannot drift out of step with itself.
  function syncLayerToViewport() {
    initSelectionFlow(); // install-once AND width-gated, both checks its own
    return renderLayer(); // width-gated, its own check
  }

  // ─── Data island ────────────────────────────────────────────────────────────

  // readIsland parses the CURRENT island. It is re-read after every in-place
  // update rather than closed over, so the island element stays the single
  // source of truth for what the page believes it is showing.
  function readIsland() {
    var islandEl = islandElement();
    if (!islandEl) return null;
    var payload;
    try {
      payload = JSON.parse(islandEl.textContent || '');
    } catch (e) { return null; }
    if (!payload || !payload.mem_id) return null;
    return payload;
  }

  // Type-qualified: <script> is not on the sanitizer's element allowlist, so this
  // selector cannot match agent content. That holds inside a parsed response
  // document too, which carries the same sanitized body.
  function islandIn(root) {
    return root.querySelector('script#pf-annot-data[type="application/json"]');
  }

  function islandElement() {
    return islandIn(document);
  }

  // ─── Layer lifecycle ────────────────────────────────────────────────────────

  // renderLayer builds the highlight + marker layer from the island as it stands
  // right now. Safe to call repeatedly, but ONLY after teardownLayer: it assumes
  // the document carries no marks or markers of its own.
  function renderLayer() {
    var payload = readIsland();
    if (!payload) return Promise.resolve();
    _memID = payload.mem_id;

    // Rebuilt every time. highlightText SPLITS text nodes to wrap them, and
    // removeHighlight unwraps without ever calling normalize(), so the node
    // boundaries the previous Range was built from no longer describe the same
    // positions. Nothing re-joins them; the Range is simply stale.
    _scope = buildContentScope();
    if (!_scope) return Promise.resolve();

    // Highlights and markers are a >=1100px affordance: below that, viewer.css
    // shows the flat list back and the marker layer has no column to live in.
    // The check lives HERE, in the function that owns the marker layer, rather
    // than in whichever caller happens to be invoking it — that is what stops a
    // second caller from getting the width policy wrong, which has already
    // happened once in each direction (see syncLayerToViewport).
    if (!wideEnough()) return Promise.resolve();

    var commits = Array.isArray(payload.commits) ? payload.commits : [];
    return anchorAll(commits).catch(function () {});
  }

  // teardownLayer removes everything the previous renderLayer put in the page.
  // Each block below removes one class of artefact; see _layer for why a mark's
  // unwrap does not cover the other three.
  function teardownLayer() {
    // Bumped FIRST: any render still in flight must see the change on its very
    // next resumption, including one suspended inside the loop below.
    _generation++;

    for (var i = 0; i < _layer.cleanups.length; i++) {
      // One unwrap that throws must not strand the ones after it. Nothing is
      // repaired here — a failed unwrap still leaves its mark in the page, and
      // the mark count is what the manual check looks at.
      try { _layer.cleanups[i](); } catch (e) { /* keep unwinding */ }
    }
    _layer.cleanups = [];

    for (var j = 0; j < _layer.anchorClicks.length; j++) {
      var a = _layer.anchorClicks[j];
      try { a.el.removeEventListener('click', a.fn); } catch (e) { /* detached */ }
    }
    _layer.anchorClicks = [];

    for (var k = 0; k < _layer.markers.length; k++) {
      var m = _layer.markers[k];
      if (m && m.parentNode) m.parentNode.removeChild(m);
    }
    _layer.markers = [];

    for (var h = 0; h < _layer.hidden.length; h++) {
      _layer.hidden[h].hidden = false;
    }
    _layer.hidden = [];

    if (_layer.popDocClick) {
      document.removeEventListener('click', _layer.popDocClick);
      _layer.popDocClick = null;
    }
    if (_layer.pop && _layer.pop.parentNode) {
      _layer.pop.parentNode.removeChild(_layer.pop);
    }
    _layer.pop = null;
  }

  // ─── Async pipeline ─────────────────────────────────────────────────────────

  // anchorAll holds its undo functions LOCALLY and only publishes them into
  // _layer once the whole pass has finished without being superseded. Pushing
  // them as they are produced would hand them to whichever registry happens to
  // be current at that moment, which is not necessarily the one that owns them.
  async function anchorAll(commits) {
    var gen = _generation;
    var anchored = [];
    var cleanups = [];

    // A superseded render is responsible for its own marks: nothing else has a
    // handle on them, and the teardown that superseded it has already run.
    function abandon(extra) {
      if (extra) cleanups.push(extra);
      for (var n = cleanups.length - 1; n >= 0; n--) {
        try { cleanups[n](); } catch (e) { /* keep unwinding */ }
      }
    }

    for (var i = 0; i < commits.length; i++) {
      var result = null;
      try {
        result = await anchorCommit(commits[i], _scope);
      } catch (e) {
        // One bad commit must not kill the render.
        result = null;
      }
      if (gen !== _generation) {
        abandon(result && result.cleanup);
        return;
      }
      if (result) {
        if (result.cleanup) cleanups.push(result.cleanup);
        anchored.push({ commit: commits[i], anchorEl: result.anchorEl });
      }
    }

    if (gen !== _generation) {
      abandon(null);
      return;
    }

    for (var j = 0; j < cleanups.length; j++) _layer.cleanups.push(cleanups[j]);
    if (anchored.length > 0) {
      buildMarkers(anchored, _memID);
    }
  }

  // ─── Anchor a single commit ─────────────────────────────────────────────────

  async function anchorCommit(commit, scope) {
    var anchor = commit.anchor;

    // Case 1: text quote selector.
    if (anchor && anchor.quote) {
      var sel = { exact: anchor.quote };
      if (anchor.prefix) sel.prefix = anchor.prefix;
      if (anchor.suffix) sel.suffix = anchor.suffix;

      // createTextQuoteSelectorMatcher returns an async generator function.
      var matcher = _ctqm(sel);
      var firstRange = null;
      for await (var r of matcher(scope)) {
        firstRange = r;
        break; // Take only the first match.
      }
      if (!firstRange) return null;

      var cls = 'pf-annot-highlight';
      if (commit.status === 'resolved') cls += ' pf-annot-highlight--resolved';
      // highlightText is synchronous and returns its own undo (removeHighlights,
      // annotator.js). That return value used to be dropped on the floor, which
      // was harmless while the layer was built once per page load; it is the
      // whole ballgame now that aihub#131 rebuilds it in place.
      var cleanup = _hl(firstRange, 'mark', { 'class': cls, 'data-commit-id': commit.id });

      var markEl = document.querySelector('mark[data-commit-id="' + commit.id + '"]');
      if (!markEl) {
        // The wrap happened but we cannot find it, so nobody downstream will ever
        // hold its undo. Spend it here rather than leaking a mark we no longer
        // have a handle on.
        try { cleanup(); } catch (e) { /* nothing else to try */ }
        return null;
      }
      return { anchorEl: markEl, cleanup: cleanup };
    }

    // Case 2: heading anchor (no quote or quote absent).
    if (anchor && (anchor.heading_id || anchor.heading_text)) {
      var headingEl = null;
      if (anchor.heading_id) {
        headingEl = document.getElementById(anchor.heading_id);
      }
      if (!headingEl && anchor.heading_text) {
        var headings = document.querySelectorAll('h1,h2,h3,h4,h5,h6');
        for (var i = 0; i < headings.length; i++) {
          if (headings[i].textContent.trim() === anchor.heading_text.trim()) {
            headingEl = headings[i];
            break;
          }
        }
      }
      // No cleanup: nothing was inserted. The heading is part of the document and
      // survives every render — which is exactly why the click listener
      // buildMarkers attaches to it has to be unregistered by hand (see _layer).
      return headingEl ? { anchorEl: headingEl, cleanup: null } : null;
    }

    return null; // No anchor → stays in flat list.
  }

  // ─── Build margin rail ──────────────────────────────────────────────────────

  function buildRail(anchored, memID, rail) {
    // Sort by document Y position for greedy push-down.
    anchored.sort(function (a, b) {
      return offsetTopInDoc(a.anchorEl) - offsetTopInDoc(b.anchorEl);
    });

    var bubbleMap  = {}; // commit.id → bubble element
    var anchorMap  = {}; // commit.id → anchor element

    for (var i = 0; i < anchored.length; i++) {
      var item   = anchored[i];
      var bubble = buildBubble(item.commit, memID);
      rail.appendChild(bubble);
      bubbleMap[item.commit.id] = bubble;
      anchorMap[item.commit.id] = item.anchorEl;
    }

    // Activate layout.
    rail.removeAttribute('hidden');
    document.body.classList.add('pf-annot-active');

    // Hide bubbled flat-list entries (keep add-comment form and unanchored entries).
    for (var j = 0; j < anchored.length; j++) {
      var cid = anchored[j].commit.id;
      var flatEntry = document.querySelector('.pf-annot-entry[data-commit-id="' + cid + '"]');
      if (flatEntry) flatEntry.hidden = true;
    }

    // Initial layout pass + re-layout on resize/load.
    positionBubbles(anchored, bubbleMap);
    var debouncedLayout = debounce(function () { positionBubbles(anchored, bubbleMap); }, 120);
    window.addEventListener('resize', debouncedLayout);
    window.addEventListener('load', function () { positionBubbles(anchored, bubbleMap); });

    // Two-way linking.
    wireLinking(anchored, bubbleMap, anchorMap);
  }

  // ─── Inline markers + popover (aihub#159 step4a) ─────────────────────────────
  // Replaces the margin rail: each anchored commit gets a small numbered marker
  // inserted right after its highlight; clicking the marker (or the highlight)
  // opens a single shared popover whose content reuses buildBubble. This frees the
  // right column for the consolidated side rail (TOC/Details/Version/Comments).
  function buildMarkers(anchored, memID) {
    anchored.sort(function (a, b) {
      return offsetTopInDoc(a.anchorEl) - offsetTopInDoc(b.anchorEl);
    });
    document.body.classList.add('pf-annot-active');

    var pop = el('div', { 'class': 'pf-annot-popover' });
    pop.hidden = true;
    pop.addEventListener('click', function (e) { e.stopPropagation(); });
    document.body.appendChild(pop);
    // Registered so the next in-place render removes it. Without this the page
    // collects one detached-but-attached popover per cycle, each still listening
    // on document (below) and each closing over a stale commit object.
    _layer.pop = pop;
    var openId = null;

    function hidePop() { pop.hidden = true; openId = null; }
    function showPop(commit, nearEl) {
      while (pop.firstChild) pop.removeChild(pop.firstChild);
      var bub = buildBubble(commit, memID);
      // The reply/resolve inline forms are gated behind .pf-margin-bubble--active
      // (collapsed in the rail by default); the popover always shows them.
      bub.classList.add('pf-margin-bubble--active');
      pop.appendChild(bub);
      pop.hidden = false;
      var r = nearEl.getBoundingClientRect();
      var left = Math.min(window.scrollX + r.left, window.scrollX + window.innerWidth - 332);
      pop.style.top = (window.scrollY + r.bottom + 6) + 'px';
      pop.style.left = Math.max(8, left) + 'px';
      openId = commit.id;
    }

    for (var i = 0; i < anchored.length; i++) {
      (function (item, n) {
        var marker = el('button', {
          'class':          'pf-annot-marker',
          'type':           'button',
          'data-commit-id': item.commit.id,
          'data-status':    item.commit.status || 'open',
          'aria-label':     'annotation ' + n
        });
        marker.appendChild(document.createTextNode(String(n)));
        if (item.anchorEl.parentNode) {
          item.anchorEl.parentNode.insertBefore(marker, item.anchorEl.nextSibling);
        }
        // The marker is a SIBLING of the highlight, not a child of it, so
        // removeHighlights does not take it down. Track it explicitly.
        _layer.markers.push(marker);
        marker.addEventListener('click', function (e) {
          e.stopPropagation();
          if (openId === item.commit.id) { hidePop(); return; }
          showPop(item.commit, marker);
        });
        var onAnchorClick = function () {
          showPop(item.commit, item.anchorEl);
        };
        item.anchorEl.addEventListener('click', onAnchorClick);
        // For a quote anchor this is belt-and-braces (the mark is unwrapped and
        // the node goes away with its listeners). For a HEADING anchor it is the
        // only thing that removes the listener at all.
        _layer.anchorClicks.push({ el: item.anchorEl, fn: onAnchorClick });
      })(anchored[i], i + 1);

      var fe = document.querySelector('.pf-annot-entry[data-commit-id="' + anchored[i].commit.id + '"]');
      if (fe) {
        fe.hidden = true;
        _layer.hidden.push(fe);
      }
    }

    document.addEventListener('click', hidePop);
    _layer.popDocClick = hidePop;
  }

  // ─── Avatar helpers ──────────────────────────────────────────────────────────

  var _avPalette = ['#3a7ca5','#b5683a','#6a7f3a','#8a5cf0','#6b6b73','#3a8a6b'];

  function initials(name) {
    if (!name) return '?';
    var words = name.trim().split(/\s+/);
    var out = '';
    for (var i = 0; i < Math.min(2, words.length); i++) {
      if (words[i].length > 0) out += words[i][0].toUpperCase();
    }
    return out || '?';
  }

  function avatarColor(name) {
    if (!name) return _avPalette[0];
    var sum = 0;
    for (var i = 0; i < name.length; i++) sum += name.charCodeAt(i);
    return _avPalette[sum % _avPalette.length];
  }

  // ─── Bubble DOM construction ─────────────────────────────────────────────────

  function buildBubble(commit, memID) {
    var isResolved = commit.status === 'resolved';
    var bubble = el('div', {
      'class':           'pf-margin-bubble',
      'data-commit-id':  commit.id,
      'data-status':     commit.status || 'open'
    });

    // Header: avatar + author + time + status (always shown).
    var header = el('div', { 'class': 'pf-margin-bubble-header' });
    var authorName = commit.author_display || '';
    var av = el('span', {
      'class': 'pf-av',
      'style': 'background:' + avatarColor(authorName)
    });
    av.appendChild(document.createTextNode(initials(authorName)));
    header.appendChild(av);
    var authorStrong = el('strong');
    authorStrong.appendChild(document.createTextNode(authorName));
    header.appendChild(authorStrong);
    header.appendChild(document.createTextNode(' · ' + relTime(commit.created_at)));
    var statusClass = isResolved ? 'pf-annot-status pf-st-resolved' : 'pf-annot-status pf-st-open';
    var statusLabel = isResolved ? 'resolved' : 'open';
    var badge = el('span', { 'class': statusClass });
    badge.appendChild(document.createTextNode(statusLabel));
    header.appendChild(badge);
    bubble.appendChild(header);

    // Quote excerpt.
    if (commit.anchor && commit.anchor.quote) {
      var q = commit.anchor.quote;
      if (q.length > 80) q = q.slice(0, 80) + '…';
      var quoteDiv = el('div', { 'class': 'pf-annot-quote' });
      quoteDiv.appendChild(document.createTextNode('“' + q + '”'));
      bubble.appendChild(quoteDiv);
    }

    // Body.
    var bodyDiv = el('div', { 'class': 'pf-annot-body' });
    bodyDiv.appendChild(document.createTextNode(commit.body || ''));
    bubble.appendChild(bodyDiv);

    // Legacy AI reply (resolved only).
    if (isResolved && commit.reply) {
      var replyDiv = el('div', { 'class': 'pf-annot-reply' });
      var aiLabel = el('strong');
      aiLabel.appendChild(document.createTextNode('AI reply: '));
      replyDiv.appendChild(aiLabel);
      replyDiv.appendChild(document.createTextNode(commit.reply));
      bubble.appendChild(replyDiv);
    }

    // Threaded replies.
    if (commit.replies && commit.replies.length > 0) {
      var repliesDiv = el('div', { 'class': 'pf-annot-replies' });
      for (var i = 0; i < commit.replies.length; i++) {
        var r = commit.replies[i];
        var replyItem = el('div', { 'class': 'pf-annot-reply-item' });
        var replyMeta = el('div', { 'class': 'pf-annot-reply-meta' });
        var rAuthor = el('strong');
        rAuthor.appendChild(document.createTextNode(r.author_display || ''));
        replyMeta.appendChild(rAuthor);
        replyMeta.appendChild(document.createTextNode(' · ' + relTime(r.created_at)));
        replyItem.appendChild(replyMeta);
        var rBody = el('div', { 'class': 'pf-annot-body' });
        rBody.appendChild(document.createTextNode(r.body || ''));
        replyItem.appendChild(rBody);
        repliesDiv.appendChild(replyItem);
      }
      bubble.appendChild(repliesDiv);
    }

    // Reply + resolve forms for open commits.
    if (!isResolved) {
      var formsDiv = el('div', { 'class': 'pf-annot-inline-forms' });
      var replyAction   = '/ui/artifacts/' + encodeURIComponent(memID) +
                          '/commit/' + encodeURIComponent(commit.id) + '/reply';
      var resolveAction = '/ui/artifacts/' + encodeURIComponent(memID) +
                          '/commit/' + encodeURIComponent(commit.id) + '/resolve';

      // data-pf-chrome on these two mirrors what the server puts on its own copies
      // of the same forms (buildAnnotationHTMLWithExact). It is what makes the
      // popover's reply/resolve update in place like every other write form
      // instead of falling through to a full-page POST (aihub#131).
      var replyForm  = el('form', { 'data-pf-chrome': '', 'method': 'POST', 'action': replyAction,   'class': 'pf-annot-inline-form' });
      var replyTA    = el('textarea', { 'name': 'body',  'rows': '2', 'placeholder': 'Reply…', 'required': '' });
      var replyBtn   = el('button', { 'type': 'submit' });
      replyBtn.appendChild(document.createTextNode('Reply'));
      replyForm.appendChild(replyTA);
      replyForm.appendChild(replyBtn);

      var resolveForm = el('form', { 'data-pf-chrome': '', 'method': 'POST', 'action': resolveAction, 'class': 'pf-annot-inline-form' });
      var resolveTA   = el('textarea', { 'name': 'reply', 'rows': '2', 'placeholder': 'Resolution note (optional)' });
      var resolveBtn  = el('button', { 'type': 'submit' });
      resolveBtn.appendChild(document.createTextNode('Resolve'));
      resolveForm.appendChild(resolveTA);
      resolveForm.appendChild(resolveBtn);

      formsDiv.appendChild(replyForm);
      formsDiv.appendChild(resolveForm);
      bubble.appendChild(formsDiv);
    }

    return bubble;
  }

  // ─── Layout: greedy push-down ────────────────────────────────────────────────

  function offsetTopInDoc(node) {
    var top = 0;
    var n = node;
    while (n && n !== document.body) {
      top += (n.offsetTop || 0);
      n = n.offsetParent;
    }
    return top;
  }

  function positionBubbles(anchored, bubbleMap) {
    var curBottom = 0;
    var GAP = 8;
    // Bubble tops are relative to the rail's content box — convert document
    // offsets into rail-local coordinates.
    var rail = chromeEl('pf-margin-rail');
    var railTop = rail ? offsetTopInDoc(rail) : 0;
    for (var i = 0; i < anchored.length; i++) {
      var item   = anchored[i];
      var bubble = bubbleMap[item.commit.id];
      if (!bubble) continue;

      var anchorY = offsetTopInDoc(item.anchorEl) - railTop;
      var top = Math.max(i === 0 ? anchorY : curBottom + GAP, anchorY);

      bubble.style.position = 'absolute';
      bubble.style.top      = top + 'px';
      bubble.style.left     = '0';
      bubble.style.right    = '0';

      curBottom = top + (bubble.offsetHeight || 60);
    }
  }

  // ─── Two-way linking ─────────────────────────────────────────────────────────

  function wireLinking(anchored, bubbleMap, anchorMap) {
    // Click on highlight mark → activate bubble + scroll into rail view.
    document.addEventListener('click', function (e) {
      var mark = e.target.closest ? e.target.closest('mark[data-commit-id]') : null;
      if (!mark) return;
      var cid = mark.getAttribute('data-commit-id');
      activateBubble(cid, bubbleMap);
      scrollBubbleIntoView(bubbleMap[cid]);
    });

    // Click on bubble (not form controls) → scroll anchor into view + flash.
    for (var i = 0; i < anchored.length; i++) {
      (function (item) {
        var bubble = bubbleMap[item.commit.id];
        if (!bubble) return;
        bubble.addEventListener('click', function (e) {
          var tag = (e.target.tagName || '').toLowerCase();
          if (tag === 'button' || tag === 'textarea' || tag === 'input' || tag === 'a') return;
          if (e.target.closest && e.target.closest('form')) return;

          activateBubble(item.commit.id, bubbleMap);

          var anchorEl = anchorMap[item.commit.id];
          if (!anchorEl) return;
          anchorEl.scrollIntoView({ behavior: 'smooth', block: 'center' });

          // Transient --active flash on highlight mark(s).
          flashMarks(item.commit.id);
        });
      })(anchored[i]);
    }
  }

  function activateBubble(cid, bubbleMap) {
    for (var id in bubbleMap) {
      if (Object.prototype.hasOwnProperty.call(bubbleMap, id)) {
        bubbleMap[id].classList.remove('pf-margin-bubble--active');
      }
    }
    if (bubbleMap[cid]) bubbleMap[cid].classList.add('pf-margin-bubble--active');
  }

  function scrollBubbleIntoView(bubble) {
    if (!bubble) return;
    var rail = chromeEl('pf-margin-rail');
    if (!rail) { bubble.scrollIntoView({ behavior: 'smooth', block: 'nearest' }); return; }
    var bTop = bubble.offsetTop;
    var bH   = bubble.offsetHeight;
    var rH   = rail.clientHeight;
    if (bTop < rail.scrollTop) {
      rail.scrollTop = bTop - 8;
    } else if (bTop + bH > rail.scrollTop + rH) {
      rail.scrollTop = bTop + bH - rH + 8;
    }
  }

  function flashMarks(cid) {
    var marks = document.querySelectorAll('mark[data-commit-id="' + cid + '"]');
    for (var j = 0; j < marks.length; j++) {
      marks[j].classList.add('pf-annot-highlight--active');
    }
    setTimeout(function () {
      var ms = document.querySelectorAll('mark[data-commit-id="' + cid + '"]');
      for (var k = 0; k < ms.length; k++) {
        ms[k].classList.remove('pf-annot-highlight--active');
      }
    }, 1500);
  }

  // ─── Selection flow ──────────────────────────────────────────────────────────

  var _selbtn  = null;
  var _selform = null;
  var _warningEl = null;

  // initSelectionFlow is INSTALL-ONCE and is deliberately not part of
  // teardownLayer/renderLayer. It appends _selbtn to <body> and installs four
  // document-level listeners; calling it a second time would leave the page with
  // two floating buttons and two of every handler, and the duplicates would be
  // unreachable for removal. After an in-place update only its STATE is reset —
  // see resetSelectionUI.
  //
  // The handlers below read the module-level _scope at call time. They used to
  // close over the Range passed in here, which was correct exactly once: the
  // Range is rebuilt on every render, and a closure holding the first one would
  // silently start judging "is this selection inside the document?" against
  // boundaries computed before the highlights moved.
  function initSelectionFlow() {
    if (_selectionFlowReady) return;
    // Width check BEFORE the ready flag, never after: a page opened narrow and
    // later widened must still be able to install this on a subsequent call. If
    // the flag were set here on a narrow viewport, the wide call would early-return
    // and the "+ Annotate" button would never exist for the life of the page.
    if (!wideEnough()) return;
    _selectionFlowReady = true;

    _selbtn = el('button', { 'class': 'pf-selbtn' });
    _selbtn.appendChild(document.createTextNode('+ Annotate'));
    document.body.appendChild(_selbtn);

    _selform = chromeEl('pf-selform');
    if (!_selform) return;

    // Show/hide selection button on mouseup.
    document.addEventListener('mouseup', debounce(function () {
      handleSelection(_scope);
    }, 50));

    // Also track keyboard selection via selectionchange.
    document.addEventListener('selectionchange', debounce(function () {
      handleSelection(_scope);
    }, 200));

    _selbtn.addEventListener('click', function () {
      triggerSelform(_scope);
    });

    // Hide on Escape.
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') hideSelUI();
    });

    // Hide when clicking outside selform / selbtn.
    document.addEventListener('mousedown', function (e) {
      if (_selform && !_selform.hidden &&
          !_selform.contains(e.target) && e.target !== _selbtn) {
        hideSelUI();
      }
    });
  }

  function handleSelection(scope) {
    // While the annotation form is open, focusing its textarea collapses the
    // document selection — don't let that dismiss the form. Escape or an
    // outside mousedown are the only ways to close it.
    if (_selform && !_selform.hidden) return;
    var sel = window.getSelection();
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) { hideSelbtn(); return; }
    var range = sel.getRangeAt(0);
    if (!rangeInsideScope(range, scope)) { hideSelbtn(); return; }
    showSelbtn(range);
  }

  function hideSelbtn() {
    if (_selbtn) _selbtn.style.display = 'none';
  }

  function rangeInsideScope(range, scope) {
    try {
      return scope.compareBoundaryPoints(Range.START_TO_START, range) <= 0 &&
             scope.compareBoundaryPoints(Range.END_TO_END, range) >= 0;
    } catch (e) { return false; }
  }

  function showSelbtn(range) {
    if (!_selbtn) return;
    var rects = range.getClientRects();
    if (!rects || rects.length === 0) return;
    var last = rects[rects.length - 1];
    // .pf-selbtn is position:fixed → viewport coordinates, no scroll offsets.
    _selbtn.style.left = Math.min(last.right, window.innerWidth - 130) + 'px';
    _selbtn.style.top  = (last.bottom + 8) + 'px';
    _selbtn.style.display = 'block';
  }

  async function triggerSelform(scope) {
    var sel = window.getSelection();
    if (!sel || sel.isCollapsed || sel.rangeCount === 0) { hideSelUI(); return; }
    var range = sel.getRangeAt(0);
    if (!rangeInsideScope(range, scope)) { hideSelUI(); return; }

    var quote;
    try {
      // describeTextQuote is async → returns Promise<{exact,prefix,suffix}>.
      quote = await _dtq(range, scope);
    } catch (e) { hideSelUI(); return; }
    if (!quote || !quote.exact) { hideSelUI(); return; }

    if (quote.exact.length > 2000) {
      showToast('Selection too long (max 2000 chars). Please select a shorter passage.');
      return;
    }

    setField('quote',        quote.exact);
    setField('prefix',       (quote.prefix || '').slice(-64));
    setField('suffix',       (quote.suffix || '').slice(0, 64));

    var hEl = findNearestHeading(range.startContainer);
    setField('heading_id',   hEl ? (hEl.id || '')                   : '');
    setField('heading_text', hEl ? (hEl.textContent.trim() || '')   : '');

    // Position selform in place — directly below the selection end (where the
    // annotate button was), clamped to the viewport. NOT pinned to the right
    // edge: on wide screens that detaches the input from the text being
    // annotated.
    var rects = range.getClientRects();
    if (rects && rects.length > 0) {
      var last = rects[rects.length - 1];
      var fw = 320; // approximate form width incl. padding
      var left = Math.max(8, Math.min(last.left, window.innerWidth - fw - 16));
      var top = Math.min(last.bottom + 8, window.innerHeight - 220);
      // Position is dynamic (computed from selection geometry); appearance
      // is now handled by viewer.css #pf-selform so dark mode works correctly.
      _selform.style.position = 'fixed';
      _selform.style.top      = top + 'px';
      _selform.style.right    = 'auto';
      _selform.style.left     = left + 'px';
      _selform.style.zIndex   = '900';
      _selform.style.maxWidth = '300px';
    }
    _selform.hidden = false;
    _selbtn.style.display = 'none';

    var ta = _selform.querySelector('textarea[name="body"]');
    if (ta) ta.focus();
  }

  function setField(name, value) {
    if (!_selform) return;
    var inputs = _selform.querySelectorAll('[name="' + name + '"]');
    for (var i = 0; i < inputs.length; i++) inputs[i].value = value;
  }

  function findNearestHeading(node) {
    var n = node;
    while (n && n !== document.body) {
      if (n.nodeType === 1) {
        // Check preceding siblings for a heading with an id.
        var prev = n.previousElementSibling;
        while (prev) {
          if (/^H[1-6]$/.test(prev.tagName) && prev.id) return prev;
          prev = prev.previousElementSibling;
        }
        if (/^H[1-6]$/.test(n.tagName) && n.id) return n;
      }
      n = n.parentNode;
    }
    return null;
  }

  function hideSelUI() {
    if (_selbtn)  _selbtn.style.display = 'none';
    if (_selform) _selform.hidden = true;
  }

  // resetSelectionUI puts the selection affordances back to their at-rest state
  // after a successful write. form.reset() is what clears the hidden quote /
  // prefix / suffix / heading fields, because their at-rest value is the empty
  // string the server rendered them with — leaving them populated would let the
  // next submission inherit the previous selection's anchor.
  function resetSelectionUI() {
    if (_selbtn) _selbtn.style.display = 'none';
    if (_selform) {
      _selform.hidden = true;
      if (typeof _selform.reset === 'function') _selform.reset();
    }
  }

  // Toast used for both the selection-length warning and write failures. Errors
  // have to surface here now: with the 303 no longer taken as a navigation,
  // there is no full-page error document to fall back on.
  function showToast(msg) {
    if (!_warningEl) {
      _warningEl = el('div', {
        'style': 'position:fixed;top:1em;right:1em;background:#fff8c5;border:1px solid #9a6700;border-radius:6px;padding:0.7em 1em;font-size:0.9em;z-index:2000;max-width:min(28em,80vw);'
      });
      _warningEl.setAttribute('role', 'status');
      document.body.appendChild(_warningEl);
    }
    _warningEl.textContent = msg;
    _warningEl.style.display = 'block';
    clearTimeout(_warningEl._pfTimer);
    _warningEl._pfTimer = setTimeout(function () {
      if (_warningEl) _warningEl.style.display = 'none';
    }, 5000);
  }

  // ─── In-place update (aihub#131) ────────────────────────────────────────────
  //
  // PJAX-lite: submit the EXISTING form endpoints with fetch, let the browser
  // follow their 303 to the artifact page, and lift the annotation layer out of
  // the page that comes back. No new server route, and no second rendering of a
  // commit — the flat list and the data island in that response are the same
  // bytes the server would have sent on a full reload, so the JS-rendered layer
  // and the no-JS fallback cannot drift apart.
  //
  // The no-JS path is untouched: these are still ordinary <form method="POST">
  // elements, and preventDefault is only reached for a form this page emitted.

  function initPjax() {
    if (_pjaxReady) return;
    _pjaxReady = true;
    // Capture phase: decide before anything else on the page sees the event.
    // Native constraint validation still runs first — a `required` textarea that
    // is empty never fires `submit` at all — so the browser's own messaging is
    // preserved.
    document.addEventListener('submit', function (e) {
      var form = e.target;
      if (!form || form.nodeType !== 1 || form.tagName !== 'FORM') return;
      if (!form.hasAttribute('data-pf-chrome')) return;
      if (!isAnnotationAction(form.getAttribute('action') || '')) return;
      e.preventDefault();
      enqueueWrite(form);
    }, true);
  }

  // _writeChain serialises annotation writes.
  //
  // Two forms can be in flight at once — setDisabled only disables the form being
  // submitted — and each response is a whole page, rendered at the instant the
  // server handled that request. Arrival order is not commit order: if A's
  // follow-GET is rendered before B commits but lands after B's, applying A last
  // replaces the layer with a page that does not contain B. B is saved and
  // invisible until a manual reload, which reads to the author exactly like a
  // write that failed — the worst possible way for this to go wrong, because it
  // invites them to type it again.
  //
  // Chaining is preferred over a sequence number that discards stale responses:
  // discarding still leaves the layer showing a page that predates the newest
  // write. Serialising means every response was rendered after every write that
  // preceded it, so the newest one is always complete.
  //
  // It also means no two renderLayer passes overlap, which is why anchorAll's
  // generation check should never fire in practice. That check stays anyway: it
  // makes anchorAll correct on its own terms rather than by a promise about the
  // order its callers happen to run in.
  var _writeChain = Promise.resolve();

  function enqueueWrite(form) {
    var run = function () { return submitAnnotationForm(form); };
    _writeChain = _writeChain.then(run, run);
    // A rejected tail with nothing chained after it would surface as an
    // unhandled rejection; this marks it handled without breaking the chain.
    _writeChain.catch(function () {});
    return _writeChain;
  }

  // isAnnotationAction answers "does this form write an annotation on THIS
  // artifact?" structurally, by matching path segments against the id in the
  // island, rather than by a substring test on a route string.
  function isAnnotationAction(action) {
    if (!_memID) return false;
    var u;
    try { u = new URL(action, window.location.href); } catch (e) { return false; }
    if (u.origin !== window.location.origin) return false;
    var segs;
    try { segs = decodeURIComponent(u.pathname).split('/'); } catch (e) { return false; }
    // ['', 'ui', 'artifacts', <memID>, 'commit'] — plus ['<commitID>','reply'|'resolve'].
    return segs.length >= 5 &&
           segs[1] === 'ui' && segs[2] === 'artifacts' &&
           segs[3] === _memID && segs[4] === 'commit';
  }

  async function submitAnnotationForm(form) {
    var buttons = form.querySelectorAll('button[type="submit"], button:not([type])');
    setDisabled(buttons, true);

    // finally, not a re-enable on each exit path: if anything below throws, a
    // button left permanently disabled with no message is indistinguishable from
    // a hung page, and the author's text is trapped behind it.
    try {
      var resp;
      try {
        resp = await fetch(form.getAttribute('action'), {
          method: 'POST',
          body: new FormData(form),
          credentials: 'same-origin',
          redirect: 'follow'
        });
      } catch (e) {
        showToast('Could not reach the server. Nothing was saved — your text is still here.');
        return;
      }

      if (!resp.ok) {
        showToast('Save failed (' + resp.status + '). Your text is still here — try again.');
        return;
      }

      var doc = null;
      try {
        doc = new DOMParser().parseFromString(await resp.text(), 'text/html');
      } catch (e) { /* handled by the island check below */ }

      if (!doc || !islandIn(doc)) {
        // 200, but not the artifact page — the likeliest cause is an expired
        // session, where the handler answers redirectToLogin. The write either
        // happened or was refused; either way re-submitting is wrong, so follow
        // wherever the server actually sent us.
        window.location.href = resp.url || window.location.href;
        return;
      }

      try {
        await applyResponseDocument(doc);
      } catch (e) {
        // The write itself succeeded — only the re-render did not. Say so, and
        // point at the recovery that is guaranteed to work.
        showToast('Saved, but the page could not refresh itself. Reload to see it.');
        return;
      }
      if (form.isConnected && typeof form.reset === 'function') form.reset();
    } finally {
      setDisabled(buttons, false);
    }
  }

  // applyResponseDocument swaps the annotation layer for the one in `doc` and
  // rebuilds the client-side half. The document body is deliberately NOT touched:
  // an annotation never changes rendered_html, so leaving the article alone is
  // both correct and what preserves the reader's scroll position for free.
  function applyResponseDocument(doc) {
    var island = islandElement();
    var newIsland = islandIn(doc);
    if (!island || !newIsland) return Promise.resolve();

    // Teardown FIRST. It has to run before the swap because a marker is inserted
    // next to its anchor, and a heading anchor is resolved document-wide — so a
    // marker can sit inside the very section swapFlatList is about to replace.
    // Removing it after the swap would mean removing a node that is no longer in
    // the document, silently leaving _layer.markers holding detached elements
    // while the page keeps whatever the swap brought in.
    teardownLayer();

    island.textContent = newIsland.textContent;
    swapFlatList(doc);
    swapSideRailComments(doc);
    resetSelectionUI();
    // syncLayerToViewport, NOT renderLayer: the in-place path must end in exactly
    // the state a fresh server render would produce at this width, and renderLayer
    // is only one of the two things that depend on width.
    //
    // Returned, not discarded: the write chain awaits it, which is what makes
    // "no two renders overlap" true rather than merely likely.
    return syncLayerToViewport();
  }

  // swapFlatList replaces the server-rendered thread groups — and only those.
  //
  // The <h2> heading and the .pf-annot-form add-comment block are left in place
  // on purpose. That block carries a nonced inline <script> (the heading_text
  // mirror emitted by buildAnnotationHTMLWithExact) which would NOT execute after
  // being inserted from a parsed document, so re-inserting it would quietly
  // disable the mirror and start storing empty heading_text. Its <select> is
  // built from the document's headings, which annotations do not change, so
  // there is nothing in it to refresh anyway.
  function swapFlatList(doc) {
    var host = chromeEl('pf-annot-list');
    var fresh = chromeElIn(doc, 'pf-annot-list');
    if (!host || !fresh) return;

    var anchorNode = host.querySelector(':scope > .pf-annot-form');
    var old = host.querySelectorAll(':scope > .pf-annot-section');
    for (var i = 0; i < old.length; i++) {
      if (old[i].parentNode) old[i].parentNode.removeChild(old[i]);
    }
    var incoming = fresh.querySelectorAll(':scope > .pf-annot-section');
    for (var j = 0; j < incoming.length; j++) {
      var node = document.importNode(incoming[j], true);
      if (anchorNode) host.insertBefore(node, anchorNode);
      else host.appendChild(node);
    }
  }

  // swapSideRailComments keeps the rail's Comments card in step with the layer.
  // Without it, adding a comment leaves the rail reading "3" next to four
  // comments until the next full page load.
  //
  // The card's own click wiring is a nonced inline script in the server's side
  // rail, which will not re-run for an inserted node, so it is re-attached here.
  function swapSideRailComments(doc) {
    var rail = chromeEl('pf-side-rail');
    var freshRail = chromeElIn(doc, 'pf-side-rail');
    if (!rail || !freshRail) return;

    var oldCard = commentsCard(rail);
    var source = commentsCard(freshRail);
    if (!source) {
      if (oldCard && oldCard.parentNode) oldCard.parentNode.removeChild(oldCard);
      return;
    }

    var card = document.importNode(source, true);
    if (oldCard) {
      // <details> reflects the reader's own expand/collapse into the `open`
      // ATTRIBUTE, so carrying it across is what stops a reply from folding the
      // card shut under the reader.
      if (oldCard.hasAttribute('open')) card.setAttribute('open', '');
      else card.removeAttribute('open');
      oldCard.parentNode.replaceChild(card, oldCard);
    } else {
      // First comment on this artifact: the server did not emit the card at all
      // on the page currently loaded.
      rail.appendChild(card);
    }
    wireSideRailComments(card);
  }

  // Scoped to a rail that chromeElIn has already established is ours, so the
  // class selector inside it is not reachable by artifact content.
  function commentsCard(rail) {
    var body = rail.querySelector('.pf-side-cmt');
    return body ? body.closest('details') : null;
  }

  // Mirrors the side rail's server-side inline handler. Only ever called on a
  // freshly inserted card, so the server-rendered one keeps its own listeners and
  // nothing is bound twice.
  function wireSideRailComments(root) {
    if (!root) return;
    var items = root.querySelectorAll('.pf-side-cmt-item');
    for (var i = 0; i < items.length; i++) {
      (function (btn) {
        btn.addEventListener('click', function (e) {
          e.stopPropagation();
          var id = btn.getAttribute('data-commit-id');
          var mk = document.querySelector('.pf-annot-marker[data-commit-id="' + id + '"]') ||
                   document.querySelector('mark[data-commit-id="' + id + '"]');
          if (mk) {
            mk.scrollIntoView({ behavior: 'smooth', block: 'center' });
            mk.click();
          }
        });
      })(items[i]);
    }
  }

  // ─── Bootstrap ──────────────────────────────────────────────────────────────
  // Scripts are defer, so DOMContentLoaded has fired before this executes.
  // The guard handles the rare edge case where a dynamic script injection
  // runs before DOMContentLoaded (e.g. test harnesses).

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', main);
  } else {
    main();
  }

})();
