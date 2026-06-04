// avatars.js — shared person-avatar painter for the polyforge /ui.
//
// Exposes a single global, window.pfPaintAvatars(root), that fills every
// element matching [data-av-name] inside `root` (default: document) with the
// person's initials and a stable palette color class. The two helpers below
// (initialsFor + avatarColorClass) are byte-for-byte equivalents of the Go
// versions in internal/server/ui_helpers.go, so a client-painted chip always
// gets the SAME initials + color as any server-rendered .who/.av chip (wi
// list/detail). The frozen memory handlers do not build chips server-side, so
// the memory list cards and memory-detail comment authors are painted here.
//
// Idempotent: a re-run just re-applies the same initials + color. Used both on
// initial load (DOMContentLoaded) and after HTMX swaps (callers re-invoke it on
// htmx:afterSwap for the node they swapped).
(function () {
  "use strict";

  // initialsFor mirrors ui_helpers.go initialsFor: prefer the first letter of
  // the first two whitespace words; for a single token take its first two
  // letters; skip leading non-alphanumerics ("@monte" -> "MO").
  function initialsFor(name) {
    var fields = name.trim().split(/\s+/).filter(Boolean);
    var letters = [];
    for (var i = 0; i < fields.length && letters.length < 2; i++) {
      var chars = Array.from(fields[i]);
      for (var j = 0; j < chars.length; j++) {
        if (/[\p{L}\p{N}]/u.test(chars[j])) {
          letters.push(chars[j].toUpperCase());
          break;
        }
      }
    }
    if (letters.length === 0) {
      var all = Array.from(name);
      for (var k = 0; k < all.length && letters.length < 2; k++) {
        if (/[\p{L}\p{N}]/u.test(all[k])) letters.push(all[k].toUpperCase());
      }
    } else if (letters.length === 1 && fields.length) {
      var first = Array.from(fields[0]);
      var seen = false;
      for (var m = 0; m < first.length; m++) {
        if (!/[\p{L}\p{N}]/u.test(first[m])) continue;
        if (!seen) { seen = true; continue; }
        letters.push(first[m].toUpperCase());
        break;
      }
    }
    return letters.length ? letters.join("") : "?";
  }

  // avatarColorClass mirrors ui_helpers.go avatarColorClass: FNV-1a over the
  // UTF-8 bytes of the display name, mod 8 into av-c0..av-c7. Same name -> same
  // color as every server-rendered chip.
  function avatarColorClass(name) {
    var bytes = utf8Bytes(name);
    var h = 2166136261; // FNV offset basis
    for (var i = 0; i < bytes.length; i++) {
      h ^= bytes[i];
      // FNV prime 16777619, kept in uint32 via Math.imul + >>> 0.
      h = Math.imul(h, 16777619) >>> 0;
    }
    return "av-c" + (h % 8);
  }

  function utf8Bytes(s) {
    if (typeof TextEncoder !== "undefined") return new TextEncoder().encode(s);
    return Array.from(unescape(encodeURIComponent(s))).map(function (c) {
      return c.charCodeAt(0);
    });
  }

  // Paint every [data-av-name] avatar inside root. Idempotent.
  function paintAvatars(root) {
    var scope = root || document;
    scope.querySelectorAll(".av[data-av-name]").forEach(function (av) {
      var name = av.getAttribute("data-av-name") || "";
      if (!name) return;
      av.textContent = initialsFor(name);
      // Drop any previously applied palette class before re-applying.
      av.className = av.className.replace(/\bav-c[0-7]\b/g, "").trim();
      av.classList.add(avatarColorClass(name));
    });
  }

  // Expose the painter; expose the helpers too in case a caller needs to build
  // a chip imperatively.
  window.pfPaintAvatars = paintAvatars;
  window.pfInitialsFor = initialsFor;
  window.pfAvatarColorClass = avatarColorClass;

  function run() { paintAvatars(document); }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", run);
  } else {
    run();
  }
})();
