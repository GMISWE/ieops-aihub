// theme.js — minimal vanilla-JS theme mode control for the polyforge /ui chrome.
//
// The initial mode is rendered server-side onto <html data-theme="..."> from
// the `theme` cookie (see layout.html.tmpl + themeFromCookie in Go), and the
// active segment is marked server-side too, so there is no flash of the wrong
// theme on load. The three modes are "auto" | "light" | "dark"; for "auto" the
// CSS prefers-color-scheme media query resolves the actual colors. This script
// only handles segment clicks: it sets data-theme to the chosen mode, persists
// the choice to the cookie, and moves the active-segment class. No page reload.
(function () {
  "use strict";
  var seg = document.getElementById("pf-theme-seg");
  if (!seg) return;
  var root = document.documentElement;
  var buttons = seg.querySelectorAll("button[data-theme-mode]");

  function setCookie(name, value) {
    // 1 year, site-wide, Lax. Not HttpOnly: this is a non-sensitive UI pref the
    // client must be able to set. SameSite=Lax is enough for a GET-rendered pref.
    var maxAge = 60 * 60 * 24 * 365;
    document.cookie =
      name + "=" + value + "; path=/; max-age=" + maxAge + "; samesite=lax";
  }

  function select(mode) {
    // CSS resolves "auto" via prefers-color-scheme; "light"/"dark" force it.
    root.setAttribute("data-theme", mode);
    setCookie("theme", mode);
    for (var i = 0; i < buttons.length; i++) {
      var b = buttons[i];
      b.classList.toggle("on", b.getAttribute("data-theme-mode") === mode);
    }
  }

  seg.addEventListener("click", function (e) {
    var b = e.target.closest("button[data-theme-mode]");
    if (!b) return;
    select(b.getAttribute("data-theme-mode"));
  });
})();
