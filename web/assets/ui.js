// The SSR pages' interactions. A small file, loaded on every page, with two
// responsibilities: opening the error dialogs and giving the charts life.
//
// Event delegation rather than one listener per element: the tables are
// re-rendered by the server on every navigation, and listeners bound to specific
// rows would die along with them.
(function () {
  "use strict";

  // --- The page's sentences -------------------------------------------------
  //
  // What this file writes in words comes from the page: a JSON block the
  // server wrote in the reader's language (i18n.ScriptKeys). A key it lacks
  // shows as itself, which a test would catch first.
  var messages = null;
  function t(key, vars) {
    if (!messages) {
      try {
        messages = JSON.parse((document.getElementById("i18n") || {}).textContent || "{}");
      } catch (e) {
        messages = {};
      }
    }
    var text = messages[key] || "[" + key + "]";
    for (var name in vars || {}) text = text.split("{" + name + "}").join(vars[name]);
    return text;
  }

  // --- Dialogos ------------------------------------------------------------
  document.addEventListener("click", function (e) {
    if (!e.target || typeof e.target.closest !== "function") return;
    var trigger = e.target.closest("[data-dialogo]");
    if (trigger) {
      var d = document.getElementById(trigger.dataset.dialogo);
      if (d && typeof d.showModal === "function") {
        // A link opener (`href="#run"`) is the no-script path; with the
        // script the dialog opens and the URL keeps no hash.
        if (trigger.tagName === "A" || trigger.type === "submit") e.preventDefault();
        if (!d.open) {
          opener.set(d, trigger);
          d.showModal();
          // A Drawer or a Modal opens on its first field -- where the reader
          // starts -- and not on the close button, which comes first in the
          // markup and which showModal() would otherwise pick.
          var first = d.querySelector(
            "[data-dialog-body] :is(input, select, textarea, button, a[href], [tabindex]):not([disabled]):not([tabindex='-1'])"
          );
          if (first) first.focus();
        }
      }
      return;
    }
    // A click on the backdrop closes it. A <dialog> does not tell the backdrop
    // from its content on its own: the click's target IS the dialog itself when
    // the frame is hit.
    if (e.target.tagName === "DIALOG") e.target.close();
  });

  // THE FOCUS GOES BACK TO WHAT OPENED THE DIALOG. Browsers that implement
  // the spec's "previously focused element" do it already; this is for the
  // ones that leave the focus on <body>, where a keyboard user starts over
  // from the top of the page. `close` does not bubble, hence the capture.
  var opener = new WeakMap();
  document.addEventListener(
    "close",
    function (e) {
      var d = e.target;
      if (!d || d.tagName !== "DIALOG") return;
      var from = opener.get(d);
      opener.delete(d);
      if (from && from.isConnected && (document.activeElement === document.body || d.contains(document.activeElement))) {
        from.focus();
      }
    },
    true
  );

  // --- Toasts ------------------------------------------------------------------
  //
  // What just happened, in the corner, for 4.2s. The server draws a flash into
  // #toasts with the page (layouts.SetFlash); a screen's own script calls
  // Brevis.toast. The region is role="status", so either is announced.
  var TOAST_MS = 4200;

  function dismiss(t) {
    setTimeout(function () {
      t.classList.add("toast-leaving");
      setTimeout(function () {
        t.remove();
      }, 220);
    }, TOAST_MS);
  }
  document.querySelectorAll("#toasts .toast").forEach(dismiss);

  window.Brevis = window.Brevis || {};
  // Brevis.toast(message, {kind: "ok"|"fail"|"run", action: "Undo", onAction: fn})
  window.Brevis.toast = function (message, opts) {
    opts = opts || {};
    var region = document.getElementById("toasts");
    if (!region) return;
    var shape = { fail: "st-diamond text-state-failed", run: "st-spin text-state-running" }[opts.kind] || "st-dot text-state-success";
    var t = document.createElement("div");
    t.className = "toast";
    var mark = document.createElement("span");
    mark.className = "st st-mark " + shape;
    mark.setAttribute("aria-hidden", "true");
    var text = document.createElement("span");
    text.className = "min-w-0 flex-1";
    text.textContent = message;
    t.appendChild(mark);
    t.appendChild(text);
    if (opts.action && typeof opts.onAction === "function") {
      var b = document.createElement("button");
      b.type = "button";
      b.className = "toast-action";
      b.textContent = opts.action;
      b.addEventListener("click", function () {
        opts.onAction();
        t.remove();
      });
      t.appendChild(b);
    }
    region.appendChild(t);
    dismiss(t);
  };

  // --- The sidebar, full or short -------------------------------------------
  //
  // Short is the icon rail. The choice is the visitor's, kept in localStorage
  // and applied by the head script before the first paint; this flips it in
  // place. Below 860px the sidebar is a drawer and always full (the CSS only
  // folds it above that).
  function drawSidebar() {
    var short = folded();
    document.querySelectorAll("[data-nav-fold]").forEach(function (b) {
      var label = short ? b.dataset.labelExpand : b.dataset.labelCollapse;
      b.setAttribute("aria-expanded", short ? "false" : "true");
      b.setAttribute("aria-label", label);
      b.dataset.name = b.dataset.labelExpand;
    });
  }
  drawSidebar();
  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("[data-nav-fold]");
    if (!b) return;
    var next = folded() ? "full" : "short";
    document.documentElement.dataset.navMode = next;
    remember("brevis_sidebar", next);
    hideHint();
    drawSidebar();
  });

  // --- A new Brevis ----------------------------------------------------------
  //
  // The page draws [data-update-notice] when a release newer than this one is
  // out. Told once per version: localStorage remembers the one already shown,
  // so the same news is not a toast on every screen. The words come from the
  // server, in the reader's language.
  (function () {
    var n = document.querySelector("[data-update-notice]");
    if (!n || typeof window.Brevis.toast !== "function") return;
    var v = n.dataset.version, seen = "";
    try { seen = localStorage.getItem("brevis_update_seen") || ""; } catch (e) { /* a private window */ }
    if (seen === v) return;
    try { localStorage.setItem("brevis_update_seen", v); } catch (e) { /* the toast shows again next page */ }
    window.Brevis.toast(n.dataset.message, {
      kind: "run",
      action: n.dataset.action,
      onAction: function () {
        var d = document.getElementById("version");
        if (d && typeof d.showModal === "function") d.showModal();
      }
    });
  })();

  // --- Switches --------------------------------------------------------------
  //
  // components.Switch flips its own aria-checked; one marked data-manual is
  // left to the screen's script, which knows whether the server agreed. Only
  // the component's class: the older switches each carry their own handler.
  document.addEventListener("click", function (e) {
    var s = e.target && typeof e.target.closest === "function" ? e.target.closest('button.switch[role="switch"]') : null;
    if (!s || s.hasAttribute("data-manual")) return;
    s.setAttribute("aria-checked", s.getAttribute("aria-checked") === "true" ? "false" : "true");
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

  // --- The calendar starts at this week ------------------------------------
  //
  // A year of squares is wider than a phone. Where it scrolls, it opens at
  // its end -- this week -- rather than at a year ago: the recent days are
  // the ones a reader came for.
  document.querySelectorAll("svg[data-calendar]").forEach(function (svg) {
    var box = svg.parentElement;
    if (box && box.scrollWidth > box.clientWidth) box.scrollLeft = box.scrollWidth;
  });

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

  // --- Hints (`[data-tip]`) ---------------------------------------------------
  //
  // What a control is FOR, beside it, on hover AND on focus -- a keyboard user
  // reaches the sidebar's sections by Tab and deserves the same sentence. Drawn
  // here rather than by CSS because the sidebar scrolls, and a hint inside a
  // scrolling box is clipped at its edge. While shown it describes its target
  // (aria-describedby), so a screen reader hears it too.
  var hint = null;
  var hinted = null;

  function showHint(target) {
    if (!hint) {
      hint = document.createElement("div");
      hint.id = "ui-hint";
      hint.className = "ui-hint";
      hint.setAttribute("role", "tooltip");
      document.body.appendChild(hint);
    }
    hideHint();
    hint.textContent = hintText(target);
    hint.style.display = "block";
    var r = target.getBoundingClientRect();
    var left = r.right + 8;
    if (left + hint.offsetWidth > window.innerWidth - 8) left = r.left - hint.offsetWidth - 8;
    hint.style.left = Math.max(8, left) + "px";
    hint.style.top = r.top + r.height / 2 - hint.offsetHeight / 2 + "px";
    target.setAttribute("aria-describedby", "ui-hint");
    hinted = target;
  }

  function hideHint() {
    if (hint) hint.style.display = "none";
    if (hinted) hinted.removeAttribute("aria-describedby");
    hinted = null;
  }

  // Folded to its icons, the sidebar names each one: the NAME, where an open
  // sidebar's hint is the sentence about what the section is for.
  function folded() { return document.documentElement.dataset.navMode === "short"; }
  function hintText(el) {
    if (folded() && el.dataset.name && el.closest(".app-sidebar")) return el.dataset.name;
    return el.getAttribute("data-tip");
  }
  function hintTarget(e) {
    if (!e.target || typeof e.target.closest !== "function") return null;
    var el = e.target.closest("[data-tip], [data-name]");
    if (!el) return null;
    if (el.hasAttribute("data-tip")) return el;
    return folded() && el.closest(".app-sidebar") ? el : null;
  }

  document.addEventListener("mouseover", function (e) {
    var t = hintTarget(e);
    if (t && t !== hinted) showHint(t);
    else if (!t && hinted && !hinted.matches(":focus-visible")) hideHint();
  });
  document.addEventListener("focusin", function (e) {
    var t = hintTarget(e);
    if (t && t.matches(":focus-visible")) showHint(t);
  });
  document.addEventListener("focusout", hideHint);
  document.addEventListener("scroll", hideHint, { passive: true, capture: true });
  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape") hideHint();
  });

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
    document.querySelectorAll("details.account-menu[open], details[data-popover][open]").forEach(function (d) {
      if (!d.contains(e.target)) d.removeAttribute("open");
    });
  });

  document.addEventListener("keydown", function (e) {
    if (e.key !== "Escape") return;
    document.querySelectorAll("details.account-menu[open], details[data-popover][open]").forEach(function (d) {
      d.removeAttribute("open");
      // The focus goes back to the button that opened it, which is what a
      // keyboard user expects and what the browser does not do on its own.
      var summary = d.querySelector("summary");
      if (summary) summary.focus();
    });
  });

  // ------------------------------------------------------------------
  // The theme and the language (the account menu)
  //
  // Both are the visitor's own and live in localStorage; the first script in
  // the head reads them before the first paint (base.templ). The theme is
  // auto (follows the system, the default), light or dark: this writes the
  // choice, flips <html data-theme> in place and lights the chosen icon. No
  // request. The language is what the SERVER draws from, so its button still
  // posts the form; this only remembers the choice in localStorage first.
  //
  // What reads a colour ONCE when it starts -- the DAG island -- keeps the
  // old one until the next navigation.
  // ------------------------------------------------------------------

  var systemLight = window.matchMedia("(prefers-color-scheme: light)");
  function remember(key, value) {
    try { localStorage.setItem(key, value); } catch (e) { /* private mode: not kept */ }
  }
  function applyTheme(pref) {
    var root = document.documentElement;
    root.dataset.themePref = pref;
    root.dataset.theme = pref === "auto" ? (systemLight.matches ? "light" : "dark") : pref;
    document.querySelectorAll("[data-theme-set]").forEach(function (b) {
      b.setAttribute("aria-pressed", b.dataset.themeSet === pref ? "true" : "false");
    });
  }
  function setTheme(pref) {
    remember("brevis_theme", pref);
    applyTheme(pref);
  }
  applyTheme(document.documentElement.dataset.themePref || "auto");
  systemLight.addEventListener("change", function () {
    if (document.documentElement.dataset.themePref === "auto") applyTheme("auto");
  });

  document.addEventListener("click", function (e) {
    var btn = e.target.closest && e.target.closest("[data-theme-set]");
    if (btn) { setTheme(btn.dataset.themeSet); return; }
    var lang = e.target.closest && e.target.closest('[data-lang-form] button[name="lang"]');
    if (lang) remember("brevis_lang", lang.value);
    // The shortcuts list opens over the menu; the menu has done its job.
    var open = e.target.closest && e.target.closest(".account-menu [data-dialogo]");
    if (open) open.closest("details").open = false;
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

  // follow polls one live host, and keeps on it how to stop: the Runs screen
  // swaps the open run without a reload, and the old run's host must stop
  // polling when it leaves the page.
  function follow(host) {
    var url = host.dataset.live;
    if (!url) return;
    // A fixed pace, when the host gives one: a list that never ends -- the
    // Overview's attention -- has no "running" or "failed" of its own to set
    // it, and reading "failed" in the text is English.
    var every = Number(host.dataset.liveEvery) || 0;
    var timer = null;
    var last = "";
    var stopped = false;

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
      if (!ms || stopped) return;
      timer = setTimeout(tick, ms);
    }

    function tick() {
      // A hidden tab is a tab nobody is reading. Polling one for an afternoon
      // is traffic for a screen behind another window; the visibility listener
      // below catches up the moment it comes back.
      if (document.hidden) {
        schedule(every || LIVE_RUNNING);
        return;
      }
      fetch(url, { headers: { Accept: "text/html" } })
        .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.text();
        })
        .then(function (html) {
          if (stopped) return;
          // Nothing changed: skip the swap entirely. Replacing identical HTML
          // still resets a text selection and interrupts a screen reader, and
          // most polls of a running step change nothing.
          if (html === last) {
            schedule(every || LIVE_RUNNING);
            return;
          }

          // An OPEN dialog is somebody reading an error. Replacing the markup
          // under it closes it mid-sentence, so the refresh waits -- the run
          // has not gone anywhere.
          if (document.querySelector("dialog[open]")) {
            schedule(every || LIVE_RUNNING);
            return;
          }

          last = html;
          var doc = new DOMParser().parseFromString(html, "text/html");
          var terminal = false;
          var failed = false;

          // A region whose HTML did not change is left alone -- the run's
          // body is the same poll after poll while a step runs, and
          // redrawing it resets a search being typed and a log being read.
          // What does change keeps the log's place (keepLog).
          var kept = keepLog();
          doc.querySelectorAll("[data-live-target]").forEach(function (block) {
            var target = document.getElementById(block.dataset.liveTarget);
            if (target && target._live !== block.innerHTML) {
              target.innerHTML = block.innerHTML;
              target._live = block.innerHTML;
            }
            if (block.dataset.terminal === "true") terminal = true;
            if (block.textContent.indexOf("failed") >= 0) failed = true;
          });

          showAllTabs();
          restoreLog(kept);

          // Terminal stops the polling for good. The run is over and nothing
          // about it will change again -- and the indicator says so rather
          // than just disappearing, which would look like a failure.
          if (terminal) {
            say(t("run.live.finished"), false);
            return;
          }
          schedule(every || (failed ? LIVE_FAILED : LIVE_RUNNING));
        })
        .catch(function () {
          // A refresh that fails is not worth a message on the screen: the
          // page still shows what it showed, and the next tick may well work.
          // It slows down, so a server that is down is not hammered.
          schedule(LIVE_FAILED);
        });
    }

    function visible() {
      if (!document.hidden && timer && !stopped) {
        clearTimeout(timer);
        tick();
      }
    }
    document.addEventListener("visibilitychange", visible);

    host._stopLive = function () {
      stopped = true;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", visible);
    };
    if (!every) say(t("run.live.following"), true);
    schedule(every || LIVE_RUNNING);
  }
  document.querySelectorAll("[data-live]").forEach(follow);

  // ------------------------------------------------------------------
  // A list that follows itself (`[data-live-list]`)
  //
  // The Runs list's "Live" switch. On, every 5 s the same URL is fetched with
  // X-Brevis-Fragment: rows -- the server draws the list's region alone, with
  // the page's filters -- and swapped in; rows whose run was not there before
  // are marked `data-run-new`, which the CSS washes and fades.
  //
  // Off by default, and the choice is kept (localStorage), since somebody who
  // watches the list watches it every time. One request in flight at a time,
  // none while the tab is hidden (it catches up when it is shown), none while
  // a dialog is open over the list (somebody is reading an error), and it
  // stops when the answer is not a fragment -- an expired session redirects.
  // ------------------------------------------------------------------

  var LIVE_LIST_EVERY = 5000;
  var LIVE_LIST_KEY = "brevis.runs.live";

  document.querySelectorAll("[data-live-list]").forEach(function (button) {
    var region = document.getElementById(button.getAttribute("data-live-list"));
    if (!region || !window.fetch) return;
    var dot = button.querySelector("[data-live-dot]");
    var timer = null;
    var busy = false;
    var last = "";

    function stored() {
      try { return localStorage.getItem(LIVE_LIST_KEY) === "on"; } catch (e) { return false; }
    }
    function store(on) {
      try { localStorage.setItem(LIVE_LIST_KEY, on ? "on" : "off"); } catch (e) { /* a private window */ }
    }
    function on() { return button.getAttribute("aria-pressed") === "true"; }
    function draw(live) {
      button.setAttribute("aria-pressed", live ? "true" : "false");
      if (dot) dot.className = "h-1.5 w-1.5 rounded-full " + (live ? "bg-state-success live-dot-on" : "bg-line-strong");
    }
    function schedule() {
      clearTimeout(timer);
      if (on()) timer = setTimeout(tick, LIVE_LIST_EVERY);
    }

    function tick() {
      if (!on() || busy || document.hidden) return;
      if (document.querySelector("dialog[open]")) { schedule(); return; }
      busy = true;
      fetch(window.location.href, { headers: { "X-Brevis-Fragment": "rows" } })
        .then(function (r) {
          if (r.redirected || !r.ok) throw new Error("not a fragment");
          return r.text();
        })
        .then(function (html) {
          if (html !== last) {
            var seen = {};
            region.querySelectorAll("[data-run-id]").forEach(function (el) {
              seen[el.getAttribute("data-run-id")] = true;
            });
            var first = last === "";
            last = html;
            region.innerHTML = html;
            if (!first) {
              region.querySelectorAll("[data-run-id]").forEach(function (el) {
                if (!seen[el.getAttribute("data-run-id")]) el.setAttribute("data-run-new", "");
              });
            }
          }
          busy = false;
          schedule();
        })
        .catch(function () {
          busy = false;
          draw(false);
        });
    }

    button.addEventListener("click", function () {
      var live = !on();
      draw(live);
      store(live);
      if (live) tick();
      else clearTimeout(timer);
    });

    // Hidden, nothing is sent; shown again, it catches up at once.
    document.addEventListener("visibilitychange", function () {
      if (document.hidden) clearTimeout(timer);
      else if (on()) tick();
    });

    if (stored()) {
      draw(true);
      tick();
    }
  });

  // --- list params, as chips --------------------------------------------
  //
  // Progressive enhancement, and the plain field underneath is the point: with
  // no JavaScript `[data-lista]` stays a text input whose value is
  // "users,orders", which is exactly what the server expects. Everything here
  // only makes that easier to read and to edit.
  //
  // The input keeps holding the real value -- the chips are a view of it -- so
  // there is one source of truth and the form submits correctly even if this
  // code throws halfway.
  document.querySelectorAll("input[data-lista]").forEach(listChips);
  function listChips(input) {
    var caixa = document.createElement("div");
    caixa.className = "flex flex-wrap items-center gap-1.5 rounded-xl border border-line bg-parchment-soft px-2 py-1.5";
    var entrada = document.createElement("input");
    entrada.type = "text";
    entrada.className = "min-w-[6rem] flex-1 bg-transparent px-1 py-0.5 font-mono text-sm text-ink outline-none placeholder:text-muted/60";
    entrada.placeholder = input.placeholder || "add and press Enter";
    entrada.setAttribute("aria-label", "add an item");

    input.type = "hidden";
    input.parentNode.insertBefore(caixa, input);
    caixa.appendChild(entrada);

    function itens() {
      return input.value.split(",").map(function (s) { return s.trim(); })
        .filter(function (s) { return s !== ""; });
    }

    function grava(lista) {
      input.value = lista.join(",");
      desenha();
      // A value set by code fires nothing; the Run… preview listens for this.
      input.dispatchEvent(new Event("change", { bubbles: true }));
    }

    function desenha() {
      caixa.querySelectorAll("[data-chip]").forEach(function (el) { el.remove(); });
      itens().forEach(function (item, i) {
        var chip = document.createElement("span");
        chip.setAttribute("data-chip", "");
        chip.className = "inline-flex items-center gap-1 rounded-lg bg-ink/5 px-2 py-0.5 font-mono text-xs text-ink";
        chip.textContent = item;
        var x = document.createElement("button");
        x.type = "button";  // not submit: this is inside a form
        x.className = "text-muted hover:text-ink";
        x.textContent = "×";
        x.setAttribute("aria-label", "remove " + item);
        x.addEventListener("click", function () {
          var lista = itens();
          lista.splice(i, 1);
          grava(lista);
          entrada.focus();
        });
        chip.appendChild(x);
        caixa.insertBefore(chip, entrada);
      });
    }

    function acrescenta(texto) {
      var lista = itens();
      // A paste of "a,b,c" adds three, which is what somebody pasting a list
      // means. The engine refuses a repeated item, so this refuses it here
      // rather than at trigger time.
      texto.split(",").forEach(function (parte) {
        parte = parte.trim();
        if (parte !== "" && lista.indexOf(parte) === -1) lista.push(parte);
      });
      grava(lista);
      entrada.value = "";
    }

    entrada.addEventListener("keydown", function (e) {
      if (e.key === "Enter" || e.key === ",") {
        // Enter inside a form submits it; an item being added is not a submit.
        e.preventDefault();
        acrescenta(entrada.value);
      } else if (e.key === "Backspace" && entrada.value === "") {
        var lista = itens();
        lista.pop();
        grava(lista);
      }
    });
    // Whatever is typed and not committed still counts on submit: losing it
    // because nobody pressed Enter is the classic way a chips field lies.
    entrada.addEventListener("blur", function () { acrescenta(entrada.value); });
    entrada.form && entrada.form.addEventListener("submit", function () {
      acrescenta(entrada.value);
    });

    desenha();
  }

  // ------------------------------------------------------------------
  // The navigation drawer, below 860px
  //
  // It already opens without this: the button is a link to #app-sidebar and
  // the sheet opens the drawer on `:target`. What this adds is what a link
  // cannot do -- keep the URL clean (a back button that reopens a menu is a
  // back button that does not go back), say `aria-expanded`, close on
  // Escape, and put the focus where the keyboard expects it: into the drawer
  // when it opens, back on the button when it closes.
  // ------------------------------------------------------------------

  function drawerOpener() {
    return document.querySelector("[data-drawer-open]");
  }

  function setDrawer(open) {
    var root = document.documentElement;
    var opener = drawerOpener();
    if (open) root.setAttribute("data-drawer", "open");
    else root.removeAttribute("data-drawer");
    if (opener) opener.setAttribute("aria-expanded", open ? "true" : "false");
    if (open) {
      var close = document.querySelector("#app-sidebar [data-drawer-close]");
      if (close) close.focus();
    } else if (opener) {
      opener.focus();
    }
  }

  document.addEventListener("click", function (e) {
    if (!e.target || typeof e.target.closest !== "function") return;
    if (e.target.closest("[data-drawer-open]")) {
      e.preventDefault();
      setDrawer(true);
    } else if (e.target.closest("[data-drawer-close]")) {
      e.preventDefault();
      setDrawer(false);
    }
  });

  document.addEventListener("keydown", function (e) {
    if (e.key === "Escape" && document.documentElement.getAttribute("data-drawer") === "open") {
      setDrawer(false);
    }
  });

  // ------------------------------------------------------------------
  // `/` focuses the page's search box
  //
  // What every list tool binds. NEVER WHILE SOMEBODY IS TYPING: a slash
  // typed into a filter, a form field or the SQL editor is a character, not
  // a shortcut. CodeMirror edits in a hidden textarea inside `.CodeMirror`,
  // which is why that is checked by ancestry rather than by tag.
  // ------------------------------------------------------------------

  // typing is whether a key is going into a field: then it is a character,
  // never a shortcut. Every single-key shortcut below asks it first.
  function typing(t) {
    return !!t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName) ||
      (typeof t.closest === "function" && !!t.closest(".CodeMirror")));
  }

  document.addEventListener("keydown", function (e) {
    if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey) return;
    if (typing(e.target)) return;
    var box = document.querySelector('[data-hotkey="/"]');
    if (!box) return;
    e.preventDefault();
    box.focus();
    if (typeof box.select === "function") box.select();
  });

  // ------------------------------------------------------------------
  // `g` then a letter goes to a section; `?` lists the shortcuts
  //
  // The letters are on the sidebar's own links (data-go-key, from nav.go),
  // so a section that is not drawn cannot be jumped to and there is no map
  // here to disagree with the sidebar. The second key must come within a
  // second and a half, the way a reader types it, or the `g` is forgotten.
  // ------------------------------------------------------------------

  var pendingG = 0;
  document.addEventListener("keydown", function (e) {
    if (e.metaKey || e.ctrlKey || e.altKey || typing(e.target)) return;
    if (document.querySelector("dialog[open]")) return;
    if (e.key === "?") {
      var list = document.getElementById("shortcuts");
      if (list && typeof list.showModal === "function" && !list.open) {
        e.preventDefault();
        list.showModal();
      }
      return;
    }
    if (pendingG && Date.now() - pendingG < 1500) {
      pendingG = 0;
      var to = document.querySelector('#app-sidebar [data-go-key="' + e.key.toLowerCase() + '"]');
      if (to) {
        e.preventDefault();
        window.location.assign(to.getAttribute("href"));
      }
      return;
    }
    pendingG = e.key === "g" ? Date.now() : 0;
  });

  // ------------------------------------------------------------------
  // Copy buttons
  //
  // A code block's copy button is `hidden` in the markup: without this file
  // it would be a button that does nothing. Here it is revealed, and a click
  // copies the text of the element its `data-copy` names. The button says
  // "Copied" for a moment, in its label as well as its tooltip, so the
  // confirmation is heard and not only seen.
  // ------------------------------------------------------------------


  document.addEventListener("click", function (e) {
    if (!e.target || typeof e.target.closest !== "function") return;
    var b = e.target.closest("[data-copy]");
    if (!b || !navigator.clipboard) return;
    var src = document.querySelector(b.getAttribute("data-copy"));
    if (!src) return;
    navigator.clipboard.writeText(src.textContent).then(function () {
      var label = b.getAttribute("aria-label");
      b.setAttribute("aria-label", t("copy.done"));
      b.setAttribute("title", t("copy.done"));
      b.setAttribute("data-copied", "");
      setTimeout(function () {
        b.setAttribute("aria-label", label);
        b.setAttribute("title", label);
        b.removeAttribute("data-copied");
      }, 1500);
    });
  });

  // ------------------------------------------------------------------
  // A table row that goes somewhere
  //
  // A list row carries its page in `data-href`, and a click anywhere on it
  // goes there -- the target is the whole row, not a strip of text. A click
  // that lands on something of its own (a writer's link, a button, a
  // field) is left to it. The row's main cell keeps a real link, which is
  // how the keyboard and a page with no script get there; Cmd/Ctrl-click
  // and the middle button open it beside this page, as a link would.
  // ------------------------------------------------------------------

  document.addEventListener("click", function (e) {
    if (!e.target || typeof e.target.closest !== "function") return;
    if (e.defaultPrevented) return;
    var row = e.target.closest("tr[data-href]");
    if (!row || e.target.closest("a, button, input, label")) return;
    if (window.getSelection && String(window.getSelection()).length > 0) return;
    var href = row.getAttribute("data-href");
    if (e.metaKey || e.ctrlKey || e.button === 1) window.open(href, "_blank");
    else {
      navigating();
      window.location.assign(href);
    }
  });

  // ------------------------------------------------------------------
  // A list's drawer, by its address (screen-catalog T4, screen-loads T7)
  //
  // The server draws a drawer's address -- /data/target?u=, a load's
  // /ingest/loads/{workflow}/{step} -- as the list with the drawer open,
  // which is all a page with no script gets: the row is a link to it, and
  // its × a link back to the list. The list says which drawer is its own
  // (`data-drawer-list`: the fragment to ask for and the addresses that are
  // a drawer), so one piece of code serves both screens.
  //
  // Here a row fetches the drawer alone (X-Brevis-Fragment) and opens it
  // over the list without leaving it, the address following (pushState), so
  // Back and Forward open and close it and a reload or a sent link shows the
  // same. A drawer the page came with is re-opened modal, which `open` in
  // the markup is not. A link inside it to another of the same kind -- a
  // load's destination choice -- swaps it in place, replacing the address,
  // so closing still goes back to the list.
  //
  // Closing -- the ×, Esc, the backdrop -- goes back to the list as it was
  // filtered: a step back when this page opened it, else the list's address
  // in place of the drawer's. Any doubt navigates, as the link would.
  // ------------------------------------------------------------------

  var drawerList = document.querySelector("[data-drawer-list]");

  function isDrawerPath(path) {
    var at = drawerList.getAttribute("data-drawer-path");
    var exact = drawerList.hasAttribute("data-drawer-exact");
    return exact ? path === at : path.indexOf(at) === 0 && path.length > at.length;
  }

  function currentDrawer() {
    return document.querySelector("dialog[data-url-drawer]");
  }

  function watchDrawer(d) {
    d.addEventListener("close", function () {
      var back = d.getAttribute("data-close") || "/";
      d.remove();
      // Closed by Back or by the next drawer: the address is already right.
      if (d._quiet) return;
      if (history.state && history.state.urlDrawer) history.back();
      else history.replaceState(null, "", back);
    });
  }

  function quietClose(d) {
    d._quiet = true;
    if (d.open) d.close();
    else d.remove();
  }

  // Taking `open` off is not closing it: no close event, which would take
  // the drawer away.
  function showDrawer(d) {
    d.removeAttribute("open");
    d.showModal();
  }

  // openDrawer fetches the drawer at href; how is "push" (a row), "replace"
  // (a choice inside it) or "" (Back and Forward, the address already set).
  function openDrawer(href, how) {
    fetch(href, { headers: { "X-Brevis-Fragment": drawerList.getAttribute("data-drawer-fragment") } })
      .then(function (r) {
        if (r.redirected || !r.ok) throw new Error("not a fragment");
        return r.text();
      })
      .then(function (html) {
        var old = currentDrawer();
        if (old) quietClose(old);
        drawerList.insertAdjacentHTML("afterend", html);
        var d = currentDrawer();
        if (how === "push") history.pushState({ urlDrawer: true }, "", href);
        if (how === "replace") history.replaceState(history.state, "", href);
        watchDrawer(d);
        showDrawer(d);
      })
      .catch(function () {
        window.location.assign(href);
      });
  }

  if (drawerList && window.fetch && window.history && history.pushState) {
    var arrived = currentDrawer();
    if (arrived) {
      watchDrawer(arrived);
      showDrawer(arrived);
    }

    // Before the row's own handler, which would navigate.
    document.addEventListener(
      "click",
      function (e) {
        if (e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        if (!e.target || typeof e.target.closest !== "function") return;
        var close = e.target.closest("[data-target-close]");
        if (close) {
          var d = close.closest("dialog");
          if (d && d.open) {
            e.preventDefault();
            d.close();
          }
          return;
        }
        var swap = e.target.closest("dialog[data-url-drawer] a[data-drawer-swap]");
        if (swap) {
          e.preventDefault();
          openDrawer(swap.getAttribute("href"), "replace");
          return;
        }
        var row = e.target.closest("tr[data-href]");
        if (!row || !drawerList.contains(row)) return;
        var a = e.target.closest("a, button, input, label");
        if (a && !(a.tagName === "A" && row.contains(a) && a.getAttribute("href") === row.getAttribute("data-href"))) return;
        if (window.getSelection && String(window.getSelection()).length > 0) return;
        var href = row.getAttribute("data-href");
        if (!isDrawerPath(new URL(href, window.location.href).pathname)) return;
        e.preventDefault();
        openDrawer(href, "push");
      },
      true
    );

    window.addEventListener("popstate", function () {
      var d = currentDrawer();
      if (isDrawerPath(window.location.pathname)) {
        openDrawer(window.location.href, "");
      } else if (d) {
        quietClose(d);
      }
    });
  }

  // ------------------------------------------------------------------
  // A list that filters while somebody types
  //
  // The search box names the rows it filters (`data-filter-for`); each row
  // carries, lower-cased, the text a search looks in. Typing hides and
  // reveals rows already drawn, and says so when none is left. It is a
  // popover's list (a workflow's tags), so the URL is left alone: the pages'
  // lists search on the server, through `data-swap`.
  // ------------------------------------------------------------------

  document.querySelectorAll("[data-filter-for]").forEach(function (box) {
    var rows = document.getElementById(box.getAttribute("data-filter-for"));
    if (!rows) return;
    var empty = rows.parentElement.querySelector("[data-filter-empty]");

    box.addEventListener("input", function () {
      var q = box.value.trim().toLowerCase();
      var shown = 0;
      rows.querySelectorAll("[data-filter-row]").forEach(function (row) {
        var hit = q === "" || (row.getAttribute("data-filter-text") || "").indexOf(q) >= 0;
        row.hidden = !hit;
        if (hit) shown++;
      });
      if (empty) empty.hidden = shown > 0;
    });
  });

  // ------------------------------------------------------------------
  // Tabs by the URL's hash (`[data-hash-tabs]`). Every panel is rendered
  // (`[data-tab-panel]`), and WHICH ONE SHOWS IS THE CSS'S: the head writes
  // `data-hash` on <html> before the first paint, and the page's own rules
  // show the panel -- and mark the tab -- that names, the first when it names
  // none. So a link to #config paints Configuration first, with no frame of
  // Overview. This keeps `data-hash` and `aria-selected` current on a tab's
  // click and on hashchange. With no script the panels stack, each an anchor
  // with its heading, and the tab bar is never shown (data-js).
  //
  // The click is the document's, not the bar's: on the Runs screen the tabs
  // sit inside a region the live refresh redraws, and a listener on the old
  // bar would leave the new one dead. showTabs is called again after such a
  // redraw, for the same reason.
  function showTabs(host, key) {
    var bar = host.querySelector("[data-hash-tabs-bar]");
    var panels = host.querySelectorAll("[data-tab-panel]");
    if (!bar || !panels.length) return;
    var found = false;
    panels.forEach(function (p) { if (p.getAttribute("data-tab-panel") === key) found = true; });
    // With no tab named, the first TAB -- not the first panel: a tab can
    // have more than one panel (the Runs graph sits before its body), and
    // the first in the page need not be the first tab's.
    if (!found) {
      var first = bar.querySelector("[data-tab]");
      key = first ? first.getAttribute("data-tab") : panels[0].getAttribute("data-tab-panel");
    }
    document.documentElement.dataset.hash = key;
    bar.querySelectorAll("[data-tab]").forEach(function (t) {
      t.setAttribute("aria-selected", t.getAttribute("data-tab") === key ? "true" : "false");
    });
    mountGraph();
  }
  function showAllTabs() {
    document.querySelectorAll("[data-hash-tabs]").forEach(function (host) {
      showTabs(host, location.hash.slice(1));
    });
  }
  document.addEventListener("click", function (e) {
    var t = e.target && typeof e.target.closest === "function" ? e.target.closest("[data-hash-tabs-bar] [data-tab]") : null;
    var host = t && t.closest("[data-hash-tabs]");
    if (!host) return;
    var key = t.getAttribute("data-tab");
    history.replaceState(history.state, "", "#" + key);
    showTabs(host, key);
  });
  window.addEventListener("hashchange", showAllTabs);
  showAllTabs();

  // A preview's empty columns (`[data-null-toggle]`, screen-load-v2 LV4):
  // the server marked them, the CSS hides them while the preview carries
  // data-hide-null, and the switch takes it off and puts it back.
  document.querySelectorAll("[data-null-toggle]").forEach(function (button) {
    button.addEventListener("click", function () {
      var preview = button.closest("[data-preview]");
      if (!preview) return;
      var hide = !preview.hasAttribute("data-hide-null");
      preview.toggleAttribute("data-hide-null", hide);
      button.setAttribute("aria-checked", hide ? "true" : "false");
    });
  });

  // A form that asks first (`form[data-confirm]`): the attribute is the
  // question, already in the page's language. Declined, nothing is sent.
  // Delegated, because the run's head that holds Cancel is swapped by the
  // live refresh; and registered before the handlers below, which leave a
  // prevented submit alone. With no script the form posts without asking.
  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!form || typeof form.matches !== "function" || !form.matches("form[data-confirm]")) return;
    if (!window.confirm(form.getAttribute("data-confirm"))) e.preventDefault();
  });

  // ------------------------------------------------------------------
  // A GET form that swaps its answer
  //
  // `form[data-swap]` names the element its answer goes into. A submit
  // fetches the same URL with X-Brevis-Fragment: answer -- the handler draws
  // that part alone -- and swaps it in; the form, its values and the page
  // around it stay. The URL follows with replaceState, so a reload or a sent
  // link shows the same run. GET only: this is for parameters that may sit in
  // a URL, never for SQL, which the workbench posts.
  //
  // THE FORM IS STILL THE MECHANISM. No fetch, a non-2xx or a redirect (an
  // expired session) navigates to the URL the form would have, which is
  // what a submit was before this existed. Cmd/Ctrl-Enter submits from any
  // field.
  // ------------------------------------------------------------------

  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!form || typeof form.matches !== "function" || !form.matches("form[data-swap]")) return;
    var pane = document.getElementById(form.getAttribute("data-swap"));
    if (!pane || !window.fetch || !window.FormData) return;
    e.preventDefault();
    // One answer in flight: a submit that arrives meanwhile (a keystroke) is
    // remembered and sent when it lands, so the last thing typed always wins.
    if (pane.getAttribute("aria-busy") === "true") {
      form.setAttribute("data-swap-again", "");
      return;
    }

    var url = form.getAttribute("action").split("?")[0] + "?" + new URLSearchParams(new FormData(form)).toString();
    var button = form.querySelector('[type="submit"]');
    pane.setAttribute("aria-busy", "true");
    if (button) button.disabled = true;
    fetch(url, { headers: { "X-Brevis-Fragment": "answer" } })
      .then(function (r) {
        if (r.redirected || !r.ok) throw new Error("not an answer");
        return r.text();
      })
      .then(function (html) {
        pane.innerHTML = html;
        history.replaceState(history.state, "", url);
      })
      .catch(function () { window.location.assign(url); })
      .then(function () {
        pane.removeAttribute("aria-busy");
        if (button) button.disabled = false;
        if (form.hasAttribute("data-swap-again")) {
          form.removeAttribute("data-swap-again");
          form.requestSubmit();
        }
      });
  });

  // ------------------------------------------------------------------
  // A schedule's switch, flipped in place
  //
  // The Workflows list's switch is a form that posts the toggle. With the
  // script it posts in place: the server answers whether the schedule is on
  // and the toast's words, in the reader's language; the switch and the row
  // follow, and the toast's Undo posts the same form again. Any failure falls
  // back to the plain post, which comes back with a flash.
  // ------------------------------------------------------------------

  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!form || typeof form.matches !== "function" || !form.matches("form[data-toggle]")) return;
    if (!window.fetch) return;
    e.preventDefault();
    var button = form.querySelector('[role="switch"]');
    if (button) button.disabled = true;
    fetch(form.getAttribute("action"), { method: "POST", headers: { "X-Brevis-Fragment": "toggle" } })
      .then(function (r) {
        if (!r.ok) throw new Error("toggle refused");
        return r.json();
      })
      .then(function (got) {
        if (button) button.setAttribute("aria-checked", got.Active ? "true" : "false");
        var row = form.closest("tr");
        if (row) row.toggleAttribute("data-paused", !got.Active);
        window.Brevis.toast(got.Message, {
          action: got.Undo,
          onAction: function () { form.requestSubmit(); },
        });
      })
      .catch(function () { form.submit(); })
      .then(function () { if (button) button.disabled = false; });
  });

  // ------------------------------------------------------------------
  // The Run dialog
  //
  // Now, or another slot: the slot list shows only when asked for, and the
  // time of one's own only when "another" is chosen. The dialog posts with
  // the script so that a refused slot -- off the cron, in the future -- is
  // said inside it and it stays open; a run made is where the server says to
  // go, with the toast waiting there. A failed fetch posts the plain form.
  // ------------------------------------------------------------------

  document.addEventListener("change", function (e) {
    var el = e.target;
    if (!el || typeof el.closest !== "function") return;
    var form = el.closest("form[data-run-now]");
    if (!form) return;
    var pick = form.querySelector("[data-slot-pick]");
    var other = form.querySelector("[data-slot-other]");
    if (el.name === "when" && pick) pick.hidden = el.value !== "slot";
    if (other) {
      var wantsOther = !pick.hidden && form.querySelector('select[name="slot"]').value === "other";
      other.hidden = !wantsOther;
      other.required = wantsOther;
    }
  });

  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (!form || typeof form.matches !== "function" || !form.matches("form[data-run-now]")) return;
    // The drawer's close is a submit too (method=dialog): it closes, and
    // posts nothing.
    if (e.submitter && e.submitter.getAttribute("formmethod") === "dialog") return;
    if (!window.fetch || !window.FormData) return;
    e.preventDefault();
    var error = form.querySelector("[data-run-error]");
    var button = form.querySelector('[type="submit"]:not([formmethod])');
    if (button) button.disabled = true;
    fetch(form.getAttribute("action"), {
      method: "POST",
      headers: { "X-Brevis-Fragment": "trigger" },
      body: new URLSearchParams(new FormData(form)),
    })
      .then(function (r) {
        if (r.ok) {
          return r.json().then(function (got) { window.location.assign(got.Location); });
        }
        if (r.status >= 400 && r.status < 500) {
          return r.text().then(function (text) {
            if (error) {
              error.textContent = text.trim();
              error.hidden = false;
            }
          });
        }
        throw new Error("trigger failed");
      })
      .catch(function () { form.submit(); })
      .then(function () { if (button) button.disabled = false; });
  });

  // A form whose checkboxes are its submit (`form[data-submit-on-change]`):
  // checking one navigates at once, as its Apply button would. Only a box --
  // a text field changes on blur, and leaving one is not a choice.
  document.addEventListener("change", function (e) {
    var box = e.target;
    if (!box || box.type !== "checkbox" || typeof box.closest !== "function") return;
    var form = box.closest("form[data-submit-on-change]");
    if (form && typeof form.requestSubmit === "function") form.requestSubmit();
  });

  // ------------------------------------------------------------------
  // The Run… drawer (`form[data-run-form]`, screen-workflows T9)
  //
  // Every control posts with no script. This adds what a script can, from
  // what the server wrote on each field (`[data-param]`): its type, its enum,
  // its pattern and its default. Each value is checked by the server's own
  // rules -- an integer, true or false, one of the enum, the pattern, or only
  // the safe characters when there is none -- and checked again by the
  // server; what differs from the default is marked, and its warning shown;
  // the footer counts both, and Run waits while a value is invalid. The
  // payload is the params as the trigger will read them -- a list joined
  // with commas, blanks dropped, as paramsFromForm does -- with the slot when
  // one is chosen, and the same by the CLI. Restore defaults puts every
  // control back; ⌘↵ runs.
  // ------------------------------------------------------------------

  var SAFE = /^[A-Za-z0-9_.:\/=,+@\- ]*$/;

  function joined(values) {
    return values.map(function (v) { return String(v).trim(); }).filter(function (v) { return v !== ""; }).join(",");
  }
  function fieldValue(form, field) {
    return joined(new FormData(form).getAll(field.getAttribute("data-field")));
  }
  function checkValue(form, field, value) {
    var type = field.getAttribute("data-type") || "string";
    var list = type.indexOf("list|") === 0;
    var elem = list ? type.slice(5) : type;
    var allowed = (field.getAttribute("data-enum") || "").split(",").filter(Boolean);
    var pattern = field.getAttribute("data-pattern");
    if (value === "") return field.getAttribute("data-default") === "" && !list ? form.dataset.msgRequired : "";
    var items = list ? value.split(",") : [value];
    for (var i = 0; i < items.length; i++) {
      var item = items[i];
      if (elem === "integer" && !/^-?\d+$/.test(item)) return form.dataset.msgInteger;
      if (elem === "boolean" && item !== "true" && item !== "false") return form.dataset.msgBoolean;
      if (allowed.length && allowed.indexOf(item) < 0) return form.dataset.msgEnum;
      if (pattern) {
        try { if (!new RegExp(pattern).test(item)) return form.dataset.msgPattern; } catch (e) { /* the server says */ }
      } else if (elem === "string" && !SAFE.test(item)) {
        return form.dataset.msgChars;
      }
    }
    return "";
  }
  // chosenSlot is the slot the drawer asks for, or "".
  function chosenSlot(form) {
    var when = form.querySelector('input[name="when"]:checked');
    if (!when || when.value !== "slot") return "";
    var slot = form.querySelector('select[name="slot"]');
    if (!slot) return "";
    if (slot.value !== "other") return slot.value;
    var at = form.querySelector('input[name="slot_at"]');
    return at ? at.value : "";
  }
  function quote(v) { return /^[A-Za-z0-9_.:\/=,+@-]+$/.test(v) ? v : "'" + v.replace(/'/g, "'\\''") + "'"; }
  function say(form, key, n) {
    var text = form.dataset[n === 1 ? key + "One" : key] || "";
    return text.replace("{n}", n);
  }

  function runState(form) {
    var changed = 0, invalid = 0, params = {}, given = [];
    form.querySelectorAll("[data-param]").forEach(function (field) {
      var name = field.getAttribute("data-param");
      var value = fieldValue(form, field);
      var isChanged = value !== joined((field.getAttribute("data-default") || "").split(","));
      var problem = checkValue(form, field, value);
      params[name] = value;
      if (isChanged) {
        changed++;
        given.push([name, value]);
      }
      if (problem) invalid++;
      var mark = field.querySelector("[data-param-changed]");
      if (mark) mark.hidden = !isChanged;
      var warn = field.querySelector("[data-param-warn]");
      if (warn) warn.hidden = !isChanged;
      var error = field.querySelector("[data-param-error]");
      if (error) {
        error.textContent = problem || "";
        error.hidden = !problem;
      }
    });

    var status = form.querySelector("[data-run-status]");
    if (status) {
      status.textContent = invalid ? say(form, "sayInvalid", invalid)
        : changed ? "● " + say(form, "sayChanged", changed) : form.dataset.sayDefaults || "";
      status.classList.toggle("text-state-failed", invalid > 0);
      status.classList.toggle("text-warn", !invalid && changed > 0);
    }
    var run = form.querySelector('[type="submit"]:not([formmethod])');
    if (run) run.disabled = invalid > 0;

    var box = form.querySelector("[data-run-preview]");
    if (!box) return;
    var slot = chosenSlot(form);
    var payload = { params: params };
    if (slot) payload.slot = slot;
    box.querySelector("code").textContent = JSON.stringify(payload, null, 2);
    var cli = box.querySelector("[data-run-cli]");
    if (cli) {
      var slug = form.dataset.slug, project = form.dataset.project;
      if (slot) {
        // The CLI has no slot: the trigger takes it, as a curl.
        cli.textContent = ["curl -X POST " + location.origin + "/workflows/" + slug + "/trigger", "-d slot=" + quote(slot)]
          .concat(given.map(function (g) { return "-d " + quote("param." + g[0] + "=" + g[1]); })).join(" \\\n  ");
      } else {
        cli.textContent = ["brevis run " + slug].concat(project ? ["--project " + quote(project)] : [])
          .concat(given.map(function (g) { return "--param " + quote(g[0] + "=" + g[1]); })).join(" ");
      }
    }
    box.hidden = false;
  }

  // A boolean's switch drives its select, which is what posts.
  function syncSwitches(form) {
    form.querySelectorAll("[data-bool-for]").forEach(function (sw) {
      var select = form.querySelector('select[name="' + sw.getAttribute("data-bool-for") + '"]');
      if (!select) return;
      sw.setAttribute("aria-checked", select.value === "true" ? "true" : "false");
      var shown = sw.parentElement.querySelector("[data-bool-value]");
      if (shown) shown.textContent = select.value;
    });
  }

  document.querySelectorAll("form[data-run-form]").forEach(function (form) {
    syncSwitches(form);
    runState(form);
  });
  ["input", "change"].forEach(function (type) {
    document.addEventListener(type, function (e) {
      var form = e.target && typeof e.target.closest === "function" ? e.target.closest("form[data-run-form]") : null;
      if (form) runState(form);
    });
  });

  document.addEventListener("click", function (e) {
    if (!e.target || typeof e.target.closest !== "function") return;
    var form = e.target.closest("form[data-run-form]");
    if (!form) return;
    var sw = e.target.closest("[data-bool-for]");
    if (sw) {
      var select = form.querySelector('select[name="' + sw.getAttribute("data-bool-for") + '"]');
      if (select) {
        select.value = select.value === "true" ? "false" : "true";
        select.dispatchEvent(new Event("change", { bubbles: true }));
        syncSwitches(form);
      }
      return;
    }
    var step = e.target.closest("[data-step]");
    if (step) {
      var input = step.parentElement.querySelector('input[type="number"]');
      if (input) {
        input.value = (parseInt(input.value, 10) || 0) + parseInt(step.getAttribute("data-step"), 10);
        input.dispatchEvent(new Event("input", { bubbles: true }));
      }
      return;
    }
    if (e.target.closest("[data-run-reset]")) {
      form.querySelectorAll("[data-param]").forEach(function (field) {
        var name = field.getAttribute("data-field"), dflt = field.getAttribute("data-default") || "";
        var items = dflt.split(",").map(function (v) { return v.trim(); });
        field.querySelectorAll('[name="' + name + '"]').forEach(function (el) {
          if (el.type === "checkbox") el.checked = items.indexOf(el.value) >= 0;
          else if (el.type !== "hidden") el.value = dflt;
          if (el.hasAttribute("data-lista")) el.dispatchEvent(new Event("input", { bubbles: true }));
        });
      });
      syncSwitches(form);
      runState(form);
    }
  });

  document.addEventListener("keydown", function (e) {
    if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey)) return;
    var form = e.target && typeof e.target.closest === "function" ? e.target.closest("form[data-run-form]") : null;
    if (!form || typeof form.requestSubmit !== "function") return;
    e.preventDefault();
    var run = form.querySelector('[type="submit"]:not([formmethod])');
    if (run && !run.disabled) form.requestSubmit(run);
  });

  // The test event (`[data-test-body]`): the body is checked against the
  // stream's format as it is edited -- one object for json, a list of them
  // for array, one object per line for ndjson -- and, for an auto_table, the
  // table each event names against the prefixes the stream accepts; then it
  // says where the event would land. The rules and the words are the
  // server's, written on the box (data-test-*, data-msg-*), so the browser
  // and the gateway cannot disagree. The curl under it is rebuilt from its
  // head and the body. Nothing is sent from here.
  function testEvent(box) {
    var drawer = box.closest("dialog") || document;
    var status = drawer.querySelector("[data-test-status]");
    var curl = drawer.querySelector("[data-test-curl]");
    var body = box.value.replace(/\s+$/, "");
    var say = function (name, vars) {
      var text = box.getAttribute("data-msg-" + name) || "";
      for (var k in vars || {}) text = text.split("{" + k + "}").join(vars[k]);
      return text;
    };
    var isObject = function (v) { return v !== null && typeof v === "object" && !Array.isArray(v); };
    var problem = "";
    var warning = "";
    var events = [];
    try {
      switch (box.getAttribute("data-test-format")) {
        case "array":
          var list = JSON.parse(body);
          if (!Array.isArray(list)) problem = say("array");
          else if (!list.every(isObject)) problem = say("items");
          else events = list;
          break;
        case "ndjson":
          body.split("\n").forEach(function (line, i) {
            if (problem || line.trim() === "") return;
            try {
              var one = JSON.parse(line);
              if (!isObject(one)) problem = say("line", { n: i + 1, e: say("object") });
              else events.push(one);
            } catch (e) { problem = say("line", { n: i + 1, e: e.message }); }
          });
          break;
        default:
          var event = JSON.parse(body);
          if (!isObject(event)) problem = say("object");
          else events = [event];
      }
    } catch (e) { problem = say("invalid", { e: e.message }); }

    var lands = [];
    var table = box.getAttribute("data-test-table");
    if (!problem && table) {
      var allow = (box.getAttribute("data-test-allow") || "").split(",").filter(Boolean);
      var dataset = box.getAttribute("data-test-dataset") || "";
      events.forEach(function (ev) {
        if (problem || warning) return;
        var name = ev[table];
        if (typeof name !== "string" || name === "") {
          problem = say("no-table");
        } else if (allow.length && !allow.some(function (p) { return name.indexOf(p) === 0; })) {
          warning = say("refused", { name: name, allow: allow.join(", ") });
        } else {
          var to = dataset ? dataset + "." + name : name;
          if (lands.indexOf(to) < 0) lands.push(to);
        }
      });
    } else if (!problem) {
      lands.push(box.getAttribute("data-test-lands") || "");
    }

    if (status) {
      var format = box.getAttribute("data-test-format");
      status.textContent = problem || warning || (say("valid", { format: format }) + " " + say("lands", { to: lands.join(", ") }));
      status.className = "text-[12px] " + (problem ? "text-state-failed" : warning ? "text-state-queued" : "text-state-success");
    }
    if (curl) curl.textContent = curl.getAttribute("data-test-curl-head") + "\n" + body + "\nEOF";
  }
  document.querySelectorAll("[data-test-body]").forEach(function (box) {
    testEvent(box);
    box.addEventListener("input", function () { testEvent(box); });
  });

  // A form that answers while somebody types (`form[data-swap-typing]`):
  // a pause of 200 ms in its search box submits it, so a word is one request
  // and not one per letter. The server answers, so a paginated list is
  // searched whole and not one page of it.
  var typing;
  document.addEventListener("input", function (e) {
    var box = e.target;
    if (!box || box.type !== "search" || typeof box.closest !== "function") return;
    var form = box.closest("form[data-swap-typing]");
    if (!form || typeof form.requestSubmit !== "function") return;
    clearTimeout(typing);
    typing = setTimeout(function () { form.requestSubmit(); }, 200);
  });

  document.addEventListener("keydown", function (e) {
    if (e.key !== "Enter" || !(e.metaKey || e.ctrlKey)) return;
    var form = e.target && typeof e.target.closest === "function" ? e.target.closest("form[data-swap]") : null;
    if (!form || typeof form.requestSubmit !== "function") return;
    e.preventDefault();
    form.requestSubmit();
  });

  // ------------------------------------------------------------------
  // A curl that follows its form
  //
  // The endpoint's curl names the form it follows (`data-curl-from`) and
  // carries its fixed parts; as somebody types, the query string is built
  // again from the fields that have a value -- an empty one is the default
  // and says nothing. The escaping is Go's url.QueryEscape, which the server
  // used to draw the same curl: encodeURIComponent leaves ! ' ( ) * alone,
  // and a quote typed into a field would end the shell's quoted string.
  // ------------------------------------------------------------------

  function queryEscape(v) {
    return encodeURIComponent(v)
      .replace(/[!'()*]/g, function (c) { return "%" + c.charCodeAt(0).toString(16).toUpperCase(); })
      .replace(/%20/g, "+");
  }

  function followCurl(form) {
    document.querySelectorAll('[data-curl-from="' + form.id + '"]').forEach(function (pre) {
      var q = [];
      new FormData(form).forEach(function (value, name) {
        if (name === "run" || typeof value !== "string" || value === "") return;
        q.push(queryEscape(name) + "=" + queryEscape(value));
      });
      var u = pre.getAttribute("data-curl-base") + (q.length ? "?" + q.join("&") : "");
      var code = pre.querySelector("code") || pre;
      code.textContent = pre.getAttribute("data-curl-head") + "'" + u + "'";
    });
  }

  ["input", "change"].forEach(function (type) {
    document.addEventListener(type, function (e) {
      var form = e.target && e.target.form;
      if (form && form.id && document.querySelector('[data-curl-from="' + form.id + '"]')) followCurl(form);
    });
  });

  // ------------------------------------------------------------------
  // The command palette (Cmd/Ctrl-K)
  //
  // A native <dialog> in every page (Base), filled from /api/palette -- the
  // sections and everything the console lists -- fetched on the first open
  // and kept for the page's life. Filtering is here, on every keystroke; the
  // search text in each item was lower-cased by the server, the list
  // filter's contract.
  //
  // RANKED, NOT FUZZY: the label starting with the query, then a word in it
  // starting with it (`sal` finds `daily_sales`), then anywhere. Groups keep
  // their own heading, ordered by their best match, at most 8 rows each and
  // 40 in all. A query that is a run's id offers that run first.
  //
  // THE KEY IS HEARD IN THE CAPTURE PHASE, so an editor below -- CodeMirror
  // in the workbench -- cannot swallow it. A choice is an ordinary
  // navigation (View Transitions apply); Cmd/Ctrl-Enter opens it beside.
  // Escape and the backdrop close it (the dialog's own, and the backdrop
  // rule at the top of this file), and the focus goes back where it was.
  // ------------------------------------------------------------------

  var paletteEl = document.getElementById("palette");
  if (paletteEl && typeof paletteEl.showModal === "function") {
    wirePalette(paletteEl);
  }

  function wirePalette(dialog) {
    var input = document.getElementById("palette-input");
    var list = document.getElementById("palette-list");
    var rowTpl = dialog.querySelector("template[data-palette-row]");
    var groupTpl = dialog.querySelector("template[data-palette-group]");
    var icons = {};
    dialog.querySelectorAll("[data-palette-icon-set] [data-icon]").forEach(function (n) {
      icons[n.getAttribute("data-icon")] = n.innerHTML;
    });
    var ORDER = ["Runs", "Sections", "Workflows", "Destinations", "Endpoints", "Projects"];
    var UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
    var index = null;
    var failed = false;
    var loading = false;
    var shown = [];
    var active = 0;
    var returnTo = null;


    function load() {
      if (index || loading) return;
      loading = true;
      fetch("/api/palette", { headers: { Accept: "application/json" } })
        .then(function (r) {
          // A REDIRECT IS THE LOGIN PAGE, not an index: an expired session
          // must not be parsed as data.
          if (r.redirected || !r.ok) throw new Error("no index");
          return r.json();
        })
        .then(function (d) { index = (d && d.items) || []; })
        .catch(function () { index = []; failed = true; })
        .then(function () { loading = false; if (dialog.open) render(); });
    }

    function rank(item, q) {
      var label = item.label.toLowerCase();
      if (label.indexOf(q) === 0) return 0;
      // Letters in any language: "Execuções" is one word, not three.
      var words = label.split(/[^\p{L}\p{N}]+/u);
      for (var i = 0; i < words.length; i++) if (words[i].indexOf(q) === 0) return 1;
      return item.text.indexOf(q) >= 0 ? 2 : -1;
    }

    function results(raw) {
      var q = raw.trim().toLowerCase();
      var out = [];
      if (UUID.test(q)) {
        out.push({ group: "Runs", groupLabel: t("palette.group.runs"), label: t("palette.open_run", { id: q.slice(0, 8) }), hint: q, href: "/runs/" + q, icon: "history" });
      }
      if (!index) return out;
      if (q === "") return out.concat(index.filter(function (it) { return it.group === "Sections"; }));

      var groups = {};
      index.forEach(function (it, n) {
        var r = rank(it, q);
        if (r < 0) return;
        (groups[it.group] = groups[it.group] || []).push({ it: it, r: r, n: n });
      });
      var names = Object.keys(groups).map(function (g) {
        groups[g].sort(function (a, b) { return a.r - b.r || a.n - b.n; });
        return g;
      });
      names.sort(function (a, b) {
        return groups[a][0].r - groups[b][0].r || ORDER.indexOf(a) - ORDER.indexOf(b);
      });
      names.forEach(function (g) {
        groups[g].slice(0, 8).forEach(function (x) { out.push(x.it); });
      });
      return out.slice(0, 40);
    }

    function heading(text) {
      var h = groupTpl.content.firstElementChild.cloneNode(true);
      h.textContent = text;
      list.appendChild(h);
    }

    function render() {
      shown = results(input.value);
      list.textContent = "";
      var last = "";
      shown.forEach(function (it, i) {
        if (it.group !== last) { heading(it.groupLabel || it.group); last = it.group; }
        var row = rowTpl.content.firstElementChild.cloneNode(true);
        row.id = "palette-opt-" + i;
        row.setAttribute("data-palette-index", String(i));
        row.querySelector("[data-palette-icon]").innerHTML = icons[it.icon] || icons.database || "";
        row.querySelector("[data-palette-label]").textContent = it.label;
        row.querySelector("[data-palette-hint]").textContent = it.hint || "";
        list.appendChild(row);
      });
      if (shown.length === 0) {
        heading(failed ? t("palette.failed") : index ? t("palette.empty") : t("palette.loading"));
      }
      input.setAttribute("aria-expanded", shown.length > 0 ? "true" : "false");
      choose(Math.min(active, Math.max(0, shown.length - 1)));
    }

    function choose(i) {
      if (shown.length === 0) {
        input.removeAttribute("aria-activedescendant");
        return;
      }
      active = (i + shown.length) % shown.length;
      list.querySelectorAll('[role="option"]').forEach(function (row) {
        row.setAttribute("aria-selected", row.id === "palette-opt-" + active ? "true" : "false");
      });
      var row = document.getElementById("palette-opt-" + active);
      input.setAttribute("aria-activedescendant", row.id);
      if (typeof row.scrollIntoView === "function") row.scrollIntoView({ block: "nearest" });
    }

    function go(i, beside) {
      var it = shown[i];
      if (!it) return;
      dialog.close();
      // An action is done here rather than followed.
      if (it.action === "toggle-theme") {
        setTheme(document.documentElement.dataset.theme === "light" ? "dark" : "light");
        return;
      }
      // A language is the sidebar's own switcher pressed: the same POST, the
      // same way back to this page.
      if (it.action && it.action.indexOf("set-lang:") === 0) {
        var lang = it.action.slice("set-lang:".length);
        var b = document.querySelector('[data-lang-form] button[name="lang"][value="' + lang + '"]');
        if (b) b.click();
        return;
      }
      if (beside) window.open(it.href, "_blank");
      else window.location.assign(it.href);
    }

    function open() {
      returnTo = document.activeElement;
      input.value = "";
      active = 0;
      dialog.showModal();
      input.focus();
      load();
      render();
    }

    dialog.addEventListener("close", function () {
      input.setAttribute("aria-expanded", "false");
      if (returnTo && typeof returnTo.focus === "function") returnTo.focus();
      returnTo = null;
    });

    document.addEventListener("keydown", function (e) {
      if (!(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey || !e.key) return;
      if (e.key.toLowerCase() === "k") {
        e.preventDefault();
        e.stopPropagation();
        if (dialog.open) dialog.close();
        else open();
      }
    }, true);

    document.addEventListener("click", function (e) {
      if (e.target && typeof e.target.closest === "function" && e.target.closest("[data-palette-open]")) open();
    });

    input.addEventListener("input", function () { active = 0; render(); });
    input.addEventListener("keydown", function (e) {
      if (e.key === "ArrowDown") { e.preventDefault(); choose(active + 1); }
      else if (e.key === "ArrowUp") { e.preventDefault(); choose(active - 1); }
      else if (e.key === "Enter") { e.preventDefault(); go(active, e.metaKey || e.ctrlKey); }
    });
    list.addEventListener("mousemove", function (e) {
      var row = e.target.closest ? e.target.closest("[data-palette-index]") : null;
      if (row && +row.getAttribute("data-palette-index") !== active) choose(+row.getAttribute("data-palette-index"));
    });
    list.addEventListener("click", function (e) {
      var row = e.target.closest ? e.target.closest("[data-palette-index]") : null;
      if (row) go(+row.getAttribute("data-palette-index"), e.metaKey || e.ctrlKey);
    });
  }
  // ------------------------------------------------------------------
  // The Overview's recent runs, filtered on the page (screen-overview T6)
  //
  // The server drew twenty rows, each with its state; All / Failed /
  // Running shows the ones that match, Running meaning in flight -- the same
  // three questions as the Runs screen. A filter of twenty rows is not worth
  // a request.
  // ------------------------------------------------------------------

  var inFlightStates = { running: true, queued: true, retrying: true };
  document.addEventListener("click", function (e) {
    var b = e.target && typeof e.target.closest === "function" ? e.target.closest("[data-recent-filter]") : null;
    if (!b) return;
    var panel = b.closest("[data-tab-panel]") || document;
    var want = b.getAttribute("data-recent-filter");
    panel.querySelectorAll("[data-recent-filter]").forEach(function (x) {
      x.setAttribute("aria-pressed", x === b ? "true" : "false");
    });
    panel.querySelectorAll("tr[data-status]").forEach(function (tr) {
      var s = tr.getAttribute("data-status");
      tr.hidden = !(want === "all" || (want === "running" ? inFlightStates[s] : s === want));
    });
  });

  // ------------------------------------------------------------------
  // The run's log in the browser (screen-runs T6)
  //
  // The server drew every line with its number and level; this filters what
  // is already there. A search marks what it finds (<mark>, the text escaped
  // first), the level shows the warnings or the errors, Wrap breaks long
  // lines, and Copy takes the lines that show, as text.
  //
  // What they hold is kept per run, not in the markup: the live refresh
  // redraws the log when a step ends, and a search somebody is typing, or the
  // place somebody is reading, must survive it (keepLog / restoreLog).
  //
  // "Go to the error in the log" opens the Logs tab, clears a filter that
  // would hide the line, brings it to the middle and lights it for 1.6s --
  // without the motion where the reader asked for less.
  // ------------------------------------------------------------------

  var logStates = {};
  function logStateOf(log) {
    var id = log.getAttribute("data-run") || "";
    return logStates[id] || (logStates[id] = { q: "", level: "all", wrap: false });
  }

  function escapeHTML(s) {
    return s.replace(/[&<>"]/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c];
    });
  }

  function markText(text, q) {
    var lower = text.toLowerCase(), out = "", at = 0, i;
    while ((i = lower.indexOf(q, at)) >= 0) {
      out += escapeHTML(text.slice(at, i)) + "<mark>" + escapeHTML(text.slice(i, i + q.length)) + "</mark>";
      at = i + q.length;
    }
    return out + escapeHTML(text.slice(at));
  }

  function applyLog(log) {
    var st = logStateOf(log);
    var q = st.q.toLowerCase();
    var search = log.querySelector("[data-log-search]");
    if (search && search.value !== st.q) search.value = st.q;
    log.querySelectorAll("[data-log-level]").forEach(function (b) {
      b.setAttribute("aria-pressed", b.getAttribute("data-log-level") === st.level ? "true" : "false");
    });
    var wrap = log.querySelector("[data-log-wrap]");
    if (wrap) wrap.setAttribute("aria-pressed", st.wrap ? "true" : "false");
    if (st.wrap) log.setAttribute("data-wrap", "");
    else log.removeAttribute("data-wrap");

    log.querySelectorAll("[data-n]").forEach(function (line) {
      var span = line.querySelector("[data-text]");
      if (!span) return;
      if (line._text === undefined) line._text = span.textContent;
      var text = line._text;
      var show = (st.level === "all" || line.getAttribute("data-level") === st.level) &&
        (!q || text.toLowerCase().indexOf(q) >= 0);
      line.hidden = !show;
      if (q && show) span.innerHTML = markText(text, q);
      else if (span.firstElementChild) span.textContent = text;
    });
    // A gap counts lines that are not here, so it matches nothing.
    var filtering = q !== "" || st.level !== "all";
    log.querySelectorAll("[data-log-gap]").forEach(function (g) { g.hidden = filtering; });
  }

  function applyAllLogs(root) {
    (root || document).querySelectorAll("[data-log]").forEach(applyLog);
  }

  // keepLog notes where the reader is before a live redraw: how far down the
  // log, and the search box's caret when it has the focus.
  function keepLog() {
    var lines = document.querySelector("[data-log-lines]");
    var box = document.activeElement;
    var typing = box && box.hasAttribute && box.hasAttribute("data-log-search");
    return {
      scroll: lines ? lines.scrollTop : 0,
      caret: typing ? [box.selectionStart, box.selectionEnd] : null,
    };
  }

  function restoreLog(kept) {
    applyAllLogs();
    var lines = document.querySelector("[data-log-lines]");
    if (lines) lines.scrollTop = kept.scroll;
    var box = document.querySelector("[data-log-search]");
    if (kept.caret && box && document.activeElement !== box) {
      box.focus();
      box.setSelectionRange(kept.caret[0], kept.caret[1]);
    }
  }

  var logTimer = null;
  document.addEventListener("input", function (e) {
    var box = e.target && typeof e.target.closest === "function" ? e.target.closest("[data-log-search]") : null;
    var log = box && box.closest("[data-log]");
    if (!log) return;
    logStateOf(log).q = box.value;
    clearTimeout(logTimer);
    logTimer = setTimeout(function () { applyLog(log); }, 80);
  });

  function goToError() {
    var line = document.getElementById("log-cause");
    var log = line && line.closest("[data-log]");
    if (!log) return;
    if (line.hidden) {
      var st = logStateOf(log);
      st.q = "";
      st.level = "all";
      applyLog(log);
    }
    history.replaceState(history.state, "", "#logs");
    showAllTabs();
    var still = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    line.scrollIntoView({ block: "center", behavior: still ? "auto" : "smooth" });
    line.removeAttribute("data-flash");
    void line.offsetWidth; // restart the light on a second click
    line.setAttribute("data-flash", "");
    setTimeout(function () { line.removeAttribute("data-flash"); }, 1600);
  }

  document.addEventListener("click", function (e) {
    var el = e.target && typeof e.target.closest === "function" ? e.target : null;
    if (!el) return;
    if (el.closest("[data-goto-error]")) {
      goToError();
      return;
    }
    var log = el.closest("[data-log]");
    if (!log) return;
    var st = logStateOf(log);
    var level = el.closest("[data-log-level]");
    if (level) {
      st.level = level.getAttribute("data-log-level");
      applyLog(log);
      return;
    }
    if (el.closest("[data-log-wrap]")) {
      st.wrap = !st.wrap;
      applyLog(log);
      return;
    }
    var copy = el.closest("[data-log-copy]");
    if (copy && navigator.clipboard) {
      var text = [];
      log.querySelectorAll("[data-n]").forEach(function (line) {
        if (!line.hidden) text.push(line._text !== undefined ? line._text : line.querySelector("[data-text]").textContent);
      });
      navigator.clipboard.writeText(text.join("\n")).then(function () {
        var label = copy.querySelector("[data-label]");
        var was = label.textContent;
        label.textContent = t("copy.done");
        copy.setAttribute("data-copied", "");
        setTimeout(function () {
          label.textContent = was;
          copy.removeAttribute("data-copied");
        }, 1500);
      });
    }
  });

  applyAllLogs();

  // ------------------------------------------------------------------
  // The Runs screen opens another run in place (screen-runs)
  //
  // A click on the list fetches that run's pane (X-Brevis-Fragment: detail)
  // and swaps it in; the list stays where it was, scrolled where it was. The
  // address follows (pushState), so Back and Forward walk the runs opened,
  // and a reload opens the same one.
  //
  // What the old pane started is stopped first -- its live refresh, its
  // graph's polling -- and what the new one needs is started: those two, and
  // the drawer's chips and preview. Everything else on the page is delegated
  // to the document and needs nothing.
  //
  // Any doubt navigates instead: a failed fetch, an expired session. The
  // item is a real link, so that is just the click it always was.
  // ------------------------------------------------------------------

  // The graph comes with the Steps tab (screen-runs T7). It is mounted when
  // its box is on the screen -- React Flow measures it -- and its scripts,
  // ~350 KB of React, are fetched the first time, in their order, from the
  // list the page wrote on it (data-island-js): a page opened on a log does
  // not pay for a drawing nobody asked for.
  var graphLoading = false;
  function mountGraph() {
    var root = document.getElementById("dag");
    if (!root || root._dag || root.offsetParent === null) return;
    if (window.BrevisDag) {
      window.BrevisDag.mount(root);
      return;
    }
    var host = root.closest("[data-island-js]");
    if (!host || graphLoading) return;
    graphLoading = true;
    (host.getAttribute("data-island-css") || "").split(" ").filter(Boolean).forEach(function (href) {
      var link = document.createElement("link");
      link.rel = "stylesheet";
      link.href = href;
      document.head.appendChild(link);
    });
    var srcs = (host.getAttribute("data-island-js") || "").split(" ").filter(Boolean);
    (function next(i) {
      if (i >= srcs.length) {
        graphLoading = false;
        mountGraph();
        return;
      }
      var script = document.createElement("script");
      script.src = srcs[i];
      script.onload = function () { next(i + 1); };
      script.onerror = function () { graphLoading = false; };
      document.body.appendChild(script);
    })(0);
  }

  // ------------------------------------------------------------------
  // A stream's configuration: Explained or YAML (screen-streams T4)
  //
  // Both are drawn; the segment says which shows, by an attribute the CSS
  // reads. With no script the segment is not drawn and the two stack.
  // ------------------------------------------------------------------

  document.addEventListener("click", function (e) {
    if (!e.target || typeof e.target.closest !== "function") return;
    var set = e.target.closest("[data-config-view] [data-view-set]");
    if (!set) return;
    var box = set.closest("[data-config-view]");
    var view = set.getAttribute("data-view-set");
    box.setAttribute("data-config-view", view);
    box.querySelectorAll("[data-view-set]").forEach(function (b) {
      b.setAttribute("aria-pressed", b.getAttribute("data-view-set") === view ? "true" : "false");
    });
  });

  // ------------------------------------------------------------------
  // A workflow's steps: graph or list (screen-workflows T8)
  //
  // The segment chooses the view, by an attribute the CSS reads; the graph is
  // mounted when it is shown, since React Flow measures its box. A row of the
  // list chooses its step in the graph: the graph is shown and told, and a
  // graph not yet mounted reads the choice when it is.
  // ------------------------------------------------------------------

  function showSteps(card, view) {
    card.setAttribute("data-view", view);
    card.querySelectorAll("[data-view-set]").forEach(function (b) {
      b.setAttribute("aria-pressed", b.getAttribute("data-view-set") === view ? "true" : "false");
    });
    if (view === "graph") mountGraph();
  }

  document.addEventListener("click", function (e) {
    if (!e.target || typeof e.target.closest !== "function") return;
    var set = e.target.closest("[data-steps-view] [data-view-set]");
    if (set) {
      showSteps(set.closest("[data-steps-view]"), set.getAttribute("data-view-set"));
      return;
    }
    var row = e.target.closest("[data-steps-view] [data-select-step]");
    if (!row) return;
    var card = row.closest("[data-steps-view]");
    var id = row.getAttribute("data-select-step");
    var dag = card.querySelector("#dag");
    if (dag) dag.dataset.select = id;
    showSteps(card, "graph");
    document.dispatchEvent(new CustomEvent("brevis:select-step", { detail: id }));
    card.scrollIntoView({ block: "nearest" });
  });

  var runPane = document.getElementById("run-pane");
  var runColumn = document.querySelector("[data-runs-list]");

  function runIdOf(path) {
    var m = /^\/runs\/([0-9a-f-]{36})$/.exec(path);
    return m ? m[1] : "";
  }

  function markOpen(id) {
    runColumn.querySelectorAll("a[data-run-id]").forEach(function (a) {
      if (a.getAttribute("data-run-id") === id) a.setAttribute("aria-current", "true");
      else a.removeAttribute("aria-current");
    });
  }

  function openRun(href, push) {
    fetch(href, { headers: { "X-Brevis-Fragment": "detail" } })
      .then(function (r) {
        if (r.redirected || !r.ok) throw new Error("not a fragment");
        return r.text();
      })
      .then(function (html) {
        runPane.querySelectorAll("[data-live]").forEach(function (host) {
          if (host._stopLive) host._stopLive();
        });
        if (window.BrevisDag) window.BrevisDag.unmount(runPane);
        runPane.innerHTML = html;
        runPane.scrollTop = 0;

        runPane.querySelectorAll("[data-live]").forEach(follow);
        runPane.querySelectorAll("input[data-lista]").forEach(listChips);
        runPane.querySelectorAll("form[data-run-form]").forEach(runPreview);
        showAllTabs(); // which mounts the graph, if its tab is open
        applyAllLogs(runPane);

        if (push) history.pushState({ run: true }, "", href);
        var url = new URL(href, window.location.href);
        markOpen(runIdOf(url.pathname));
        var detail = runPane.querySelector("[data-crumb]");
        var crumb = document.querySelector("[data-crumb-current]");
        if (detail && crumb) crumb.textContent = detail.getAttribute("data-crumb");
      })
      .catch(function () {
        window.location.href = href;
      });
  }

  if (runPane && runColumn && window.fetch && window.history && history.pushState) {
    document.addEventListener("click", function (e) {
      if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
      var a = e.target && typeof e.target.closest === "function" ? e.target.closest("a[data-run-id]") : null;
      if (!a || !runColumn.contains(a)) return;
      e.preventDefault();
      if (a.getAttribute("aria-current") === "true") return;
      openRun(a.href, true);
    });

    // Back and Forward between runs swap the pane again. Anything else -- the
    // bare /runs, which opens the newest -- is a page of its own.
    window.addEventListener("popstate", function () {
      if (runIdOf(window.location.pathname)) openRun(window.location.href, false);
      else window.location.reload();
    });

    // Up and Down walk the list; Enter is the link's own.
    runColumn.addEventListener("keydown", function (e) {
      if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
      if (e.metaKey || e.ctrlKey || e.altKey || typing(e.target)) return;
      var items = Array.prototype.slice.call(runColumn.querySelectorAll("a[data-run-id]"));
      if (!items.length) return;
      var at = items.indexOf(document.activeElement);
      if (at < 0) at = items.findIndex(function (a) { return a.getAttribute("aria-current") === "true"; });
      var next = e.key === "ArrowDown" ? Math.min(at + 1, items.length - 1) : Math.max(at - 1, 0);
      e.preventDefault();
      items[next].focus();
    });
  }

  // ------------------------------------------------------------------
  // A click is acknowledged (nav-latency NL3)
  //
  // On real infrastructure a page takes a moment to come, and an old page
  // that sits unchanged after a click reads as a click that did not take.
  // 120 ms after a navigation starts, <html data-navigating> draws a thin bar
  // at the top (the sheet's); below that -- a prefetched page -- nothing
  // shows. The next page arrives without the mark, and `pageshow` clears it
  // on a page restored by Back, which would otherwise keep it.
  //
  // Registered LAST and on `defaultPrevented`: a click a handler already took
  // -- a dialog's opener, a tab, a swap in place -- is not a navigation. Nor
  // is a new tab, a download, another origin or a hash on this page.
  // ------------------------------------------------------------------

  var navTimer = null;
  function navigating() {
    clearTimeout(navTimer);
    navTimer = setTimeout(function () {
      document.documentElement.setAttribute("data-navigating", "");
    }, 120);
  }

  document.addEventListener("click", function (e) {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    var a = e.target && typeof e.target.closest === "function" ? e.target.closest("a[href]") : null;
    if (!a || a.hasAttribute("download")) return;
    if (a.getAttribute("target") === "_blank") return;
    var u = new URL(a.href, window.location.href);
    if (u.origin !== window.location.origin) return;
    if (u.pathname === window.location.pathname && u.search === window.location.search) return;
    navigating();
  });

  document.addEventListener("submit", function (e) {
    var form = e.target;
    if (e.defaultPrevented || !form || typeof form.getAttribute !== "function") return;
    if ((form.getAttribute("method") || "").toLowerCase() === "dialog") return;
    navigating();
  });

  window.addEventListener("pageshow", function () {
    clearTimeout(navTimer);
    document.documentElement.removeAttribute("data-navigating");
  });
})();
