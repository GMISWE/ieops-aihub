// memories.js — client-side behaviour for the polyforge /ui/memories card list.
//
// SERVER vs CLIENT split (authoritative, see the work-item brief):
//   SERVER-SIDE (re-query the backend via the existing query params the frozen
//   handler already reads): project switch, type filter, search (q),
//   strength_min, limit. Those are driven by the shared filter <form> + the
//   self-drawn dropdowns in dropdown.js, which dispatch `pf-filter` and trigger
//   an HTMX hx-get that reloads #mem-list-body. This file does NOT touch them.
//
//   CLIENT-SIDE (operate on the cards already rendered within the current
//   recall window, no server round-trip): sort (newest / oldest / strength),
//   the Mine / All owner toggle, and pagination + per-page count. That is all
//   this file does. It reads data-created / data-strength / data-author off each
//   card and reorders / shows / hides them in place. Nothing is persisted to the
//   backend.
//
// Re-init: the server-side filter reload swaps #mem-list-body (HTMX
// hx-select="#mem-list-body" + hx-swap="outerHTML"), re-rendering the cards. We
// re-run init() on htmx:afterSwap so the freshly rendered cards get avatars,
// the active sort, the active owner filter, and pagination re-applied.
(function () {
  "use strict";

  var PER_PAGE_DEFAULT = 12;

  // Persist nothing to the backend; these are per-page, in-memory only.
  // gridMinH reserves the tallest (full-page) grid height so the pager strip
  // below it keeps a fixed vertical position instead of jumping up on a
  // partial last page.
  var state = { sort: "newest", owner: "all", perPage: PER_PAGE_DEFAULT, page: 1, gridMinH: 0 };

  // Avatar initials + color come from the shared avatars.js module
  // (window.pfPaintAvatars over [data-av-name]); the previously inlined
  // initialsFor/avatarColorClass/paintAvatars were extracted there so the
  // memory cards and the memory-detail comment chips share one painter that
  // stays byte-identical to the Go helpers. Loaded before this file in the
  // template.
  function paintAvatars(grid) {
    if (window.pfPaintAvatars) window.pfPaintAvatars(grid);
  }

  // ---- card collection -----------------------------------------------------

  function gridEl() {
    return document.querySelector("[data-mem-grid]");
  }

  function allCards(grid) {
    return Array.prototype.slice.call(grid.querySelectorAll("[data-mem-card]"));
  }

  // Cards that pass the current owner (Mine/All) filter. Owner filtering is a
  // hard show/hide; sort + pagination only ever consider this matched set.
  function matchedCards(grid) {
    var me = currentUserId();
    return allCards(grid).filter(function (c) {
      if (state.owner === "mine") return me !== "" && c.getAttribute("data-author") === me;
      return true;
    });
  }

  function currentUserId() {
    var el = document.querySelector("[data-mem-user]");
    return el ? el.getAttribute("data-mem-user") || "" : "";
  }

  // ---- sort ----------------------------------------------------------------

  function sortCards(grid) {
    var cards = allCards(grid);
    cards.sort(function (a, b) {
      if (state.sort === "strength") {
        return num(b, "data-strength") - num(a, "data-strength");
      }
      var ca = num(a, "data-created"), cb = num(b, "data-created");
      return state.sort === "oldest" ? ca - cb : cb - ca;
    });
    // Re-append in sorted order; DOM order is the source of truth for paging.
    cards.forEach(function (c) { grid.appendChild(c); });
  }

  function num(el, attr) {
    var v = parseFloat(el.getAttribute(attr));
    return isNaN(v) ? 0 : v;
  }

  // ---- pagination ----------------------------------------------------------

  function pageSequence(total, cur) {
    if (total <= 7) {
      var all = [];
      for (var i = 1; i <= total; i++) all.push(i);
      return all;
    }
    var want = {}; want[1] = want[2] = want[total] = want[total - 1] = true;
    want[cur] = want[cur - 1] = want[cur + 1] = true;
    var seq = [], prev = 0;
    for (var p = 1; p <= total; p++) {
      if (!want[p]) continue;
      if (p - prev > 1) seq.push("…");
      seq.push(p); prev = p;
    }
    return seq;
  }

  // Show only the current page's slice of the matched set; hide everything else
  // (including owner-filtered-out cards). Then render the pager controls.
  function applyPaging(grid) {
    var matched = matchedCards(grid);
    var unmatched = allCards(grid).filter(function (c) { return matched.indexOf(c) === -1; });
    unmatched.forEach(function (c) { c.hidden = true; });

    var pages = Math.max(1, Math.ceil(matched.length / state.perPage));
    if (state.page > pages) state.page = 1;

    var start = (state.page - 1) * state.perPage;
    var end = start + state.perPage;
    matched.forEach(function (c, i) { c.hidden = i < start || i >= end; });

    reserveGridHeight(grid, matched.length, start);
    renderPager(grid, matched.length, pages, start, end);
  }

  // Keep the pager at a fixed vertical position across pages: reserve the
  // tallest (full-page) grid height as a min-height so a partial last page does
  // not collapse the grid and pull the pager up. Only engages when there is
  // more than one page; single-page lists never jump.
  function reserveGridHeight(grid, matchedCount, start) {
    var multiPage = matchedCount > state.perPage;
    if (!multiPage) { grid.style.minHeight = ""; return; }
    var onThisPage = Math.max(0, Math.min(state.perPage, matchedCount - start));
    if (onThisPage >= state.perPage) {
      // A full page — measure its natural height and remember the tallest.
      grid.style.minHeight = "";
      var h = grid.offsetHeight;
      if (h > state.gridMinH) state.gridMinH = h;
    }
    grid.style.minHeight = state.gridMinH ? state.gridMinH + "px" : "";
  }

  function renderPager(grid, totalMatched, pages, start, end) {
    var bar = document.querySelector("[data-mem-pager]");
    if (!bar) return;
    if (totalMatched === 0) { bar.hidden = true; return; }
    bar.hidden = false;

    var info = bar.querySelector("[data-mem-pinfo]");
    if (info) {
      var lo = totalMatched === 0 ? 0 : start + 1;
      var hi = Math.min(end, totalMatched);
      info.textContent = lo + "-" + hi + " of " + totalMatched;
    }

    var pagesBox = bar.querySelector("[data-mem-pages]");
    if (!pagesBox) return;
    pagesBox.textContent = "";

    var cur = state.page;

    var prev = mkBtn("‹", "pbtn pnav", cur <= 1, function () {
      if (state.page > 1) { state.page--; applyPaging(grid); }
    });
    prev.setAttribute("aria-label", "Previous page");
    pagesBox.appendChild(prev);

    pageSequence(pages, cur).forEach(function (tok) {
      if (tok === "…") {
        var ell = document.createElement("span");
        ell.className = "pell";
        ell.textContent = "…";
        pagesBox.appendChild(ell);
        return;
      }
      var b = mkBtn(String(tok), "pbtn" + (tok === cur ? " on" : ""), false, function () {
        state.page = tok; applyPaging(grid);
      });
      pagesBox.appendChild(b);
    });

    var next = mkBtn("›", "pbtn pnav", cur >= pages, function () {
      if (state.page < pages) { state.page++; applyPaging(grid); }
    });
    next.setAttribute("aria-label", "Next page");
    pagesBox.appendChild(next);
  }

  function mkBtn(text, cls, disabled, onClick) {
    var b = document.createElement("button");
    b.type = "button";
    b.className = cls;
    b.textContent = text;
    if (disabled) b.disabled = true;
    else b.addEventListener("click", onClick);
    return b;
  }

  // ---- re-rendering --------------------------------------------------------

  // Re-apply sort then paging (paging depends on the owner filter + DOM order).
  function rerender() {
    var grid = gridEl();
    if (!grid) {
      var bar = document.querySelector("[data-mem-pager]");
      if (bar) bar.hidden = true;
      return;
    }
    sortCards(grid);
    applyPaging(grid);
  }

  // ---- wiring (idempotent; bound once, survives swaps) ---------------------

  var wired = false;
  function wireControls() {
    if (wired) return; // the controls live OUTSIDE #mem-list-body, so they
    wired = true;       // survive the HTMX swap — bind them exactly once.

    // Sort dropdown: dropdown.js already toggles .on + the button label and
    // closes the menu (no data-dd-field => it never fires a server reload). We
    // add a parallel listener that reads data-sort and re-sorts in place.
    var sortDD = document.querySelector("[data-mem-sort]");
    if (sortDD) {
      sortDD.querySelectorAll(".dd-it[data-sort]").forEach(function (it) {
        it.addEventListener("click", function () {
          state.sort = it.getAttribute("data-sort") || "newest";
          state.page = 1;
          rerender();
        });
      });
    }

    // Mine / All segmented control.
    var ownerSeg = document.querySelector("[data-mem-owner]");
    if (ownerSeg) {
      ownerSeg.querySelectorAll("button[data-owner]").forEach(function (b) {
        b.addEventListener("click", function () {
          ownerSeg.querySelectorAll("button").forEach(function (x) {
            x.classList.toggle("on", x === b);
          });
          state.owner = b.getAttribute("data-owner") || "all";
          state.page = 1;
          rerender();
        });
      });
    }

    // Per-page dropdown (lives inside the pager, which is re-rendered on swap;
    // re-bound in initBody since its items are recreated each render is false —
    // the pager markup itself is static template, only its page buttons are
    // rebuilt, so binding here once is fine).
    var perDD = document.querySelector("[data-mem-perpage]");
    if (perDD) {
      perDD.querySelectorAll(".dd-it[data-perpage]").forEach(function (it) {
        it.addEventListener("click", function () {
          var n = parseInt(it.getAttribute("data-perpage"), 10);
          if (!isNaN(n) && n > 0) {
            state.perPage = n;
            state.page = 1;
            state.gridMinH = 0; // per-page changed: recompute the reserved height
            rerender();
          }
        });
      });
    }
  }

  // initBody runs against the freshly rendered #mem-list-body: paint avatars,
  // then sort + paginate. Called on load and after every HTMX swap.
  function initBody() {
    var grid = gridEl();
    if (grid) paintAvatars(grid);
    rerender();
  }

  function init() {
    wireControls();
    initBody();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }

  // After a server-side filter reload swaps the list body, re-init the cards.
  document.body.addEventListener("htmx:afterSwap", function (e) {
    var t = e.target;
    if (t && (t.id === "mem-list-body" || (t.querySelector && t.querySelector("[data-mem-grid]")))) {
      // A new recall window => reset to page 1; keep the user's sort + owner.
      state.page = 1;
      state.gridMinH = 0; // new card set: recompute the reserved full-page height
      initBody();
    }
  });
})();
