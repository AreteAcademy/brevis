/* The star count in the header's GitHub button.
   The number is never in the HTML: the generated pages are committed and
   checked, and a count that changes between builds would fail that check
   or need a commit of its own. So the browser asks GitHub, once per tab
   (sessionStorage), and the button is exactly what it was until the answer
   comes -- or stays that way, if it never does. The API allows 60 anonymous
   requests an hour per IP; the cache keeps a visitor at one. */
(function () {
  'use strict';
  var target = document.querySelector('[data-stars]');
  if (!target || !('fetch' in window)) return;

  var key = 'brevis.stars';
  var number = target.querySelector('span');

  function show(n) {
    number.textContent = n >= 1000 ? (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k' : String(n);
    target.hidden = false;
  }

  var stored = null;
  try { stored = sessionStorage.getItem(key); } catch (e) { /* private window, blocked storage: ask again */ }
  if (stored !== null && /^\d+$/.test(stored)) {
    show(Number(stored));
    return;
  }

  fetch('https://api.github.com/repos/' + target.getAttribute('data-stars'), {
    headers: { Accept: 'application/vnd.github+json' }
  }).then(function (r) {
    return r.ok ? r.json() : null;
  }).then(function (d) {
    if (!d || typeof d.stargazers_count !== 'number') return;
    try { sessionStorage.setItem(key, String(d.stargazers_count)); } catch (e) { /* same as above */ }
    show(d.stargazers_count);
  }).catch(function () { /* offline or rate-limited: the button already reads fine */ });
})();
