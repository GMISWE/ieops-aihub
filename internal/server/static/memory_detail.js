// memory_detail.js — inline comment editing + in-place thread mutation glue
// for the polyforge /ui/memories/:id detail page.
//
// The frozen commit endpoints (add / edit / delete / reply / resolve) all
// 303-redirect to the full detail page. The thread is wrapped in #mem-comments
// and every mutating <form> carries hx-post + hx-target=#mem-comments +
// hx-select=#mem-comments + hx-swap=outerHTML, so HTMX follows the redirect,
// pulls #mem-comments out of the returned full document, and swaps it in place
// — no jarring full-page reload. Without JS the same plain <form action=...>
// POSTs and the browser follows the 303 to a normal reload (fallback intact).
//
// This file only handles the CLIENT toggles that have no server round-trip:
//   - Edit: hide the rendered .body, reveal the inline edit <form> (pre-filled
//     with the original text), focus the textarea. Cancel restores the view.
//   - Reply: reveal the inline reply <form>; Cancel hides it.
// And it re-paints avatars after every #mem-comments swap.
(function () {
  "use strict";

  // Toggle a single comment between its rendered body and the inline edit form.
  function openEdit(note) {
    var view = note.querySelector("[data-cmt-view]");
    var form = note.querySelector("[data-cmt-edit]");
    if (!form) return; // no edit affordance for this comment (not author/admin)
    if (view) view.hidden = true;
    form.hidden = false;
    var ta = form.querySelector("textarea");
    if (ta) { ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length); }
  }
  function closeEdit(note) {
    var view = note.querySelector("[data-cmt-view]");
    var form = note.querySelector("[data-cmt-edit]");
    if (form) form.hidden = true;
    if (view) view.hidden = false;
  }
  function openReply(note) {
    var form = note.querySelector("[data-cmt-reply]");
    if (!form) return;
    form.hidden = false;
    var ta = form.querySelector("textarea");
    if (ta) ta.focus();
  }
  function closeReply(note) {
    var form = note.querySelector("[data-cmt-reply]");
    if (form) { form.hidden = true; var ta = form.querySelector("textarea"); if (ta) ta.value = ""; }
  }

  function noteOf(el) { return el.closest(".note"); }

  // Delegated click handler — survives #mem-comments swaps because it is bound
  // once on the container's parent (document.body) rather than per-button.
  function onClick(e) {
    var t = e.target;
    if (t.closest("[data-cmt-editbtn]")) { var n = noteOf(t); if (n) openEdit(n); return; }
    if (t.closest("[data-cmt-cancel]")) { var n2 = noteOf(t); if (n2) closeEdit(n2); return; }
    if (t.closest("[data-cmt-replybtn]")) { var n3 = noteOf(t); if (n3) openReply(n3); return; }
    if (t.closest("[data-cmt-replycancel]")) { var n4 = noteOf(t); if (n4) closeReply(n4); return; }
    if (t.closest("[data-cmt-delbtn]")) { var n5 = noteOf(t); var d = n5 && n5.querySelector("[data-cmt-confirm]"); if (d && d.showModal) d.showModal(); return; }
    if (t.closest("[data-cmt-delcancel]")) { var n6 = noteOf(t); var dc = n6 && n6.querySelector("[data-cmt-confirm]"); if (dc && dc.close) dc.close(); return; }
  }

  function paint() {
    var root = document.getElementById("mem-comments");
    if (window.pfPaintAvatars) window.pfPaintAvatars(root || document);
  }

  function init() {
    document.body.addEventListener("click", onClick);
    paint();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }

  // After any in-place mutation swaps #mem-comments, re-paint the freshly
  // rendered author / reply chips.
  document.body.addEventListener("htmx:afterSwap", function (e) {
    var t = e.target;
    if (t && (t.id === "mem-comments" || (t.querySelector && t.querySelector("#mem-comments")))) {
      paint();
    }
  });
})();
