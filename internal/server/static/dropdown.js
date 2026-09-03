// dropdown.js — custom dropdowns, segmented filters, and client-side search for
// the polyforge /ui work-item list.
//
// Filtering is HTMX-driven and IN PLACE. The project switcher and the "me"
// toggle DO NOT navigate: they write their value into the shared filter <form>
// (the switcher into a hidden field, the toggle via htmx:configRequest below)
// and then fire a single `pf-filter` event on the form. The form carries hx-get
// + hx-target="#wi-list-body" + hx-include="this", so each request swaps just
// the list body while carrying the COMPLETE current param set — which is what
// makes a project switch preserve the selected segment and the owner scope.
//
// Status is NOT a dropdown any more: since the aihub#185 redesign it is the
// LCRS segment sidebar, rendered server-side inside #wi-list-body.
//
// Search is purely client-side: it shows/hides already-rendered rows by text
// match. There is no server-side text search in the domain, so this is an
// honest in-page filter, not a backend query. It is re-applied after every
// HTMX swap so a filter change does not drop the active text query.
(function () {
  "use strict";

  // ---- helpers -------------------------------------------------------------

  // The shared filter form. Controls that live outside it (the project
  // switcher) still drive it via this lookup. wi_list marks its form
  // [data-wi-filters]; the memories list (aihub#137) marks its own
  // [data-mem-filters]. Only one is present per page, so the combined selector
  // keeps the wi page byte-identical while letting the memory page reuse the
  // same dropdown -> pf-filter -> hx-get path.
  function filtersForm() {
    return document.querySelector("[data-wi-filters], [data-mem-filters]");
  }

  // Fire the form's hx-get in place. Debounced so a burst of control changes
  // collapses into one request. HTMX listens for `pf-filter` (see the form's
  // hx-trigger) and includes the whole form (hx-include="this").
  var fireTimer = null;
  function fireFilter(delay) {
    var form = filtersForm();
    if (!form) return;
    if (fireTimer) clearTimeout(fireTimer);
    fireTimer = setTimeout(function () {
      fireTimer = null;
      // Event name must NOT contain a colon: HTMX's hx-trigger tokenizer treats
      // ":" as the modifier separator (delay:, from:), so "pf:filter" would be
      // parsed as event "pf" + a bogus modifier and never match. Use a hyphen.
      form.dispatchEvent(new CustomEvent("pf-filter", { bubbles: true }));
    }, delay || 0);
  }

  // Find the hidden filter input named field anywhere on the page (the project
  // switcher lives outside the <form>, so we look up by data attribute, not by
  // form descendancy).
  function inputFor(field) {
    return document.querySelector('[data-dd-input="' + field + '"]');
  }

  // Close every open menu.
  function closeAllMenus(except) {
    document.querySelectorAll("[data-dd]").forEach(function (dd) {
      var m = dd.querySelector(".dd-menu");
      if (!m || m === except || m.hidden) return;
      m.hidden = true;
    });
  }

  // ---- custom dropdowns (.dd) ---------------------------------------------

  // Single-select dropdown — the only dropdown kind left. It backs the wi-list
  // project switcher and all four of the memories page's ([data-dd] x4:
  // project, type, sort, per-page). Choosing an item writes the hidden field
  // named by data-dd-field and fires the shared in-place filter request; an
  // item with NO data-dd-field (memories' sort / per-page) just relabels and
  // closes, and memories.js handles it from there.
  //
  // On the wi list the segment and owner scope ride along on the form (see
  // htmx:configRequest below), so switching project preserves them. The
  // memories form ([data-mem-filters]) matches none of those selectors and
  // submits its own params.
  function wireDropdown(dd) {
    var btn = dd.querySelector("[data-dd-btn]");
    var menu = dd.querySelector(".dd-menu");
    var lbl = dd.querySelector(".lbl");
    if (!btn || !menu) return;

    btn.addEventListener("click", function (e) {
      e.stopPropagation();
      var willOpen = menu.hidden;
      closeAllMenus(menu);
      menu.hidden = !willOpen;
    });

    menu.querySelectorAll(".dd-it").forEach(function (it) {
      it.addEventListener("click", function (e) {
        e.stopPropagation();
        menu.querySelectorAll(".dd-it").forEach(function (x) {
          x.classList.toggle("on", x === it);
        });
        if (lbl && it.dataset.ddLabel) lbl.textContent = it.dataset.ddLabel;
        menu.hidden = true;

        var field = it.dataset.ddField;
        if (!field) return;
        var input = inputFor(field);
        if (!input) return;
        input.value = it.dataset.ddValue || "";
        fireFilter(0);
      });
    });
  }

  document.querySelectorAll("[data-dd]").forEach(wireDropdown);
  document.addEventListener("click", function () {
    closeAllMenus(null);
  });

  // ---- "me" owner toggle (header) -----------------------------------------

  // Header pill ([data-me-toggle]): on = only my items, off = all (the list
  // defaults to All). Delegated because the header is re-rendered on every swap.
  // Fires the filter form; htmx:configRequest below injects the owner from this
  // toggle's live state (there is no owner segment / hidden owner driver anymore).
  document.body.addEventListener("click", function (e) {
    var b = e.target.closest ? e.target.closest("[data-me-toggle]") : null;
    if (!b) return;
    b.classList.toggle("on");
    // Mine and Watching are mutually exclusive view scopes (aihub#143): turning
    // one on turns the other off. Done here as well as server-side because the
    // request is built from these pills' LIVE classes — leaving both lit would
    // send owner + watching together, which the handler resolves in Watching's
    // favour, and the user would be looking at a "me" pill that is on and doing
    // nothing.
    if (b.classList.contains("on")) {
      var w = document.querySelector("[data-watch-toggle]");
      if (w) w.classList.remove("on");
    }
    fireFilter(0);
  });

  // ---- "watching" scope toggle (header, aihub#143) -------------------------

  // Header pill ([data-watch-toggle]): on = only work items I watch, off = the
  // Mine/All owner scopes. Same delegated-click + configRequest-injection shape
  // as the "me" toggle above, for the same reason: the .grp header is inside
  // #wi-list-body and is replaced on every swap, so a directly-bound listener
  // would survive exactly one request.
  document.body.addEventListener("click", function (e) {
    var b = e.target.closest ? e.target.closest("[data-watch-toggle]") : null;
    if (!b) return;
    b.classList.toggle("on");
    if (b.classList.contains("on")) {
      var me = document.querySelector("[data-me-toggle]");
      if (me) me.classList.remove("on");
    }
    fireFilter(0);
  });

  // ---- keep the filter form's seg param fresh (aihub#185 fix) --------------
  // The sidebar segment links swap only #wi-list-body, so the form's hidden
  // `seg` input (which lives OUTSIDE the swap, in the page header) never updates
  // when you switch segments — it stays at the initial value. So a Mine/All
  // toggle or project switch (which reloads via this form) would send the STALE
  // seg and reset you to it (e.g. Unclaimed). Fix: at request time, read the live
  // selected segment from the sidebar's .on item (which IS re-rendered on every
  // swap) and inject it, so the segment is always preserved.
  document.body.addEventListener("htmx:configRequest", function (e) {
    var el = e.target;
    if (!el || !el.matches) return;
    var isForm = el.matches("form[data-wi-filters]");
    var isSeg = el.matches(".seg-nav .seg-item[data-seg-key]");
    // Done's server pager (aihub#298) lives in .wi-main, not .seg-nav, so it
    // matches neither selector above. Without this it would be the only htmx
    // trigger on the page that sends no owner at all — not an empty owner, the
    // parameter absent — which the handler reads as All, silently dropping the
    // Mine toggle / explicit ?owner= the moment you page the archive. Its href
    // fallback carries owner already; this is the JS path catching up, so both
    // paths agree and there stays exactly ONE place that injects owner.
    var isDonePager = el.matches("[data-done-older], [data-done-newest]");
    if ((!isForm && !isSeg && !isDonePager) || !e.detail || !e.detail.parameters) return;
    // owner: inject the live "me" toggle state for the form reload, the sidebar
    // segment links AND Done's server pager, so switching segment / project or
    // paging the archive keeps the personal filter. Empty = All (the default
    // view). Anything else that swaps #wi-list-body must be added here too — the
    // page has exactly one owner-injection point on purpose.
    var me = document.querySelector("[data-me-toggle]");
    e.detail.parameters.owner = (me && me.classList.contains("on")) ? (me.getAttribute("data-me-owner") || "") : "";
    // watching: the third view scope (aihub#143), injected at the SAME single
    // point and for the same reason — the pill lives inside the swapped
    // #wi-list-body, so its state has to be read at request time or a segment
    // click silently drops the scope. Empty = off. Always assigned, never
    // conditionally omitted: an absent parameter and "watching=" both mean off
    // to the handler, but only the explicit form also overwrites a stale
    // watching=1 sitting in the form's hidden field from the last render.
    var wt = document.querySelector("[data-watch-toggle]");
    e.detail.parameters.watching = (wt && wt.classList.contains("on")) ? "1" : "";
    // seg: for the form reload, read the live active sidebar item (the form's
    // hidden seg input lives outside the swap and would be stale).
    if (isForm) {
      var on = document.querySelector(".seg-nav .seg-item.on[data-seg-key]");
      if (on) e.detail.parameters.seg = on.getAttribute("data-seg-key");
    }
  });

  // ---- per-section pagination ---------------------------------------------

  // Each .grp-wrap paginates its rows independently: at most PAGE_SIZE rows are
  // visible at a time, with a per-block pager (‹ prev, numbered pages, › next).
  // Blocks are fully independent of one another (own current-page state). All
  // rows are already in the DOM (server renders the full block); this is pure
  // client-side show/hide. The header still prints the TOTAL count so paged-out
  // rows are never invisible. To change the page size, edit PAGE_SIZE.
  var PAGE_SIZE = 10; // default; overridden at runtime by the per-page picker (aihub#185)

  // Build the page-number sequence with ellipsis gaps, e.g. for 21 pages on
  // page 1: [1,2,3,"…",21]. Always shows first + last, the current page, and
  // its immediate neighbours; collapses the rest into "…" markers.
  function pageSequence(total, cur) {
    if (total <= 7) {
      var all = [];
      for (var i = 1; i <= total; i++) all.push(i);
      return all;
    }
    var want = { 1: true, 2: true };
    want[total] = true;
    want[total - 1] = true;
    want[cur] = true;
    want[cur - 1] = true;
    want[cur + 1] = true;
    var seq = [];
    var prev = 0;
    for (var p = 1; p <= total; p++) {
      if (!want[p]) continue;
      if (p - prev > 1) seq.push("…");
      seq.push(p);
      prev = p;
    }
    return seq;
  }

  function wirePager(wrap) {
    var rowsBox = wrap.querySelector("[data-grp-rows]");
    var pager = wrap.querySelector("[data-grp-pager]");
    if (!rowsBox || !pager) return;

    // The rows this block paginates: real rows that survived the search filter.
    // The empty-state placeholder never paginates.
    function rows() {
      return Array.prototype.filter.call(rowsBox.children, function (el) {
        return el.hasAttribute("data-wi-row") && !el.dataset.searchHidden;
      });
    }

    wrap._page = 1;
    var footer = wrap.querySelector("[data-wi-pager]");
    var pinfo = wrap.querySelector("[data-grp-pinfo]");
    // "N–M of T" page-info shown in the footer.
    function setPinfo(rs) {
      if (!pinfo) return;
      if (!rs.length) { pinfo.textContent = ""; return; }
      var start = (wrap._page - 1) * PAGE_SIZE;
      var end = Math.min(start + PAGE_SIZE, rs.length);
      pinfo.textContent = (start + 1) + "–" + end + " of " + rs.length;
    }

    // Pool of empty filler rows used to pad a short page (e.g. the last page)
    // up to PAGE_SIZE slots, so the block height is constant across pages and
    // the pager never jumps. Topped up by ensureFillers() (called from
    // _applyPager) so a larger per-page pick gets enough fillers; capped so a
    // 100/page pick doesn't spawn 99 filler nodes.
    var fillers = [];
    function ensureFillers() {
      var want = Math.min(PAGE_SIZE - 1, 24);
      while (fillers.length < want) {
        var f = document.createElement("div");
        f.className = "row-pad";
        f.setAttribute("aria-hidden", "true");
        // Skeleton placeholder matching a real row's shape (id / title+meta /
        // badge+avatar) — the design-system "skeleton matches final shape".
        f.innerHTML =
          '<span class="skel" style="width:36px"></span>' +
          '<span class="pti"><span class="skel" style="width:46%"></span>' +
          '<span class="skel" style="width:28%;height:10px"></span></span>' +
          '<span class="prt"><span class="skel" style="width:54px;height:18px;border-radius:999px"></span>' +
          '<span class="skel av"></span></span>';
        f.hidden = true;
        rowsBox.appendChild(f);
        fillers.push(f);
      }
    }

    function hideFillers() {
      fillers.forEach(function (f) { f.hidden = true; });
    }

    // Show only the current page's slice; hide the rest. Pad the page up to
    // PAGE_SIZE with filler rows (sized to a real row) so the block keeps a
    // constant height regardless of how many rows land on the page.
    function showPage(rs, page) {
      var start = (page - 1) * PAGE_SIZE;
      var end = start + PAGE_SIZE;
      rs.forEach(function (el, i) {
        el.hidden = i < start || i >= end;
      });
      var realOnPage = Math.max(0, Math.min(end, rs.length) - start);
      var need = PAGE_SIZE - realOnPage;
      var rowH = rs.length ? rs[Math.min(start, rs.length - 1)].offsetHeight : 0;
      fillers.forEach(function (f, i) {
        if (i < need) {
          if (rowH) f.style.height = rowH + "px";
          f.hidden = false;
        } else {
          f.hidden = true;
        }
      });
    }

    function go(page, rs, pages) {
      wrap._page = page;
      showPage(rs, page);
      render(rs, pages);
      setPinfo(rs);
    }

    // Render the pager controls for the current matched set + current page.
    function render(rs, pages) {
      pager.textContent = "";
      var cur = wrap._page;

      var prev = document.createElement("button");
      prev.type = "button";
      prev.className = "pbtn pnav";
      prev.textContent = "‹"; // ‹
      prev.setAttribute("aria-label", "Previous page");
      prev.disabled = cur <= 1;
      prev.addEventListener("click", function () {
        if (wrap._page > 1) go(wrap._page - 1, rows(), pages);
      });
      pager.appendChild(prev);

      pageSequence(pages, cur).forEach(function (tok) {
        if (tok === "…") {
          var ell = document.createElement("span");
          ell.className = "pell";
          ell.textContent = "…"; // …
          pager.appendChild(ell);
          return;
        }
        var b = document.createElement("button");
        b.type = "button";
        b.className = "pbtn" + (tok === cur ? " on" : "");
        b.textContent = String(tok);
        b.addEventListener("click", function () {
          go(tok, rows(), pages);
        });
        pager.appendChild(b);
      });

      var next = document.createElement("button");
      next.type = "button";
      next.className = "pbtn pnav";
      next.textContent = "›"; // ›
      next.setAttribute("aria-label", "Next page");
      next.disabled = cur >= pages;
      next.addEventListener("click", function () {
        if (wrap._page < pages) go(wrap._page + 1, rows(), pages);
      });
      pager.appendChild(next);
    }

    // (Re)compute pages from the current matched set and reset to page 1. Hide
    // the pager entirely when a single page covers every matched row. Re-run
    // after search so pages reflect only the matched subset.
    wrap._applyPager = function () {
      var rs = rows();
      var pages = Math.ceil(rs.length / PAGE_SIZE);
      // The footer (page-info + per-page picker + page buttons) is shown whenever
      // the segment has rows, so the per-page control is always usable; the
      // numbered page buttons only appear when there is more than one page.
      if (footer) footer.hidden = rs.length === 0;
      if (rs.length === 0) {
        hideFillers();
        pager.hidden = true;
        pager.textContent = "";
        wrap._page = 1;
        return;
      }
      // Only past the empty early-return: an empty segment shows its empty state,
      // never a padded page, so building fillers for it is pure DOM waste.
      ensureFillers();
      if (wrap._page > pages) wrap._page = 1;
      if (pages <= 1) {
        // One page: show every matched row, no padding, no page buttons (but the
        // footer with the per-page picker + count stays visible).
        rs.forEach(function (el) { el.hidden = false; });
        hideFillers();
        pager.hidden = true;
        pager.textContent = "";
        setPinfo(rs);
        return;
      }
      pager.hidden = false;
      go(wrap._page, rs, pages);
    };
  }

  // wirePager only INSTALLS wrap._applyPager; it deliberately does not run it.
  // initListBody drives exactly one paging pass per (re)render, via applySearch
  // — running it here too paged every block twice on every init and swap.
  function wirePagers() {
    document.querySelectorAll("[data-wi-list] [data-grp]").forEach(wirePager);
  }

  // ---- client-side search --------------------------------------------------

  // Apply the current search text to the rendered rows. Factored out so it can
  // re-run after an HTMX swap replaces the list body (the search box itself
  // lives outside #wi-list-body and survives the swap, so its value persists).
  // It must stay safe to call with no search box on the page: this is the single
  // paging pass initListBody relies on, so an early return here would leave the
  // blocks unpaginated. No box simply means an empty query.
  function applySearch() {
    var search = document.querySelector("[data-wi-search]");
    var q = search ? search.value.trim().toLowerCase() : "";
    document.querySelectorAll("[data-wi-list] [data-grp]").forEach(function (wrap) {
      var rowsBox = wrap.querySelector("[data-grp-rows]");
      if (!rowsBox) return;
      var anyVisible = false;
      Array.prototype.forEach.call(rowsBox.children, function (el) {
        if (!el.hasAttribute("data-wi-row")) return;
        var hay = (el.getAttribute("data-wi-text") || "").toLowerCase();
        var miss = q !== "" && hay.indexOf(q) === -1;
        // Drive `hidden` from BOTH sides, and mark misses so the pager skips
        // them when capping. Hiding a miss is what actually filters the list:
        // _applyPager only ever touches the matched set, so an unhidden miss on
        // the visible page would sit there next to a "1–1 of 1" page-info.
        // Un-hiding a hit is the other half, and it is not symmetry for its own
        // sake — Done (aihub#298) renders a SERVER pager, so wirePager finds no
        // [data-grp-pager], never installs wrap._applyPager, and nothing below
        // would ever restore a row this function hid. Clearing the query there
        // has to be able to bring every row back on its own.
        if (miss) {
          el.dataset.searchHidden = "1";
          el.hidden = true;
        } else {
          delete el.dataset.searchHidden;
          el.hidden = false;
          anyVisible = true;
        }
      });
      // Re-run the pager so pages are recomputed from the matched subset
      // (reset to page 1); cleared search restores full pagination.
      if (wrap._applyPager) wrap._applyPager();
      // Hide the whole section while searching if nothing under it matches.
      wrap.hidden = q !== "" && !anyVisible;
    });
  }

  (function wireSearch() {
    var search = document.querySelector("[data-wi-search]");
    if (search) search.addEventListener("input", applySearch);
  })();

  // ---- client-side sort (aihub#185) ---------------------------------------

  // Newest(↓)/oldest(↑) toggle in the group header ([data-wi-sort]), reordering
  // the rendered rows by data-created. Client-only — no server round-trip; the
  // server renders newest-first, so the default (newest) needs no reorder. The
  // mode is held in memory so it survives HTMX swaps (a full reload resets to
  // newest, matching the server order). Re-applied by initListBody after a swap.
  var wiSortMode = "newest";
  function applySort() {
    document.querySelectorAll("[data-wi-list] [data-grp-rows]").forEach(function (rowsBox) {
      var rows = Array.prototype.filter.call(rowsBox.children, function (el) {
        return el.hasAttribute("data-wi-row");
      });
      rows.sort(function (a, b) {
        var ca = parseInt(a.getAttribute("data-created") || "0", 10);
        var cb = parseInt(b.getAttribute("data-created") || "0", 10);
        return wiSortMode === "newest" ? cb - ca : ca - cb;
      });
      rows.forEach(function (r) { rowsBox.appendChild(r); });
      // Keep any pager filler/padding rows (non data-wi-row) after the real rows,
      // so a reorder never floats fillers above content on an underfull page.
      Array.prototype.filter.call(rowsBox.children, function (el) {
        return !el.hasAttribute("data-wi-row");
      }).forEach(function (el) { rowsBox.appendChild(el); });
    });
    document.querySelectorAll("[data-wi-sort]").forEach(function (btn) {
      btn.textContent = wiSortMode === "newest" ? "↓" : "↑";
    });
  }

  // Delegated — the sort button is re-rendered with the header on every swap.
  document.body.addEventListener("click", function (e) {
    var btn = e.target.closest ? e.target.closest("[data-wi-sort]") : null;
    if (!btn) return;
    wiSortMode = wiSortMode === "newest" ? "oldest" : "newest";
    applySort();
    applySearch(); // re-page from the reordered (still-filtered) set
  });

  // ---- per-page picker (aihub#185) ----------------------------------------

  // Segmented control [data-wi-perpage] sets the page size; delegated so it
  // survives HTMX swaps. PAGE_SIZE is module-level (shared by all pagers).
  document.body.addEventListener("click", function (e) {
    var b = e.target.closest ? e.target.closest("[data-wi-perpage] button[data-perpage]") : null;
    if (!b) return;
    var n = parseInt(b.getAttribute("data-perpage"), 10);
    if (!n) return;
    PAGE_SIZE = n;
    var seg = b.closest("[data-wi-perpage]");
    seg.querySelectorAll("button").forEach(function (x) { x.classList.toggle("on", x === b); });
    applySearch(); // re-runs each wrap._applyPager() with the new page size
  });

  // Reflect the current PAGE_SIZE on the per-page control after a re-render.
  function applyPerPageState() {
    document.querySelectorAll("[data-wi-perpage]").forEach(function (seg) {
      seg.querySelectorAll("button[data-perpage]").forEach(function (b) {
        b.classList.toggle("on", parseInt(b.getAttribute("data-perpage"), 10) === PAGE_SIZE);
      });
    });
  }

  // ---- (re)initialise list-body widgets -----------------------------------

  // Re-run the per-render wiring against the current list body. Called once on
  // load and again after every HTMX swap of #wi-list-body so the freshly
  // rendered rows get pagers, the current sort order, and the active search
  // filter re-applied.
  function initListBody() {
    wirePagers();       // installs wrap._applyPager, does not run it
    applySort();
    applyPerPageState();
    applySearch();      // the ONE paging pass, over the reordered+filtered set
  }

  initListBody();

  document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e.target && e.target.id === "wi-list-body") {
      initListBody();
    }
  });
})();
