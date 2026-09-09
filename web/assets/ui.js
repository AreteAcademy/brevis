// The SSR pages' interactions. A small file, loaded on every page, with two
// responsibilities: opening the error dialogs and giving the charts life.
//
// Event delegation rather than one listener per element: the tables are
// re-rendered by the server on every navigation, and listeners bound to specific
// rows would die along with them.
(function () {
  "use strict";

  // --- Dialogos ------------------------------------------------------------
  document.addEventListener("click", function (e) {
    if (!e.target || typeof e.target.closest !== "function") return;
    var trigger = e.target.closest("[data-dialogo]");
    if (trigger) {
      var d = document.getElementById(trigger.dataset.dialogo);
      if (d && typeof d.showModal === "function") d.showModal();
      return;
    }
    // A click on the backdrop closes it. A <dialog> does not tell the backdrop
    // from its content on its own: the click's target IS the dialog itself when
    // the frame is hit.
    if (e.target.tagName === "DIALOG") e.target.close();
  });

  // Opening by link: /runs#erro-<id> arrives with the dialog already open (the
  // anchor keeps its Portuguese name, like data-dica: the template writes it).
  // It
  // serves for sending somebody the exact failure instead of "open the list and
  // look".
  function openFromHash() {
    if (!location.hash) return;
    var d = document.getElementById(location.hash.slice(1));
    if (d && d.tagName === "DIALOG" && !d.open && typeof d.showModal === "function") {
      d.showModal();
    }
  }
  openFromHash();
  window.addEventListener("hashchange", openFromHash);

  // --- The charts' tooltip --------------------------------------------------
  //
  // The SVG's <title> does show the value, but only after a second of hovering
  // and with the operating system's appearance. Here the tooltip appears at
  // once, follows the cursor and uses the same typography as the rest of the
  // page.
  //
  // `grafico-dica` and `data-dica` stay Portuguese: the class is in
  // app.src.css and the attribute is written by the templates, and the three
  // only move together. See the note in dag.js.
  var tip = null;

  function show(text, x, y) {
    if (!tip) {
      tip = document.createElement("div");
      tip.className = "grafico-dica";
      document.body.appendChild(tip);
    }
    tip.textContent = text;
    tip.style.display = "block";
    // It sits above and to the right of the cursor, flipping to the other side
    // when it meets the edge -- otherwise the tooltip leaves the screen on the
    // last columns.
    var width = tip.offsetWidth;
    var left = x + 14;
    if (left + width > window.innerWidth - 8) left = x - width - 14;
    tip.style.left = left + "px";
    tip.style.top = y - tip.offsetHeight - 12 + "px";
  }

  function hide() {
    if (tip) tip.style.display = "none";
  }

  document.addEventListener("mousemove", function (e) {
    // `closest` does not exist on every possible event target (the document
    // itself, for instance). Without the guard, a mousemove outside any element
    // throws a TypeError and kills the listener for the rest of the session.
    if (!e.target || typeof e.target.closest !== "function") return;
    var target = e.target.closest("[data-dica]");
    if (target) show(target.dataset.dica, e.clientX, e.clientY);
    else hide();
  });

  // Scrolling with the tooltip open would leave it floating over another point
  // of the chart.
  window.addEventListener("scroll", hide, { passive: true });
  document.addEventListener("mouseleave", hide);

  // ------------------------------------------------------------------
  // The account menu
  //
  // It is a <details>, so the browser already gives the open state, the
  // keyboard, the focus order and the ARIA. These are the two things it does
  // not give: Escape, and closing when the click lands somewhere else.
  //
  // Both are delegated, like everything else here -- the sidebar is
  // re-rendered by the server on every navigation, and a listener bound to
  // the element would die with it.
  // ------------------------------------------------------------------

  document.addEventListener("click", function (e) {
    document.querySelectorAll("details.account-menu[open]").forEach(function (d) {
      if (!d.contains(e.target)) d.removeAttribute("open");
    });
  });

  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    document.querySelectorAll("details.account-menu[open]").forEach(function (d) {
      d.removeAttribute("open");
      // The focus goes back to the button that opened it, which is what a
      // keyboard user expects and what the browser does not do on its own.
      var summary = d.querySelector("summary");
      if (summary) summary.focus();
    });
  });

})();
