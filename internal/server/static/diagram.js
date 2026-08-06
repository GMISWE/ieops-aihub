// diagram.js — click-to-zoom for d2 diagram figures (aihub#234).
//
// Inline, a `figure.pf-diagram` is capped at the document column width (ui.css).
// That is readable for most diagrams but not for wide ones (long sequence diagrams,
// 10+ node pipelines), where fitting the column shrinks the labels. Before this
// script the only escape was browser page zoom, which enlarges the prose along with
// the figure.
//
// Clicking a figure moves its <svg> into a full-viewport overlay and sizes it in
// px: scaled up to fill the viewport when the diagram is smaller than it, kept at
// natural size (with the overlay scrolling) when it is larger. Escape, the close
// button or a backdrop click puts the node back where it came from.
//
// Why MOVE the node instead of cloning it: a d2 SVG carries ids (arrow markers,
// gradients) referenced as url(#id). A clone would duplicate every one of them,
// and duplicate ids resolve to whichever comes first in document order — a subtle
// way to render the wrong arrowheads. Moving keeps the document unambiguous, and
// the vacated figure is hidden behind the overlay anyway.
//
// Loaded on the artifact viewer via the /ui-gated head block in routes_artifacts.go,
// and on the app-shell pages via layout.html.tmpl — those do not compile d2 yet
// (aihub#231), where it is inert: no .pf-diagram, nothing marked, no listeners fire.
(function () {
  "use strict";

  var open = null; // {figure, svg, ovl, restoreFocus}

  // Natural (unscaled) size of a d2 SVG. render/diagram.go passes Scale=1 so the
  // outer <svg> carries width/height, but viewBox is checked first because it is
  // the one attribute d2 emits unconditionally; the others are fallbacks for
  // anything else that ends up inside a .pf-diagram.
  function naturalSize(svg) {
    var vb = svg.viewBox && svg.viewBox.baseVal;
    if (vb && vb.width > 0 && vb.height > 0) return { w: vb.width, h: vb.height };
    var w = parseFloat(svg.getAttribute("width"));
    var h = parseFloat(svg.getAttribute("height"));
    if (w > 0 && h > 0) return { w: w, h: h };
    var r = svg.getBoundingClientRect();
    if (r.width > 0 && r.height > 0) return { w: r.width, h: r.height };
    return null;
  }

  function sizeToViewport(svg) {
    var n = naturalSize(svg);
    if (!n) return; // unknown geometry — leave the SVG to the CSS
    // Overlay padding is var(--s6) = 24px a side, plus the figure's own
    // var(--s4) padding and 1px border. Budget generously; a few px of slack
    // only means the overlay does not scroll when it did not need to.
    var chrome = 2 * (24 + 16 + 1);
    var availW = Math.max(120, window.innerWidth - chrome);
    var availH = Math.max(120, window.innerHeight - chrome);
    // Never shrink below natural size — that is what the inline preview already
    // does. Below 1 we keep 1:1 and let the overlay scroll, which is the actual
    // "let me read the labels" case for a wide diagram.
    var scale = Math.max(1, Math.min(availW / n.w, availH / n.h));
    svg.style.width = Math.round(n.w * scale) + "px";
    svg.style.height = Math.round(n.h * scale) + "px";
  }

  function close() {
    if (!open) return;
    var o = open;
    open = null;
    o.svg.style.width = "";
    o.svg.style.height = "";
    o.figure.insertBefore(o.svg, o.figure.firstChild);
    if (o.ovl.parentNode) o.ovl.parentNode.removeChild(o.ovl);
    document.body.classList.remove("pf-diagram-zoomed");
    if (o.restoreFocus && o.restoreFocus.focus) o.restoreFocus.focus();
  }

  function openFigure(figure) {
    if (open) return;
    var svg = figure.querySelector("svg");
    if (!svg) return;

    var ovl = document.createElement("div");
    ovl.className = "pf-diagram-ovl";
    ovl.setAttribute("role", "dialog");
    ovl.setAttribute("aria-modal", "true");
    ovl.setAttribute("aria-label", "Enlarged diagram");

    // Keep the pf-diagram class: the light/dark theming in ui.css is a set of
    // `.pf-diagram svg .fill-N1`-style descendant rules, so without this ancestor
    // the zoomed diagram would show d2's baked-in light ramp on a dark page.
    var box = document.createElement("div");
    box.className = "pf-diagram";

    var closeBtn = document.createElement("button");
    closeBtn.type = "button";
    closeBtn.className = "pf-diagram-ovl-close";
    closeBtn.setAttribute("aria-label", "Close enlarged diagram");
    closeBtn.textContent = "×";

    box.appendChild(svg);
    ovl.appendChild(box);
    ovl.appendChild(closeBtn);
    document.body.appendChild(ovl);
    document.body.classList.add("pf-diagram-zoomed");

    open = { figure: figure, svg: svg, ovl: ovl, restoreFocus: figure };
    sizeToViewport(svg);

    closeBtn.addEventListener("click", close);
    // Backdrop only — a click on the diagram itself (e.g. mid-drag while
    // scrolling a wide figure) must not dismiss it.
    ovl.addEventListener("click", function (e) {
      if (e.target === ovl) close();
    });
    closeBtn.focus();
  }

  function onActivate(e) {
    var figure = e.target.closest ? e.target.closest(".pf-diagram--zoomable") : null;
    if (!figure || open) return;
    // d2 can emit links inside a diagram; let those win.
    if (e.target.closest("a")) return;
    // Do not fight the annotation UI: a click that ends a text selection is the
    // reader finishing a drag, not a request to zoom.
    var sel = window.getSelection && window.getSelection();
    if (sel && String(sel).length > 0) return;
    e.preventDefault();
    openFigure(figure);
  }

  // The "click to enlarge" hint is CSS generated content on .pf-diagram--zoomable
  // (ui.css), deliberately NOT a node appended here: annot.js anchors reviewer
  // annotations by text-quote over the visible text of #pf-doc-col, and a real text
  // node inside a figure would join that haystack and could break the anchoring of
  // any annotation spanning a diagram — silently, since a failed anchor is simply
  // dropped. ::after content never appears in Range.toString().
  function markZoomable(figure) {
    if (figure.classList.contains("pf-diagram--zoomable")) return;
    if (!figure.querySelector("svg")) return; // a d2 block that failed to compile
    figure.classList.add("pf-diagram--zoomable");
    figure.setAttribute("role", "button");
    figure.setAttribute("tabindex", "0");
    figure.setAttribute("aria-label", "Diagram — activate to enlarge");
  }

  function scan(root) {
    var figures = (root || document).querySelectorAll(".pf-diagram");
    for (var i = 0; i < figures.length; i++) {
      // Skip the overlay's own wrapper.
      if (figures[i].parentNode && figures[i].parentNode.classList &&
          figures[i].parentNode.classList.contains("pf-diagram-ovl")) continue;
      markZoomable(figures[i]);
    }
  }

  function init() {
    scan(document);
    document.addEventListener("click", onActivate);
    document.addEventListener("keydown", function (e) {
      if (e.key === "Escape" && open) {
        close();
        return;
      }
      if ((e.key === "Enter" || e.key === " ") && !open) onActivate(e);
    });
    window.addEventListener("resize", function () {
      if (open) sizeToViewport(open.svg);
    });
    // HTMX swaps parts of the app-shell pages in place; re-scan what it inserts
    // so diagrams inside a swapped fragment are zoomable too.
    document.body.addEventListener("htmx:afterSwap", function (e) {
      scan(e.target);
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
