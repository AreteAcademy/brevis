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
        if (trigger.tagName === "A") e.preventDefault();
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
    hint.textContent = target.getAttribute("data-tip");
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

  function hintTarget(e) {
    return e.target && typeof e.target.closest === "function" ? e.target.closest("[data-tip]") : null;
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
  // The theme switch
  //
  // The server chose the theme from the `brevis_theme` cookie before the page
  // was painted; this only flips it in place and writes the cookie the next
  // page will be drawn from. No request: the theme is a preference, not
  // state anybody else reads.
  //
  // What reads a colour ONCE when it starts -- the DAG island -- keeps the
  // old one until the next navigation.
  // ------------------------------------------------------------------

  document.addEventListener("click", function (e) {
    var btn = e.target.closest && e.target.closest("[data-theme-toggle]");
    if (!btn) return;
    var next = document.documentElement.dataset.theme === "light" ? "dark" : "light";
    document.documentElement.dataset.theme = next;
    document.cookie = "brevis_theme=" + next + "; Path=/; Max-Age=31536000; SameSite=Lax";
    var label = next === "light" ? t("theme.to_dark") : t("theme.to_light");
    btn.setAttribute("aria-label", label);
    btn.setAttribute("title", label);
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
      if (dot) dot.className = "h-1.5 w-1.5 rounded-full " + (live ? "bg-state-running animate-pulse" : "bg-line-strong");
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
            region.querySelectorAll("tr[data-run-id]").forEach(function (tr) {
              seen[tr.getAttribute("data-run-id")] = true;
            });
            var first = last === "";
            last = html;
            region.innerHTML = html;
            if (!first) {
              region.querySelectorAll("tr[data-run-id]").forEach(function (tr) {
                if (!seen[tr.getAttribute("data-run-id")]) tr.setAttribute("data-run-new", "");
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
  document.querySelectorAll("input[data-lista]").forEach(function (input) {
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
  });

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
  // A list that filters while somebody types
  //
  // The search box names the rows it filters (`data-filter-for`); each row
  // carries, lower-cased, the text a search looks in -- written by the same
  // function the server searches with, so the two cannot disagree. Typing
  // hides and reveals rows the server already drew: a search on the server
  // HIDES non-matches rather than dropping them, which is what lets deleting
  // a letter bring a row back. Enter still submits the form, as with no
  // script.
  //
  // THE URL FOLLOWS, with replaceState: a reload or a sent link shows the
  // same rows, and the back button is not filled with one entry per letter.
  // A list URL carries no statement -- the SQL screen's refusal to touch
  // history is about SQL, and is kept in sql.js. The filter bar's links
  // (`data-filter-keep`) are rewritten too, so a status clicked after typing
  // keeps the search.
  // ------------------------------------------------------------------

  function withQuery(href, name, value) {
    var u = new URL(href, window.location.href);
    if (value) u.searchParams.set(name, value);
    else u.searchParams.delete(name);
    return u.pathname + u.search + u.hash;
  }

  document.querySelectorAll("[data-filter-for]").forEach(function (box) {
    var rows = document.getElementById(box.getAttribute("data-filter-for"));
    if (!rows) return;
    var scope = rows.closest("[data-filter-scope]") || document;

    box.addEventListener("input", function () {
      var raw = box.value.trim();
      var q = raw.toLowerCase();
      var shown = 0;
      rows.querySelectorAll("[data-filter-row]").forEach(function (row) {
        var hit = q === "" || (row.getAttribute("data-filter-text") || "").indexOf(q) >= 0;
        row.hidden = !hit;
        if (hit) shown++;
      });

      var foot = scope.querySelector("[data-foot]");
      if (foot) {
        var total = parseInt(foot.getAttribute("data-foot-total"), 10) || 0;
        var noun = foot.getAttribute("data-foot-noun") || "";
        foot.textContent = shown === total ? total + " " + noun : "Showing " + shown + " of " + total + " " + noun;
      }
      // A group (a workflow's tbody) with every row hidden hides its header
      // too, the way the server draws it.
      rows.querySelectorAll("[data-filter-group]").forEach(function (group) {
        group.hidden = !group.querySelector("[data-filter-row]:not([hidden])");
      });
      var empty = scope.querySelector("[data-filter-empty]");
      if (empty) empty.hidden = shown > 0;

      // A box with no name filters a list that is not the page's (a
      // popover's), and says nothing in the URL.
      if (!box.name) return;
      history.replaceState(history.state, "", withQuery(window.location.href, box.name, raw));
      document.querySelectorAll("[data-filter-keep] a[href]").forEach(function (a) {
        a.setAttribute("href", withQuery(a.getAttribute("href"), box.name, raw));
      });
    });
  });

  // Tabs by the URL's hash (`[data-hash-tabs]`). Every panel is rendered
  // (`[data-tab-panel]`), and WHICH ONE SHOWS IS THE CSS'S: the head writes
  // `data-hash` on <html> before the first paint, and the page's own rules
  // show the panel -- and mark the tab -- that names, the first when it names
  // none. So a link to #config paints Configuration first, with no frame of
  // Overview. This keeps `data-hash` and `aria-selected` current on a tab's
  // click and on hashchange. With no script the panels stack, each an anchor
  // with its heading, and the tab bar is never shown (data-js).
  document.querySelectorAll("[data-hash-tabs]").forEach(function (host) {
    var bar = host.querySelector("[data-hash-tabs-bar]");
    var panels = host.querySelectorAll("[data-tab-panel]");
    if (!bar || !panels.length) return;
    function show(key) {
      var found = false;
      panels.forEach(function (p) { if (p.getAttribute("data-tab-panel") === key) found = true; });
      if (!found) key = panels[0].getAttribute("data-tab-panel");
      document.documentElement.dataset.hash = key;
      bar.querySelectorAll("[data-tab]").forEach(function (t) {
        t.setAttribute("aria-selected", t.getAttribute("data-tab") === key ? "true" : "false");
      });
    }
    bar.addEventListener("click", function (e) {
      var t = e.target && typeof e.target.closest === "function" ? e.target.closest("[data-tab]") : null;
      if (!t) return;
      var key = t.getAttribute("data-tab");
      history.replaceState(history.state, "", "#" + key);
      show(key);
    });
    window.addEventListener("hashchange", function () { show(location.hash.slice(1)); });
    show(location.hash.slice(1));
  });

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

  // A group that collapses (`[data-group-toggle]` in a tbody): the button,
  // shown only where a script runs (data-js), folds the group's rows under
  // its header. With no script every group stays open.
  document.querySelectorAll("[data-group-toggle]").forEach(function (button) {
    button.addEventListener("click", function () {
      var open = button.getAttribute("aria-expanded") !== "false";
      button.setAttribute("aria-expanded", open ? "false" : "true");
      var group = button.closest("tbody");
      if (group) group.toggleAttribute("data-collapsed", open);
    });
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

  // A form whose checkboxes are its submit (`form[data-submit-on-change]`):
  // checking one navigates at once, as its Apply button would. Only a box --
  // a text field changes on blur, and leaving one is not a choice.
  document.addEventListener("change", function (e) {
    var box = e.target;
    if (!box || box.type !== "checkbox" || typeof box.closest !== "function") return;
    var form = box.closest("form[data-submit-on-change]");
    if (form && typeof form.requestSubmit === "function") form.requestSubmit();
  });

  // The Run… drawer's payload (`form[data-run-form]`): the params as the
  // trigger will read them -- each `param.` field, a list's values joined with
  // commas and blanks dropped, as paramsFromForm does on the server. Hidden in
  // the markup, so with no script there is no preview rather than a stale one.
  function runPreview(form) {
    var box = form.querySelector("[data-run-preview]");
    if (!box || !window.FormData) return;
    var params = {};
    var data = new FormData(form);
    data.forEach(function (_, name) {
      if (name.indexOf("param.") !== 0 || params.hasOwnProperty(name.slice(6))) return;
      params[name.slice(6)] = data.getAll(name).map(function (v) { return String(v).trim(); })
        .filter(function (v) { return v !== ""; }).join(",");
    });
    box.querySelector("code").textContent = JSON.stringify({ params: params }, null, 2);
    box.hidden = false;
  }
  document.querySelectorAll("form[data-run-form]").forEach(runPreview);
  ["input", "change"].forEach(function (type) {
    document.addEventListener(type, function (e) {
      var form = e.target && typeof e.target.closest === "function" ? e.target.closest("form[data-run-form]") : null;
      if (form) runPreview(form);
    });
  });

  // The test event (`[data-test-body]`): the body is checked against the
  // stream's format as it is edited -- one object for json, a list of them
  // for array, one object per line for ndjson -- and the curl under it is
  // rebuilt from its head and the body. Nothing is sent from here.
  function testEvent(box) {
    var drawer = box.closest("dialog") || document;
    var status = drawer.querySelector("[data-test-status]");
    var curl = drawer.querySelector("[data-test-curl]");
    var body = box.value.replace(/\s+$/, "");
    var problem = "";
    var isObject = function (v) { return v !== null && typeof v === "object" && !Array.isArray(v); };
    try {
      switch (box.getAttribute("data-test-format")) {
        case "array":
          var list = JSON.parse(body);
          if (!Array.isArray(list)) problem = "an array stream takes a JSON list of events";
          else if (!list.every(isObject)) problem = "every item of the list has to be an object";
          break;
        case "ndjson":
          body.split("\n").forEach(function (line, i) {
            if (problem || line.trim() === "") return;
            try {
              if (!isObject(JSON.parse(line))) problem = "line " + (i + 1) + " is not an object";
            } catch (e) { problem = "line " + (i + 1) + ": " + e.message; }
          });
          break;
        default:
          if (!isObject(JSON.parse(body))) problem = "a json stream takes one object per request";
      }
    } catch (e) { problem = e.message; }
    if (status) {
      status.textContent = problem ? "Not valid: " + problem : "Valid " + box.getAttribute("data-test-format") + ".";
      status.className = "text-[0.75rem] " + (problem ? "text-state-failed" : "text-state-success");
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
        var sw = document.querySelector("[data-theme-toggle]");
        if (sw) sw.click();
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
