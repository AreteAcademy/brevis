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


  // ------------------------------------------------------------------
  // Live regions
  //
  // An element with data-live="<url>" is refreshed from that URL. The response
  // is HTML holding one or more [data-live-target="<id>"] blocks, and each one
  // replaces the element with that id.
  //
  // HTML and not JSON, for the reason the endpoint gives: rendering a step's
  // state a second time here would be a second place that has to know what
  // colour `retrying` is.
  //
  // The DAG polls on its own and this does not change that. What it fixes is
  // everything AROUND the drawing: the status pill said `queued` while the
  // graph showed a step turning green, and the output below never moved until
  // somebody pressed F5.
  // ------------------------------------------------------------------

  var LIVE_RUNNING = 2000; // the graph's own cadence, so the page moves together
  var LIVE_FAILED = 10000; // failed is not terminal -- a retry is coming, slowly

  document.querySelectorAll("[data-live]").forEach(function (host) {
    var url = host.dataset.live;
    if (!url) return;
    var timer = null;
    var last = "";

    // The indicator. It is only ever SHOWN by the script: with JavaScript off
    // the page is static and saying "following this run" would be a lie.
    var indicator = document.getElementById("live-indicator");
    function say(text, live) {
      if (!indicator) return;
      indicator.classList.remove("hidden");
      indicator.classList.add("flex");
      var label = indicator.querySelector("[data-live-label]");
      if (label) label.textContent = text;
      var dot = indicator.querySelector("span");
      if (dot) dot.className = "h-1.5 w-1.5 rounded-full " +
        (live ? "bg-state-running" : "bg-line-strong");
    }

    function schedule(ms) {
      if (!ms) return;
      timer = setTimeout(tick, ms);
    }

    function tick() {
      // A hidden tab is a tab nobody is reading. Polling one for an afternoon
      // is traffic for a screen behind another window; the visibility listener
      // below catches up the moment it comes back.
      if (document.hidden) {
        schedule(LIVE_RUNNING);
        return;
      }
      fetch(url, { headers: { Accept: "text/html" } })
        .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.text();
        })
        .then(function (html) {
          // Nothing changed: skip the swap entirely. Replacing identical HTML
          // still resets a text selection and interrupts a screen reader, and
          // most polls of a running step change nothing.
          if (html === last) {
            schedule(LIVE_RUNNING);
            return;
          }

          // An OPEN dialog is somebody reading an error. Replacing the markup
          // under it closes it mid-sentence, so the refresh waits -- the run
          // has not gone anywhere.
          if (document.querySelector("dialog[open]")) {
            schedule(LIVE_RUNNING);
            return;
          }

          last = html;
          var doc = new DOMParser().parseFromString(html, "text/html");
          var terminal = false;
          var failed = false;

          doc.querySelectorAll("[data-live-target]").forEach(function (block) {
            var target = document.getElementById(block.dataset.liveTarget);
            if (target) target.innerHTML = block.innerHTML;
            if (block.dataset.terminal === "true") terminal = true;
            if (block.textContent.indexOf("failed") >= 0) failed = true;
          });

          // Terminal stops the polling for good. The run is over and nothing
          // about it will change again -- and the indicator says so rather
          // than just disappearing, which would look like a failure.
          if (terminal) {
            say("this run is finished", false);
            return;
          }
          schedule(failed ? LIVE_FAILED : LIVE_RUNNING);
        })
        .catch(function () {
          // A refresh that fails is not worth a message on the screen: the
          // page still shows what it showed, and the next tick may well work.
          // It slows down, so a server that is down is not hammered.
          schedule(LIVE_FAILED);
        });
    }

    document.addEventListener("visibilitychange", function () {
      if (!document.hidden && timer) {
        clearTimeout(timer);
        tick();
      }
    });

    say("following this run", true);
    schedule(LIVE_RUNNING);
  });

})();
