// dropdown.js — custom dropdowns, segmented filters, and client-side search for
// the polyforge /ui work-item list.
//
// Filtering is HTMX-driven and IN PLACE. The custom dropdowns and the
// All/Mine segmented control DO NOT navigate: they write their value into the
// shared filter <form> (the project switcher into a hidden field; the status
// multi-select into one hidden <input name="status"> per checked box) and then
// fire a single `pf-filter` event on the form. The form carries hx-get +
// hx-target="#wi-list-body" + hx-include="this", so each request swaps just the
// list body while carrying the COMPLETE current param set. This is what makes a
// status toggle auto-apply (problem #2) and a project switch preserve the status
// filter (problem #1) — the statuses ride along as form inputs on every request.
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

  // Fire the form's hx-get in place. Debounced so a burst of checkbox toggles
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

  // Rebuild the per-status hidden inputs inside [data-status-params] so the
  // form carries exactly one <input name="status"> per currently-checked box.
  // This is the multi-status param set that travels with EVERY request.
  function syncStatusParams(values) {
    var box = document.querySelector("[data-status-params]");
    if (!box) return;
    box.textContent = "";
    values.forEach(function (v) {
      var inp = document.createElement("input");
      inp.type = "hidden";
      inp.name = "status";
      inp.value = v;
      box.appendChild(inp);
    });
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

  // Single-select dropdown (the project switcher): choosing an item writes the
  // hidden field and fires the shared in-place filter request. Because the
  // status params already live on the form, switching project preserves them.
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

  // Multi-select dropdown (status): checkbox items toggle in place and the menu
  // STAYS OPEN. Each toggle rebuilds the hidden status params and fires the
  // in-place request (debounced) — no navigation, no apply-on-close. The
  // selection is mirrored to localStorage as a secondary fallback for arrivals
  // that carry no ?status= in the URL.
  function wireMultiDropdown(dd) {
    var field = dd.dataset.ddMulti;
    var btn = dd.querySelector("[data-dd-btn]");
    var menu = dd.querySelector(".dd-menu");
    var lbl = dd.querySelector(".lbl");
    if (!field || !btn || !menu) return;

    var storeKey = "pf.wi-list." + field;

    function selected() {
      var out = [];
      menu.querySelectorAll("[data-dd-multi-value]").forEach(function (it) {
        if (it.classList.contains("on")) out.push(it.dataset.ddMultiValue);
      });
      return out;
    }

    function relabel() {
      var sel = selected();
      if (!lbl) return;
      if (sel.length === 0) lbl.textContent = "All status";
      else if (sel.length === 1) {
        lbl.textContent = sel[0].charAt(0).toUpperCase() + sel[0].slice(1);
      } else lbl.textContent = sel.length + " selected";
    }

    // Push the current selection into the form (hidden status inputs) and fire
    // the in-place request. Also persist to localStorage as the fallback.
    function apply(delay) {
      var sel = selected();
      try { localStorage.setItem(storeKey, JSON.stringify(sel)); } catch (e) {}
      syncStatusParams(sel);
      relabel();
      fireFilter(delay);
    }

    // Restore persisted selection when the URL carries no explicit status
    // (e.g. arriving via an internal link). The server is the source of truth
    // when ?status= is present, so we only override when it is absent — and in
    // that case we push the restored set into the form WITHOUT firing a request
    // (the page already rendered; no need to reload on load).
    (function restore() {
      var url = new URL(window.location.href);
      if (url.searchParams.has("status")) {
        try { localStorage.setItem(storeKey, JSON.stringify(selected())); } catch (e) {}
        syncStatusParams(selected());
        return;
      }
      var saved;
      try { saved = JSON.parse(localStorage.getItem(storeKey) || "[]"); } catch (e) { saved = []; }
      if (!saved || !saved.length) return;
      var want = {};
      saved.forEach(function (v) { want[v] = true; });
      menu.querySelectorAll("[data-dd-multi-value]").forEach(function (it) {
        it.classList.toggle("on", !!want[it.dataset.ddMultiValue]);
      });
      var clear = menu.querySelector("[data-dd-multi-clear]");
      if (clear) clear.classList.toggle("on", saved.length === 0);
      // Sync the hidden status params so the NEXT in-place request (the user's
      // next filter action — a project switch, a segment, a further toggle)
      // already carries the restored selection. We deliberately do NOT fire a
      // request here: the page already rendered, and auto-firing on load would
      // be a redundant round-trip / flicker. localStorage is a secondary
      // fallback — explicit user actions are the primary path.
      syncStatusParams(selected());
      relabel();
    })();

    btn.addEventListener("click", function (e) {
      e.stopPropagation();
      var willOpen = menu.hidden;
      closeAllMenus(menu);
      menu.hidden = !willOpen;
    });

    menu.querySelectorAll(".dd-it").forEach(function (it) {
      it.addEventListener("click", function (e) {
        e.stopPropagation(); // stay open while toggling
        // A drag that ends on the same item fires a click — ignore it so a
        // reorder never accidentally toggles the checkbox.
        if (dd._didDrag) {
          dd._didDrag = false;
          return;
        }
        if (it.hasAttribute("data-dd-multi-clear")) {
          menu.querySelectorAll("[data-dd-multi-value]").forEach(function (x) {
            x.classList.remove("on");
          });
          it.classList.add("on");
        } else {
          it.classList.toggle("on");
          var clear = menu.querySelector("[data-dd-multi-clear]");
          if (clear) clear.classList.toggle("on", selected().length === 0);
        }
        // Live update — debounce so rapid toggles collapse into one request,
        // but the menu stays open throughout.
        apply(200);
      });
    });

    wireSortable(dd, menu, field);
  }

  // wireSortable makes the draggable status rows reorderable via the native
  // HTML5 drag-and-drop API (no external library). The resulting order is
  // persisted to localStorage as pf.wi-list.<field>-order and drives the
  // display order of the status group sections in the list (round-3 #3). It is
  // applied on the next render by applyStatusGroupOrder below; we do NOT reload
  // on drop, so reordering is cheap and does not disturb the current filter.
  function wireSortable(dd, menu, field) {
    var box = menu.querySelector("[data-dd-sortable]");
    if (!box) return;
    var orderKey = "pf.wi-list." + field + "-order";
    var dragEl = null;

    function persist() {
      var order = [];
      box.querySelectorAll("[data-dd-multi-value]").forEach(function (it) {
        order.push(it.dataset.ddMultiValue);
      });
      try { localStorage.setItem(orderKey, JSON.stringify(order)); } catch (e) {}
    }

    // Return the row that a drop at clientY should land before (or null = end).
    function afterEl(y) {
      var rows = Array.prototype.slice.call(box.querySelectorAll("[data-dd-multi-value]"));
      for (var i = 0; i < rows.length; i++) {
        var box2 = rows[i].getBoundingClientRect();
        if (y < box2.top + box2.height / 2) return rows[i];
      }
      return null;
    }

    box.querySelectorAll("[data-dd-multi-value]").forEach(function (it) {
      it.addEventListener("dragstart", function (e) {
        dragEl = it;
        dd._didDrag = true;
        it.classList.add("dragging");
        if (e.dataTransfer) {
          e.dataTransfer.effectAllowed = "move";
          // Firefox requires data to be set for the drag to start.
          try { e.dataTransfer.setData("text/plain", it.dataset.ddMultiValue); } catch (err) {}
        }
      });
      it.addEventListener("dragend", function () {
        if (dragEl) dragEl.classList.remove("dragging");
        dragEl = null;
        persist();
        // Re-apply the status group order to the (current) list in place.
        applyStatusGroupOrder();
      });
    });

    box.addEventListener("dragover", function (e) {
      e.preventDefault(); // allow drop
      if (!dragEl) return;
      if (e.dataTransfer) e.dataTransfer.dropEffect = "move";
      var ref = afterEl(e.clientY);
      if (ref === dragEl) return;
      if (ref) box.insertBefore(dragEl, ref);
      else box.appendChild(dragEl);
    });

    box.addEventListener("drop", function (e) {
      e.preventDefault();
      e.stopPropagation();
    });
  }

  document.querySelectorAll("[data-dd]").forEach(function (dd) {
    if (dd.dataset.ddMulti) wireMultiDropdown(dd);
    else wireDropdown(dd);
  });
  document.addEventListener("click", function () {
    closeAllMenus(null);
  });

  // ---- segmented control (All / Mine / Watching) --------------------------

  document.querySelectorAll("[data-seg]").forEach(function (seg) {
    var input = document.querySelector('[data-seg-input="owner"]');
    var allInput = document.querySelector('[data-seg-input="all"]');
    seg.querySelectorAll("button").forEach(function (b) {
      if (b.disabled) return; // Watching is inert — no backend relationship.
      b.addEventListener("click", function () {
        if (!input) return;
        seg.querySelectorAll("button").forEach(function (x) {
          x.classList.toggle("on", x === b);
        });
        input.value = b.dataset.segOwner || "";
        // The "All" segment carries data-seg-all="1" so the handler does not
        // re-default the empty owner filter back to Mine.
        if (allInput) allInput.value = b.dataset.segAll === "1" ? "1" : "";
        fireFilter(0);
      });
    });
  });

  // ---- per-section pagination ---------------------------------------------

  // Each .grp-wrap paginates its rows independently: at most PAGE_SIZE rows are
  // visible at a time, with a per-block pager (‹ prev, numbered pages, › next).
  // Blocks are fully independent of one another (own current-page state). All
  // rows are already in the DOM (server renders the full block); this is pure
  // client-side show/hide. The header still prints the TOTAL count so paged-out
  // rows are never invisible. To change the page size, edit PAGE_SIZE.
  var PAGE_SIZE = 5;

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

    // Pool of empty filler rows used to pad a short page (e.g. the last page)
    // up to PAGE_SIZE slots, so the block height is constant across pages and
    // the pager never jumps. At most PAGE_SIZE-1 fillers are ever needed.
    var fillers = [];
    (function ensureFillers() {
      while (fillers.length < PAGE_SIZE - 1) {
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
    })();

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
      if (pages <= 1) {
        // One page (or fewer): show every matched row, no pager, no padding.
        rs.forEach(function (el) { el.hidden = false; });
        hideFillers();
        pager.hidden = true;
        pager.textContent = "";
        wrap._page = 1;
        return;
      }
      pager.hidden = false;
      if (wrap._page > pages) wrap._page = 1;
      go(wrap._page, rs, pages);
    };

    wrap._applyPager();
  }

  function wirePagers() {
    document.querySelectorAll("[data-wi-list] [data-grp]").forEach(wirePager);
  }

  // ---- client-side search --------------------------------------------------

  // Apply the current search text to the rendered rows. Factored out so it can
  // re-run after an HTMX swap replaces the list body (the search box itself
  // lives outside #wi-list-body and survives the swap, so its value persists).
  function applySearch() {
    var search = document.querySelector("[data-wi-search]");
    if (!search) return;
    var q = search.value.trim().toLowerCase();
    document.querySelectorAll("[data-wi-list] [data-grp]").forEach(function (wrap) {
      var rowsBox = wrap.querySelector("[data-grp-rows]");
      if (!rowsBox) return;
      var anyVisible = false;
      Array.prototype.forEach.call(rowsBox.children, function (el) {
        if (!el.hasAttribute("data-wi-row")) return;
        var hay = (el.getAttribute("data-wi-text") || "").toLowerCase();
        var miss = q !== "" && hay.indexOf(q) === -1;
        // Mark search misses so the pager skips them when capping.
        if (miss) el.dataset.searchHidden = "1";
        else delete el.dataset.searchHidden;
        if (!miss) anyVisible = true;
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

  // ---- status group reordering (round-3 #3) -------------------------------

  // The status filter dropdown persists a drag order under pf.wi-list.status-order.
  // Apply that order to the "status"-kind blocks in the list (Model A) so the
  // rendered blocks follow the user's chosen order. The smart sections "Needs
  // you" (Kind "personal", pinned first) and "Unclaimed" (Kind "pool", pinned
  // last) are never matched here, so they keep their fixed top/bottom position.
  //
  // The server re-renders the list body in the canonical status order on every
  // HTMX swap, so this MUST run again after each swap (see initListBody) to
  // re-impose the saved order on the freshly rendered blocks.
  function applyStatusGroupOrder() {
    var list = document.querySelector("[data-wi-list]");
    if (!list) return;
    var order;
    try {
      order = JSON.parse(localStorage.getItem("pf.wi-list.status-order") || "[]");
    } catch (e) {
      order = [];
    }
    // No saved order -> leave the server's canonical order untouched.
    if (!order || !order.length) return;

    var statusGroups = Array.prototype.filter.call(
      list.querySelectorAll("[data-grp]"),
      function (g) {
        return g.dataset.grpKind === "status";
      }
    );
    if (statusGroups.length < 2) return; // nothing to reorder

    // Place a stable marker where the first status block currently sits, then
    // reinsert the status blocks in the persisted order at that marker. The
    // marker keeps a fixed boundary so the smart sections before (Needs you)
    // and after (Unclaimed) stay put. Statuses absent from the saved order keep
    // their relative order AFTER the explicitly-ordered ones.
    var marker = document.createComment("status-order");
    list.insertBefore(marker, statusGroups[0]);
    var rank = {};
    order.forEach(function (s, i) {
      rank[s] = i;
    });
    var sorted = statusGroups.slice().sort(function (a, b) {
      var ra = rank[a.dataset.grpStatus];
      var rb = rank[b.dataset.grpStatus];
      if (ra === undefined) ra = Infinity;
      if (rb === undefined) rb = Infinity;
      return ra - rb;
    });
    sorted.forEach(function (g) {
      list.insertBefore(g, marker);
    });
    marker.remove();
  }

  // ---- (re)initialise list-body widgets -----------------------------------

  // Re-run the per-render wiring against the current list body. Called once on
  // load and again after every HTMX swap of #wi-list-body so the freshly
  // rendered rows get pagers, the persisted status-group order, and the active
  // search filter re-applied.
  function initListBody() {
    wirePagers();
    applyStatusGroupOrder();
    applySearch();
  }

  initListBody();

  document.body.addEventListener("htmx:afterSwap", function (e) {
    if (e.target && e.target.id === "wi-list-body") {
      initListBody();
    }
  });
})();
