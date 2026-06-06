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

  // ─── Content scope ─────────────────────────────────────────────────────────
  // Build a Range covering only the document content — excluding
  // .pf-annotations, .pf-version-history, #pf-margin-rail, #pf-selform.
  // These elements follow the article content at the bottom of <body>.

  function buildContentScope() {
    // Preferred: the server-emitted content column (/ui path) — cleanly
    // excludes the annotation chrome without walking body children.
    var col = document.getElementById('pf-doc-col');
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
    if (!window.matchMedia('(min-width:1100px)').matches) return;

    var islandEl = document.getElementById('pf-annot-data');
    if (!islandEl) return;

    var payload;
    try {
      payload = JSON.parse(islandEl.textContent || '');
    } catch (e) { return; }
    if (!payload || !payload.mem_id) return;

    var commits = Array.isArray(payload.commits) ? payload.commits : [];

    // rail is only required for bubbles; the selection flow must work even on
    // a document with zero existing annotations (creating the first one).
    var rail = document.getElementById('pf-margin-rail');

    var scope = buildContentScope();
    if (!scope) return;

    // Kick off async pipeline.
    anchorAllAndWire(commits, payload.mem_id, rail, scope).catch(function () {});
  }

  // ─── Async pipeline ─────────────────────────────────────────────────────────

  async function anchorAllAndWire(commits, memID, rail, scope) {
    var anchored = [];

    for (var i = 0; i < commits.length; i++) {
      try {
        var result = await anchorCommit(commits[i], scope);
        if (result) {
          anchored.push({ commit: commits[i], anchorEl: result.anchorEl });
        }
      } catch (e) {
        // One bad commit must not kill init.
      }
    }

    if (anchored.length > 0) {
      buildMarkers(anchored, memID);
    }
    // Selection flow is independent of existing annotations.
    initSelectionFlow(scope, memID);
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
      // highlightText is synchronous.
      _hl(firstRange, 'mark', { 'class': cls, 'data-commit-id': commit.id });

      var markEl = document.querySelector('mark[data-commit-id="' + commit.id + '"]');
      return markEl ? { anchorEl: markEl } : null;
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
      return headingEl ? { anchorEl: headingEl } : null;
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
        marker.addEventListener('click', function (e) {
          e.stopPropagation();
          if (openId === item.commit.id) { hidePop(); return; }
          showPop(item.commit, marker);
        });
        item.anchorEl.addEventListener('click', function () {
          showPop(item.commit, item.anchorEl);
        });
      })(anchored[i], i + 1);

      var fe = document.querySelector('.pf-annot-entry[data-commit-id="' + anchored[i].commit.id + '"]');
      if (fe) fe.hidden = true;
    }

    document.addEventListener('click', hidePop);
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

      var replyForm  = el('form', { 'method': 'POST', 'action': replyAction,   'class': 'pf-annot-inline-form' });
      var replyTA    = el('textarea', { 'name': 'body',  'rows': '2', 'placeholder': 'Reply…', 'required': '' });
      var replyBtn   = el('button', { 'type': 'submit' });
      replyBtn.appendChild(document.createTextNode('Reply'));
      replyForm.appendChild(replyTA);
      replyForm.appendChild(replyBtn);

      var resolveForm = el('form', { 'method': 'POST', 'action': resolveAction, 'class': 'pf-annot-inline-form' });
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
    var rail = document.getElementById('pf-margin-rail');
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
    var rail = document.getElementById('pf-margin-rail');
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

  function initSelectionFlow(scope, memID) {
    _selbtn = el('button', { 'class': 'pf-selbtn' });
    _selbtn.appendChild(document.createTextNode('+ Annotate'));
    document.body.appendChild(_selbtn);

    _selform = document.getElementById('pf-selform');
    if (!_selform) return;

    // Show/hide selection button on mouseup.
    document.addEventListener('mouseup', debounce(function () {
      handleSelection(scope);
    }, 50));

    // Also track keyboard selection via selectionchange.
    document.addEventListener('selectionchange', debounce(function () {
      handleSelection(scope);
    }, 200));

    _selbtn.addEventListener('click', function () {
      triggerSelform(scope);
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
      showSelWarning('Selection too long (max 2000 chars). Please select a shorter passage.');
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

  function showSelWarning(msg) {
    if (!_warningEl) {
      _warningEl = el('div', {
        'style': 'position:fixed;top:1em;right:1em;background:#fff8c5;border:1px solid #9a6700;border-radius:6px;padding:0.7em 1em;font-size:0.9em;z-index:2000;'
      });
      document.body.appendChild(_warningEl);
    }
    _warningEl.textContent = msg;
    _warningEl.style.display = 'block';
    setTimeout(function () { if (_warningEl) _warningEl.style.display = 'none'; }, 4000);
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
