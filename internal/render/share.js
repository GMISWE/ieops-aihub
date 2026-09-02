// share.js — polyforge artifact viewer share control (aihub#154)
// Vanilla JS, no deps, IIFE. Loaded defer on the /ui artifact viewer only.
// Toggles public sharing of the current spec/plan/review artifact via the
// /ui share/unshare endpoints, copies the share link to the clipboard, and
// shows an inline toast. No framework, native DOM API only. Does NOT depend
// on annot.js / annotator.js.
(function () {
  'use strict';

  var root = document.getElementById('pf-share');
  if (!root) return;

  var memId = root.dataset.memId || '';
  var btn   = root.querySelector('[data-pf-share-btn]');
  var link  = root.querySelector('[data-pf-share-link]');
  var input = link ? link.querySelector('input') : null;
  var copy  = root.querySelector('[data-pf-share-copy]');
  var toast = root.querySelector('[data-pf-share-toast]');
  if (!btn || !memId) return;

  var _toastTimer = null;

  function showToast(msg) {
    if (!toast) return;
    toast.textContent = msg;
    // Visibility is driven entirely by viewer.css via the --show class; the JS
    // never writes inline style so the stylesheet keeps full control of looks.
    toast.classList.add('pf-share-toast--show');
    if (_toastTimer) clearTimeout(_toastTimer);
    _toastTimer = setTimeout(function () {
      toast.textContent = '';
      toast.classList.remove('pf-share-toast--show');
    }, 2500);
  }

  function isShared() {
    return root.dataset.shared === 'true';
  }

  function setShared(shared, shareURL) {
    root.dataset.shared = shared ? 'true' : 'false';
    btn.textContent = shared ? 'Stop sharing' : 'Share';
    if (!link) return;
    if (shared) {
      if (input && typeof shareURL === 'string' && shareURL) input.value = shareURL;
      link.hidden = false;
    } else {
      link.hidden = true;
      if (input) input.value = '';
    }
  }

  function copyToClipboard(text) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
      return navigator.clipboard.writeText(text);
    }
    return Promise.reject(new Error('no clipboard'));
  }

  // aihub#151: these are FALLBACKS, not the message. They used to be the whole
  // answer, and both were wrong the moment the server grew more reasons: 403 now
  // covers "not a shareable artifact type" and "visibility is narrower than the
  // project" as well as "you need writer access", and the 412 text named three
  // types when six render by default and the set is configurable
  // (RENDER_MEMORY_TYPES). A canned client-side string cannot track that, so
  // showError prefers the server's own message and only falls back to these when
  // the body is missing or unparseable.
  function errFallback(status) {
    if (status === 403) return 'Not allowed to share this artifact';
    if (status === 412) return 'This artifact has no rendered HTML to share';
    return 'Share failed (' + status + ')';
  }

  function showError(res) {
    res.json().then(function (data) {
      showToast((data && data.message) || errFallback(res.status));
    }).catch(function () {
      showToast(errFallback(res.status));
    });
  }

  function doShare() {
    fetch('/ui/artifacts/' + encodeURIComponent(memId) + '/share', { method: 'POST' })
      .then(function (res) {
        if (!res.ok) { showError(res); return; }
        return res.json().then(function (data) {
          var url = (data && data.share_url) || '';
          setShared(true, url);
          copyToClipboard(url).then(function () {
            showToast('Link copied to clipboard');
          }).catch(function () {
            showToast('Share link ready — copy below');
          });
        });
      })
      .catch(function () { showToast('Network error'); });
  }

  function doUnshare() {
    fetch('/ui/artifacts/' + encodeURIComponent(memId) + '/share', { method: 'DELETE' })
      .then(function (res) {
        if (!res.ok) { showError(res); return; }
        setShared(false, '');
        showToast('Sharing stopped');
      })
      .catch(function () { showToast('Network error'); });
  }

  btn.addEventListener('click', function () {
    try {
      if (isShared()) { doUnshare(); } else { doShare(); }
    } catch (e) {
      showToast('Network error');
    }
  });

  if (copy) {
    copy.addEventListener('click', function () {
      if (!input) return;
      try {
        copyToClipboard(input.value).then(function () {
          showToast('Copied');
        }).catch(function () {
          input.select();
        });
      } catch (e) {
        try { input.select(); } catch (e2) { /* noop */ }
      }
    });
  }
})();
