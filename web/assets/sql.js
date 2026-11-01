// The workbench's editor: CodeMirror mounted ON the textarea, never instead
// of it.
//
// MOUNTED AND NOT REPLACED, which is the whole contract. `fromTextArea` keeps
// the original element in the DOM and writes back into it before the form
// submits, so the page works with this file blocked, failing to load, or
// simply disabled -- and so the Go suite can test every behaviour of this
// screen without a browser.
//
// CODEMIRROR 5 AND NOT 6. Six is ESM-only and needs a bundler; this
// repository has no JavaScript toolchain and does not want one -- the
// Tailwind binary is the standalone build for the same reason, "no Node".
// Five ships a UMD bundle that drops in beside React's, which is how every
// vendored asset here already works.
(function () {
  "use strict";
  var area = document.getElementById("q");
  if (!area) return;

  // WHAT THE SERVER ECHOED, READ BEFORE ANYTHING REWRITES IT.
  //
  // The tabs put the active statement into this box at startup and the
  // Recent rail needs the one that actually ran. Reading it once here makes
  // the two independent of the order they are wired in -- which is the
  // mistake the storage prefix already made in this file.
  var echoed = area.value;

  // EVERY PIECE OF STATE IS DECLARED HERE, ABOVE THE CALLS THAT FILL IT, AND
  // THAT IS NOT TIDINESS.
  //
  // A `var` is hoisted WITHOUT its value: the declaration moves to the top
  // of this function and the ASSIGNMENT stays where it is written. These
  // lived below, beside the functions that use them -- so `wireTabs()` read
  // three tabs out of storage and put them here, and then `var tabs = [];`
  // ran and threw them away. The strip had already been drawn, so the screen
  // looked right, and the first click on a tab threw.
  //
  // It hid for a second reason worth writing down: this file RETURNS EARLY
  // when CodeMirror is missing, above those lines -- so the only path that
  // could be exercised without a browser was the only path on which the bug
  // could not happen.
  //
  // This file has shipped that mistake twice. `var store` was the first, and
  // every key it read at startup came out `undefined`. Twice is a shape
  // rather than an accident, so the shape is what changed.
  var editor = null;
  var tabs = [];
  var at = "";
  var pending = null;

  // The cost line, and the statement it last asked a price for.
  var costLine = null;
  var costAfter = 0;
  var costWait = null;
  var priced = "";

  // Recent: the rail, its template, its cap and what it lists. Held here
  // rather than inside wireRecent because a run that replaces the answer
  // adds to the same list the page loaded with.
  var recent = { rail: null, tpl: null, cap: 20, list: [] };

  // Whether a Run is in flight: a second Run waits for the first rather than
  // racing it into the same pane.
  var running = false;

  // The tree is wired whether or not CodeMirror loaded, so a blocked CDN
  // costs the highlighting and not the screen. Tabs and Recent are the same:
  // both work on the plain textarea.
  wireTree();
  wireChrome();
  wireResultTabs();
  wirePager(answer());
  wireRecent();
  wireTabs();
  wireCost();
  wireCopy();
  wireCSV();
  if (typeof CodeMirror === "undefined") {
    area.addEventListener("input", keep);
    return;
  }

  editor = CodeMirror.fromTextArea(area, {
    mode: "text/x-sql",
    lineNumbers: true,
    lineWrapping: true,
    // The browser's own, so a long query scrolls the way every other
    // textarea on this machine does.
    viewportMargin: 10,
    // Tab moves focus rather than inserting a character. A form with one
    // button after it is a form somebody tabs out of, and trapping Tab in a
    // text field is the oldest accessibility complaint there is.
    extraKeys: {
      Tab: false,
      "Shift-Tab": false,
      // Run, which is what every workbench this is modelled on binds.
      "Ctrl-Enter": run,
      "Cmd-Enter": run,
      // And completion, which is what every one of them binds for this.
      "Ctrl-Space": complete,
    },
  });

  // A keystroke is not a save, but a tab that forgets what was typed in it
  // is a tab nobody uses twice.
  editor.on("changes", keep);

  // A `.` OPENS IT WITHOUT THE KEY, because that is the moment somebody is
  // asking what is in a table. `inputRead` fires for typing and not for a
  // paste of a hundred lines that happens to contain dots.
  editor.on("inputRead", function (cm, change) {
    if (change.origin !== "+input") return;
    if (change.text.length !== 1 || change.text[0] !== ".") return;
    complete(cm);
  });

  function run() {
    editor.save();
    var form = area.form;
    if (!form) return;
    // requestSubmit so the button's own validation and the submit event both
    // happen, which `form.submit()` skips.
    if (form.requestSubmit) form.requestSubmit();
    else form.submit();
  }

  // COMPLETION, AND THE HALF OF THE WAREHOUSE IT KNOWS.
  //
  // THE LIST IS THE TREE, READ AT THE MOMENT IT OPENS. The tree loads
  // lazily, so a table built once at mount would know whatever had been
  // opened before this file ran, which is nothing.
  //
  // AND IT KNOWS THE COLUMNS OF WHAT IS OPEN, AND ONLY THOSE. Filling the
  // rest would be one `COLUMNS` query per relation across the whole
  // warehouse -- the project-wide scan CHECKPOINT D refused. The screen
  // says so under the editor, because a list that silently knows half a
  // warehouse is how somebody concludes a column does not exist.
  function complete(cm) {
    if (!cm.showHint || !CodeMirror.hint || !CodeMirror.hint.sql) return;
    // completeSingle off: typing a `.` must not insert a name because there
    // happened to be exactly one.
    cm.showHint({ hint: hints, completeSingle: false });
  }

  function hints(cm) {
    return CodeMirror.hint.sql(cm, { tables: schema() });
  }

  // SCOPED TO THE CONNECTION THE QUERY WILL RUN ON. Two warehouses can be
  // open in the tree at once, and offering one's relations while the other
  // is the target is offering a name that does not exist there.
  //
  // The node is found through the `connect` button rather than through
  // `data-connection`: the rail groups by warehouse NAME, and the hidden
  // field holds the catalog TARGET. They are not the same string.
  function schema() {
    var hidden = document.querySelector('#workbench input[name="target"]');
    var on = hidden ? hidden.value : "";
    var scope = document;
    if (on !== "") {
      var btn = document.querySelector('button[name="connect"][value="' + css(on) + '"]');
      var node = btn && btn.closest ? btn.closest("[data-connection]") : null;
      // No node for the target is no honest list: every relation on the
      // screen then belongs to some other warehouse. Keywords still
      // complete, which is what the editor did before this slice.
      if (!node) return {};
      scope = node;
    }
    var tables = {};
    scope.querySelectorAll("[data-relation]").forEach(function (row) {
      var cols = [];
      row.querySelectorAll("[data-column]").forEach(function (c) {
        cols.push(c.getAttribute("data-column"));
      });
      tables[row.getAttribute("data-relation")] = cols;
    });
    return tables;
  }

  // The textarea is what the handler reads, so it has to hold the text at
  // the moment of submit and not at the moment somebody stopped typing.
  if (area.form) {
    area.form.addEventListener("submit", function (ev) {
      editor.save();
      // AND THE TAB KEEPS WHAT IS ABOUT TO BE RUN, so a refusal comes back
      // to a tab holding the statement that was refused.
      store();

      // A RUN REPLACES THE ANSWER AND NOTHING ELSE. The tree's own buttons
      // (expand, connect) submit this form too, carrying a name; they are
      // not Runs, and they still post the page. A Run is the button with no
      // name, or no button at all (Ctrl/Cmd-Enter).
      var by = ev.submitter;
      if (by && by.name) return;
      var pane = document.querySelector("[data-answer]");
      if (!pane || !window.fetch || !window.FormData) return;
      ev.preventDefault();
      if (running) return;
      runInPlace(area.form, pane);
    });
  }

  // THE TREE, AND WHY IT NO LONGER RELOADS THE SCREEN.
  //
  // Every button in the tree is a real submit against the editor's form, and
  // that is the version somebody with JavaScript off still gets: opening a
  // relation is a round trip, because the columns are not on the page yet.
  //
  // With this file, the same click fetches the same markup from
  // `/api/sql/columns` and puts it where the reload used to put it. The
  // server draws it -- there is no tree markup in this file -- so the
  // classes, the ARIA and the sentence about a warehouse that cannot say
  // have one definition.
  //
  // AND NOTHING CLOSES. The server path can only hold ONE open relation,
  // because what is open rides in a form field and a field holding a list
  // needs a separator that a `schema.name` may contain. Here the open set is
  // just the DOM, so three relations in three schemas stay open at once.
  //
  // THE PICKER IS A PAGE (screen-sql T1). Choosing a connection submits its
  // own GET form: the listing, the logo and the label beside Run all belong
  // to the connection, the server draws them, and the open statements are in
  // the tabs' storage already, so nothing typed is lost.
  function wireTree() {
    document.addEventListener("click", function (ev) {
      var table = ev.target.closest ? ev.target.closest("[data-select]") : null;
      if (table) {
        ev.preventDefault();
        offer(table.getAttribute("data-select"));
        var shut = table.parentNode.querySelector('button[name="expand"][aria-expanded="false"]');
        if (shut) openRelation(shut);
        return;
      }
      var btn = ev.target.closest ? ev.target.closest('button[name="expand"]') : null;
      if (!btn) return;
      ev.preventDefault();
      openRelation(btn);
    });

    var pick = document.querySelector("[data-connection-pick] select");
    if (pick) pick.addEventListener("change", function () {
      store();
      pick.form.submit();
    });

    var box = document.querySelector("[data-tree-search]");
    if (box) box.addEventListener("input", function () { filter(box.value); });
  }

  // A TABLE'S FIRST LOOK GOES WHERE IT TAKES NOTHING AWAY: the open tab when
  // it is empty, a new tab when somebody has written in it. Overwriting a
  // half-written query because a table was clicked would be the click
  // somebody learns never to make.
  function offer(statement) {
    if (text().trim() === "") {
      put(statement);
      store();
    } else {
      openTab(statement);
    }
    if (editor) editor.focus();
    estimate();
  }

  // A relation's columns, fetched once and then only hidden and shown. The
  // second click costs nothing, which is CHECKPOINT D's rule read the other
  // way round: a listing costs, so it is paid for once.
  function openRelation(btn) {
    var rel = btn.value;
    var slot = document.querySelector('[data-columns="' + css(rel) + '"]');
    if (!slot) return;
    var dot = rel.indexOf(".");
    var url = "/api/sql/columns?target=" + encodeURIComponent(btn.getAttribute("data-connection")) +
      "&schema=" + encodeURIComponent(rel.slice(0, dot)) +
      "&name=" + encodeURIComponent(rel.slice(dot + 1));
    toggle(btn, slot, url);
  }

  function toggle(btn, slot, url) {
    if (slot.innerHTML.trim() !== "") {
      var shut = slot.hidden;
      slot.hidden = !shut;
      btn.setAttribute("aria-expanded", shut ? "true" : "false");
      return;
    }
    btn.setAttribute("aria-busy", "true");
    fetch(url, { headers: { Accept: "text/html" } })
      .then(function (r) { return r.ok ? r.text() : ""; })
      .then(function (html) {
        // AN EMPTY ANSWER IS AN ANSWER. A warehouse that cannot describe is
        // not an error here either -- the server draws the sentence that
        // says so, and a refusal draws nothing rather than leaving the
        // button spinning.
        slot.innerHTML = html;
        slot.hidden = false;
        btn.setAttribute("aria-expanded", "true");
      })
      .catch(function () { slot.innerHTML = ""; })
      .then(function () { btn.removeAttribute("aria-busy"); });
  }

  // THE FILTER SEES THE CHOSEN CONNECTION'S LISTING, which is all the page
  // holds: one connection at a time, drawn whole by the server.
  function filter(term) {
    var q = term.trim().toLowerCase();
    document.querySelectorAll("#objects [data-relation]").forEach(function (row) {
      var name = row.getAttribute("data-relation").toLowerCase();
      row.hidden = q !== "" && name.indexOf(q) < 0;
    });
    document.querySelectorAll("#objects details").forEach(function (d) {
      var any = false;
      d.querySelectorAll("[data-relation]").forEach(function (row) { if (!row.hidden) any = true; });
      d.hidden = q !== "" && !any;
      if (any) d.open = true;
    });
  }

  // THE CHROME: two grips, one collapse, and a memory.
  //
  // A DRAG IS A NUMBER, NOT A GESTURE. Each drag writes one custom property
  // on the workbench and one entry in localStorage; a reload restores the
  // number. Nothing replays anything, and nothing here adds or removes a
  // class to resize a pane.
  //
  // THE FLOOR COMES FROM THE MARKUP. `data-min` is on the grip, where
  // somebody reading the page can see it -- a minimum that lived only in
  // this file is one the next person deletes without noticing, and what
  // breaks is a pane dragged to nothing with no handle left to drag back.
  // THE KEY IS A FUNCTION, NOT A VARIABLE, AND THAT IS A BUG THIS FILE
  // SHIPPED WITH.
  //
  // It was `var store = "brevis.workbench."`, declared HERE -- below the
  // three `recall` calls that wireChrome makes at startup. A `var` is
  // hoisted without its value, so those three read `undefinedrail`,
  // `undefinededitor` and `undefinedrail-collapsed`, while every write
  // during a drag ran later and used `brevis.workbench.rail`. The keys
  // never met: the workbench restored nothing, ever.
  //
  // A function declaration is hoisted WITH its body, so where it sits in
  // this file stops being able to matter.
  function key(name) {
    return "brevis.workbench." + name;
  }

  function remember(name, value) {
    try {
      localStorage.setItem(key(name), value);
    } catch (e) {
      // Private browsing, blocked storage, a full quota. The chrome works;
      // it just forgets. That is not worth an error on somebody's screen.
    }
  }

  function recall(name) {
    try {
      return localStorage.getItem(key(name));
    } catch (e) {
      return null;
    }
  }

  function wireChrome() {
    var bench = document.querySelector("[data-workbench]");
    if (!bench) return;

    var rail = recall("rail");
    if (rail) bench.style.setProperty("--rail", rail);
    var editor = recall("editor");
    if (editor) bench.style.setProperty("--editor", editor);
    if (recall("rail-collapsed") === "true") fold(bench, "rail", true);

    document.addEventListener("click", function (ev) {
      if (!ev.target.closest) return;
      if (ev.target.closest("[data-rail-toggle]")) {
        ev.preventDefault();
        fold(bench, "rail", bench.getAttribute("data-rail-collapsed") !== "true");
      }
    });

    bench.querySelectorAll("[data-grip]").forEach(function (grip) {
      grip.addEventListener("pointerdown", function (ev) { drag(bench, grip, ev); });
      // A SEPARATOR IS FOCUSABLE, so it has to answer the arrow keys. A
      // control only a mouse can reach is a control half the people cannot
      // use, and `role="separator"` with `tabindex` promises this.
      grip.addEventListener("keydown", function (ev) {
        var step = ev.key === "ArrowLeft" || ev.key === "ArrowUp" ? -16
          : ev.key === "ArrowRight" || ev.key === "ArrowDown" ? 16 : 0;
        if (!step) return;
        ev.preventDefault();
        nudge(bench, grip, step);
      });
    });
  }

  // THE FOLD: an attribute on the workbench, the ARIA on every control that
  // drives it, and a line in storage. The object tree is the one rail left.
  function fold(bench, which, shut) {
    bench.setAttribute("data-" + which + "-collapsed", shut ? "true" : "false");
    bench.querySelectorAll("[data-" + which + "-toggle]").forEach(function (b) {
      b.setAttribute("aria-expanded", shut ? "false" : "true");
    });
    remember(which + "-collapsed", shut ? "true" : "false");
  }

  function drag(bench, grip, ev) {
    ev.preventDefault();
    grip.setAttribute("data-dragging", "true");
    grip.setPointerCapture(ev.pointerId);
    var move = function (e) { place(bench, grip, e.clientX, e.clientY); };
    var stop = function () {
      grip.removeAttribute("data-dragging");
      grip.removeEventListener("pointermove", move);
      grip.removeEventListener("pointerup", stop);
      grip.removeEventListener("pointercancel", stop);
    };
    grip.addEventListener("pointermove", move);
    grip.addEventListener("pointerup", stop);
    grip.addEventListener("pointercancel", stop);
  }

  function place(bench, grip, x, y) {
    var box = bench.getBoundingClientRect();
    var floor = parseInt(grip.getAttribute("data-min"), 10) || 0;
    if (grip.getAttribute("data-grip") === "rail") {
      // THE CEILING IS THE OTHER SIDE'S FLOOR. Without it the rail can eat
      // the editor, which is the same failure as dragging to zero, mirrored.
      var width = clamp(x - box.left, floor, box.width - floor * 2);
      bench.style.setProperty("--rail", width + "px");
      remember("rail", width + "px");
      return;
    }
    var main = bench.querySelector(".workbench-main");
    if (!main) return;
    var top = main.getBoundingClientRect().top;
    var height = clamp(y - top, floor, main.getBoundingClientRect().height - floor);
    bench.style.setProperty("--editor", height + "px");
    remember("editor", height + "px");
  }

  function nudge(bench, grip, step) {
    var rail = grip.getAttribute("data-grip") === "rail";
    var pane = bench.querySelector(rail ? ".workbench-rail" : ".workbench-editor");
    if (!pane) return;
    var box = pane.getBoundingClientRect();
    if (rail) place(bench, grip, bench.getBoundingClientRect().left + box.width + step, 0);
    else place(bench, grip, 0, box.top + box.height + step);
  }

  function clamp(v, lo, hi) {
    if (hi < lo) return lo;
    return v < lo ? lo : v > hi ? hi : v;
  }

  // THE ANSWER: two tabs and a pager over rows that already arrived.
  //
  // NO SECOND QUERY, EVER. Paging here is `hidden` on rows the server
  // already drew -- a warehouse is not asked again because somebody wanted
  // rows 51 to 100, which is the whole reason the limit exists.
  //
  // WIRED FROM THE ANSWER'S OWN NODE. The tabs are one delegated listener
  // that finds its pane from the tab clicked; the pager is bound to the pane
  // it is handed, so an answer that replaces this one brings a pager that is
  // its own, with no listener left behind on the old.
  function answer() {
    return document.querySelector("[data-answer]") || document;
  }

  // THE TABS ARE OVER THE ANSWER AND OUTLIVE IT (screen-sql T3): Results
  // and JSON switch the panels inside whatever answer is drawn; History hides
  // the answer and shows the list kept in this browser.
  function wireResultTabs() {
    document.addEventListener("click", function (ev) {
      var tab = ev.target.closest ? ev.target.closest("[data-results-tabs] [data-tab]") : null;
      if (!tab) return;
      ev.preventDefault();
      showTab(tab.getAttribute("data-tab"));
    });
  }

  function showTab(want) {
    document.querySelectorAll("[data-results-tabs] [data-tab]").forEach(function (t) {
      t.setAttribute("aria-selected", t.getAttribute("data-tab") === want ? "true" : "false");
    });
    var root = answer();
    // NOT `history`: that name is the browser's address-bar API, which this
    // file never touches, and a test holds it to that by its name.
    var kept = document.querySelector('[data-panel="history"]');
    if (kept) kept.hidden = want !== "history";
    if (root !== document) root.hidden = want === "history";
    root.querySelectorAll("[data-panel]").forEach(function (p) {
      if (p.getAttribute("data-panel") !== "history") p.hidden = p.getAttribute("data-panel") !== want;
    });
    // THE PAGER BELONGS TO THE GRID. On the JSON tab there is one block
    // of text and nothing to page through, so offering the control would
    // be offering a control that does nothing.
    var panel = root.querySelector("[data-pager]");
    if (panel) panel.hidden = want !== "results" || pagerRows(root).length <= perPage(panel);
  }

  function perPage(panel) {
    var pick = panel.querySelector("[data-per-page-pick]");
    return parseInt(pick ? pick.value : panel.getAttribute("data-per-page"), 10) || 50;
  }

  function pagerRows(root) {
    return Array.prototype.slice.call(root.querySelectorAll("[data-panel='results'] [data-row]"));
  }

  function wirePager(root) {
    var panel = root.querySelector("[data-pager]");
    if (!panel) return;
    var at = 0;

    function draw() {
      var all = pagerRows(root);
      var size = perPage(panel);
      var last = Math.max(0, Math.ceil(all.length / size) - 1);
      if (at > last) at = last;
      all.forEach(function (row, i) {
        row.hidden = i < at * size || i >= (at + 1) * size;
      });
      var from = all.length === 0 ? 0 : at * size + 1;
      var to = Math.min((at + 1) * size, all.length);
      var range = panel.querySelector("[data-pager-range]");
      // THE TOTAL COMES FROM THE SERVER, carrying its own `+` when a limit
      // cut the answer. The rule for that lives in Go, where a test can read
      // it; this only prints what it was handed.
      // AND THE WORDS ARE THE PAGE'S: "{from}–{to} of {total}" in the
      // reader's language, filled here.
      if (range) range.textContent = (panel.getAttribute("data-range") || "{from}\u2013{to} / {total}")
        .replace("{from}", from).replace("{to}", to).replace("{total}", panel.getAttribute("data-total"));
      var prev = panel.querySelector("[data-pager-prev]");
      var next = panel.querySelector("[data-pager-next]");
      if (prev) prev.disabled = at === 0;
      if (next) next.disabled = at >= last;
      panel.hidden = all.length <= size;
    }

    panel.addEventListener("click", function (ev) {
      if (ev.target.closest("[data-pager-prev]")) { at = Math.max(0, at - 1); draw(); }
      if (ev.target.closest("[data-pager-next]")) { at += 1; draw(); }
    });
    var pick = panel.querySelector("[data-per-page-pick]");
    if (pick) pick.addEventListener("change", function () { at = 0; draw(); });
    draw();
  }

  // COPY, WHICH IS THE EDITOR'S TEXT. The button is hidden in the markup:
  // what is in the editor is only here (the textarea is written back on
  // submit), so without this file the button would copy nothing true.
  function wireCopy() {
    var btn = document.querySelector("[data-copy-statement]");
    if (!btn || !navigator.clipboard) return;
    btn.addEventListener("click", function () {
      navigator.clipboard.writeText(text()).then(function () {
        // THE WORD IS THE PAGE'S, in the reader's language: this file
        // writes no text of its own.
        var said = btn.getAttribute("data-copied-label") || "";
        btn.setAttribute("aria-label", said);
        btn.setAttribute("title", said);
        setTimeout(function () {
          btn.removeAttribute("aria-label");
          btn.removeAttribute("title");
        }, 1500);
      });
    });
  }

  // THE ANSWER AS CSV, BUILT FROM WHAT ARRIVED.
  //
  // No second query and no route: the JSON panel the server drew holds the
  // columns in order and the rows as arrays, which is everything a CSV is.
  // RFC 4180: a field is quoted when it carries a comma, a quote or a line
  // break, quotes are doubled, records end in CRLF, and NULL is an empty
  // field. A byte-order mark leads, because the spreadsheet most people
  // will open this in reads UTF-8 only when told to.
  //
  // The button is in the answer's markup, shown where a script runs
  // (data-js); the click is one delegated listener that finds its own answer,
  // like the tabs.

  function csvField(v) {
    if (v === null || v === undefined) return "";
    var s = typeof v === "object" ? JSON.stringify(v) : String(v);
    return /[",\r\n]/.test(s) ? '"' + s.replace(/"/g, '""') + '"' : s;
  }

  function wireCSV() {
    document.addEventListener("click", function (ev) {
      var btn = ev.target.closest ? ev.target.closest("[data-export-csv]") : null;
      if (!btn) return;
      var root = btn.closest("[data-answer]") || document;
      var panel = root.querySelector("[data-panel='json']");
      var data;
      try { data = JSON.parse(panel ? panel.textContent : ""); } catch (e) { return; }
      var lines = [data.columns.map(csvField).join(",")];
      data.rows.forEach(function (row) { lines.push(row.map(csvField).join(",")); });
      var blob = new Blob(["\ufeff" + lines.join("\r\n") + "\r\n"], { type: "text/csv;charset=utf-8" });
      var a = document.createElement("a");
      a.href = URL.createObjectURL(blob);
      a.download = "query-" + new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19) + ".csv";
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(function () { URL.revokeObjectURL(a.href); }, 1000);
    });
  }

  // A RUN, WITHOUT THROWING THE WORKBENCH AWAY.
  //
  // The same POST the form makes, with X-Brevis-Fragment: answer, so the
  // server draws the answer pane alone -- and asks the warehouse only the
  // statement. The pane is swapped and wired again (its pager, its Recent
  // entry); the editor, the tabs, the tree and the scroll are never touched.
  //
  // THE FORM IS STILL THE MECHANISM. Anything this cannot handle -- no
  // network, a non-2xx, a redirect (an expired session lands on /login, and
  // its page must not be drawn inside the pane) -- submits the form for
  // real, which is what a Run was before this existed. `form.submit()` does
  // not fire the submit event, so it cannot come back here.
  //
  // The body is built with encodeURIComponent, as the cost line's is: it
  // carries SQL, and SQL does not go near the address bar.
  function runInPlace(form, pane) {
    var parts = [];
    new FormData(form).forEach(function (value, name) {
      if (typeof value === "string") {
        parts.push(encodeURIComponent(name) + "=" + encodeURIComponent(value));
      }
    });
    var statement = text().trim();
    var ctl = new AbortController();
    var started = Date.now();
    var status = null;
    var tick = null;
    var button = document.querySelector("[data-run]");
    running = true;
    pane.setAttribute("aria-busy", "true");
    if (button) button.disabled = true;

    // NOTHING FOR THE FIRST 120ms. Most answers arrive before that, and a
    // status that flashes for 40ms is the flicker this exists to remove.
    // After it, "Running" and a live timer go ABOVE the previous answer,
    // which stays: cancelling leaves the pane exactly as it was.
    var show = setTimeout(function () {
      var tpl = document.querySelector("[data-running-template]");
      if (!tpl) return;
      status = tpl.content.firstElementChild.cloneNode(true);
      pane.insertBefore(status, pane.firstChild);
      var elapsed = status.querySelector("[data-running-elapsed]");
      tick = setInterval(function () {
        elapsed.textContent = ((Date.now() - started) / 1000).toFixed(1) + " s";
      }, 100);
      status.querySelector("[data-stop]").addEventListener("click", function () {
        ctl.abort();
      });
    }, 120);

    fetch(form.action, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded", "X-Brevis-Fragment": "answer" },
      body: parts.join("&"),
      signal: ctl.signal,
    })
      .then(function (r) {
        if (r.redirected || !r.ok) throw new Error("not an answer");
        return r.text();
      })
      .then(function (html) {
        pane.innerHTML = html;
        wirePager(pane);
        addRecent(pane, statement);
        // A NEW ANSWER IS SHOWN: whatever tab was open, the run is what
        // somebody pressed for.
        showTab("results");
      })
      .catch(function (err) {
        // CANCELLED IS NOT FAILED. Aborting closes the request, which
        // cancels the query on the server; submitting the form now would
        // run it again.
        if (err && err.name === "AbortError") {
          if (editor) editor.focus();
          return;
        }
        form.submit();
      })
      .then(function () {
        clearTimeout(show);
        clearInterval(tick);
        if (status && status.parentNode) status.parentNode.removeChild(status);
        running = false;
        pane.removeAttribute("aria-busy");
        if (button) button.disabled = false;
      });
  }

  // TABS AND RECENT: SEVERAL STATEMENTS OPEN, AND WHAT WAS RUN.
  //
  // NEITHER REACHES A URL. The address bar is the obvious place to keep a
  // tab index -- it is how every other editor shares one -- and it is
  // exactly what this console refused: a statement in a link is a statement
  // in a proxy log, in a browser history and in a Referer header. Nothing
  // below touches `location` or `history`.
  //
  // NEITHER REACHES THE SERVER EITHER. The form posts the box, so the
  // service still sees ONE statement per submit, as it always has. The
  // other tabs and the whole of Recent stay in this browser, on this
  // machine: a second browser starts empty, and clearing site data empties
  // both. A server store would need a user model, and without one it is a
  // shared mutable list with no owner.
  //
  // Their declarations are at the top of this file, with the reason.

  function text() {
    return editor ? editor.getValue() : area.value;
  }

  function put(value) {
    if (editor) editor.setValue(value);
    else area.value = value;
  }

  function parse(raw, fallback) {
    try {
      var out = JSON.parse(raw);
      return out && out.length !== undefined ? out : fallback;
    } catch (e) {
      // Cleared site data, a half-written entry, a storage that refuses.
      // An empty workbench is a workbench; a broken one is not.
      return fallback;
    }
  }

  function here() {
    for (var i = 0; i < tabs.length; i++) if (tabs[i].id === at) return tabs[i];
    return null;
  }

  function keep() {
    if (pending) clearTimeout(pending);
    pending = setTimeout(store, 400);
    estimate();
  }

  function store() {
    var tab = here();
    if (tab) tab.text = text();
    remember("tabs", JSON.stringify(tabs));
    remember("tab", at);
  }

  function wireTabs() {
    var strip = document.querySelector("[data-tabs]");
    var tpl = document.querySelector("[data-tab-template]");
    if (!strip || !tpl) return;

    tabs = parse(recall("tabs"), []);
    at = recall("tab") || "";
    if (tabs.length === 0) tabs = [{ id: fresh(), name: "1", text: echoed }];
    if (!here()) at = tabs[0].id;

    // THE SERVER'S ECHO WINS WHEN THERE IS ONE. What is in the box after a
    // submit is the statement that just ran, and the copy in storage is
    // older by definition. With nothing echoed, the tab fills the box --
    // which is what makes three tabs survive a reload.
    //
    // BUT A LINK'S SELECT IS AN OFFER, NOT A RUN (screen-sql T1): it takes
    // the open tab only when that tab is empty, and a new tab otherwise.
    if (echoed !== "" && area.hasAttribute("data-preloaded") && here().text.trim() !== "" && here().text !== echoed) {
      put(here().text);
      drawTabs();
      openTab(echoed);
      return wireTabClicks();
    }
    if (echoed !== "") here().text = echoed;
    else put(here().text);

    drawTabs();
    store();
    wireTabClicks();
  }

  function wireTabClicks() {
    document.addEventListener("click", function (ev) {
      if (!ev.target.closest) return;
      var pick = ev.target.closest("[data-tab-pick]");
      if (pick) {
        ev.preventDefault();
        goTo(idOf(pick));
        return;
      }
      var shut = ev.target.closest("[data-tab-close]");
      if (shut) {
        ev.preventDefault();
        closeTab(idOf(shut));
        return;
      }
      if (ev.target.closest("[data-tab-new]")) {
        ev.preventDefault();
        openTab("");
      }
    });
  }

  function idOf(node) {
    var box = node.closest("[data-tab-id]");
    return box ? box.getAttribute("data-tab-id") : "";
  }

  function fresh() {
    return "t" + Date.now().toString(36) + Math.random().toString(36).slice(2, 6);
  }

  // The name is derived and never renumbered: closing the second of three
  // tabs must not rename the third, or the tab somebody was looking for
  // becomes the tab they just closed.
  //
  // WHAT IS STORED IS THE NUMBER, and the word around it is the page's
  // (`data-tab-name`, "Consulta {n}"), so a tab reads in the language the
  // console is in today -- and a tab stored as "Query 2" before this still
  // reads as its number.
  function named() {
    var top = 0;
    tabs.forEach(function (t) {
      var n = numberOf(t.name);
      if (n > top) top = n;
    });
    return String(top + 1);
  }

  function numberOf(name) {
    return parseInt(String(name).replace(/\D+/g, ""), 10) || 0;
  }

  function label(name) {
    var strip = document.querySelector("[data-tabs]");
    var pattern = (strip && strip.getAttribute("data-tab-name")) || "{n}";
    return pattern.replace("{n}", String(numberOf(name)));
  }

  function goTo(id) {
    if (!id || id === at) return;
    store();
    at = id;
    put(here().text);
    drawTabs();
    store();
    // A different tab is a different statement, so it has a different price.
    estimate();
  }

  function openTab(value) {
    store();
    var tab = { id: fresh(), name: named(), text: value || "" };
    tabs.push(tab);
    at = tab.id;
    put(tab.text);
    drawTabs();
    store();
  }

  // THE LAST TAB IS EMPTIED, NOT REMOVED. A strip with no tabs over an
  // editor with text is a screen that cannot say what it is showing.
  function closeTab(id) {
    var i = -1;
    for (var n = 0; n < tabs.length; n++) if (tabs[n].id === id) i = n;
    if (i < 0) return;
    if (tabs.length === 1) {
      put("");
      drawTabs();
      store();
      return;
    }
    tabs.splice(i, 1);
    if (at === id) {
      at = tabs[Math.min(i, tabs.length - 1)].id;
      put(here().text);
    }
    drawTabs();
    store();
  }

  // THE MARKUP IS CLONED FROM THE PAGE. W1's rule: two pieces of markup for
  // one thing is how the classes and the ARIA drift apart, and only one of
  // the two is somewhere the Go suite can read it.
  function drawTabs() {
    var strip = document.querySelector("[data-tabs]");
    var tpl = document.querySelector("[data-tab-template]");
    if (!strip || !tpl) return;
    var add = strip.querySelector("[data-tab-new]");
    strip.querySelectorAll("[data-tab-id]").forEach(function (old) {
      strip.removeChild(old);
    });
    tabs.forEach(function (t) {
      var node = tpl.content.firstElementChild.cloneNode(true);
      node.setAttribute("data-tab-id", t.id);
      var pick = node.querySelector("[data-tab-pick]");
      pick.textContent = label(t.name);
      pick.setAttribute("aria-selected", t.id === at ? "true" : "false");
      node.querySelector("[data-tab-close]").hidden = tabs.length < 2;
      strip.insertBefore(node, add);
    });
  }

  function wireRecent() {
    recent.rail = document.querySelector("[data-recent]");
    recent.tpl = document.querySelector("[data-recent-template]");
    if (!recent.rail || !recent.tpl) return;
    // THE CAP COMES FROM THE MARKUP, like the grips' floor: a number that
    // lived only here is one the next person changes with nothing on the
    // page to say what it was.
    recent.cap = parseInt(recent.rail.getAttribute("data-recent-keep"), 10) || 20;
    recent.list = parse(recall("recent"), []);

    addRecent(answer(), echoed);

    document.addEventListener("click", function (ev) {
      var btn = ev.target.closest ? ev.target.closest("[data-recent-open]") : null;
      if (!btn) return;
      ev.preventDefault();
      var entry = recent.list[parseInt(btn.getAttribute("data-recent-at"), 10)];
      // IN A NEW TAB, never over what is in the box: the statement somebody
      // is in the middle of writing is not a thing to overwrite with one
      // click.
      if (entry) openTab(entry.text);
    });
  }

  // WHAT JUST RAN, FROM THE ANSWER'S OWN FACTS: the outcome marker in the
  // pane it is handed, and the statement that produced it.
  //
  // A reload of this screen re-submits and genuinely runs the query again,
  // so a second entry is not a duplicate -- it is a second run, and it was
  // paid for.
  function addRecent(root, statement) {
    if (!recent.rail || !recent.tpl) return;
    var ran = root.querySelector("[data-outcome]");
    if (ran && statement !== "") {
      recent.list.unshift({
        text: statement,
        outcome: ran.getAttribute("data-outcome"),
        // The words the server already wrote. Formatting bytes again here
        // would be a second rounder, and two rounders disagree eventually.
        cost: ran.getAttribute("data-cost") || "",
      });
      if (recent.list.length > recent.cap) recent.list.length = recent.cap;
      remember("recent", JSON.stringify(recent.list));
    }
    drawRecent(recent.rail, recent.tpl, recent.list);
  }

  function drawRecent(rail, tpl, list) {
    var ol = rail.querySelector("[data-recent-list]");
    var empty = rail.querySelector("[data-recent-empty]");
    if (!ol) return;
    ol.textContent = "";
    list.forEach(function (entry, i) {
      var node = tpl.content.firstElementChild.cloneNode(true);
      var btn = node.querySelector("[data-recent-open]");
      btn.setAttribute("data-recent-at", String(i));
      var ok = entry.outcome === "ok";
      node.querySelector("[data-recent-ok]").hidden = !ok;
      node.querySelector("[data-recent-failed]").hidden = ok;
      node.querySelector("[data-recent-cost]").textContent = entry.cost || "";
      node.querySelector("[data-recent-text]").textContent = entry.text;
      ol.appendChild(node);
    });
    if (empty) empty.hidden = list.length > 0;
    var count = document.querySelector("[data-history-count]");
    if (count) count.textContent = String(list.length);
  }

  // THE COST LINE: WHAT THIS WOULD PROCESS, BEFORE ANYBODY PRESSES RUN.
  //
  // A POST, AND THE STATEMENT IS IN THE BODY. It is the only call this file
  // makes that carries SQL, and a statement in a URL is a statement in a
  // proxy log, in a browser history and in a Referer header. The body is
  // built with encodeURIComponent rather than URLSearchParams -- the tool a
  // reader would reach for, and the one this island refuses to name,
  // because the next use of it is the one that writes the address bar.
  //
  // THE PAUSE COMES FROM THE MARKUP. A dry run costs no money on BigQuery
  // and still counts against the client's own API quota, so the number sits
  // on the page where somebody can find it, beside the grips' floor and the
  // Recent rail's cap.
  //
  // AND THE SAME TEXT IS NEVER PRICED TWICE. Moving the cursor, switching
  // to a tab and back, a reload -- none of them changed the statement, and
  // none of them should spend a call.
  function wireCost() {
    costLine = document.querySelector("[data-cost]");
    if (!costLine) return;
    costAfter = parseInt(costLine.getAttribute("data-estimate-after"), 10) || 600;
    // NOT PRICED ON LOAD, deliberately. What ran has its REAL cost under the
    // grid already, and what did not is a statement nobody has touched yet:
    // an API call on every page view to quote somebody the thing they are
    // about to edit is the console paying for nothing.
    priced = echoed;
  }

  function estimate() {
    if (!costLine) return;
    if (costWait) clearTimeout(costWait);
    costWait = setTimeout(ask, costAfter);
  }

  function ask() {
    var hidden = document.querySelector('#workbench input[name="target"]');
    var target = hidden ? hidden.value : "";
    var statement = text().trim();
    if (statement === "" || target === "") {
      costLine.innerHTML = "";
      priced = "";
      return;
    }
    if (statement === priced) return;
    priced = statement;

    fetch("/api/sql/estimate", {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded", Accept: "text/html" },
      body: "target=" + encodeURIComponent(target) + "&q=" + encodeURIComponent(statement),
    })
      .then(function (r) { return r.ok ? r.text() : ""; })
      .then(function (html) { costLine.innerHTML = html; })
      // A SERVICE THAT COULD NOT BE REACHED SAYS NOTHING ABOUT THE SQL. The
      // line goes quiet rather than red: it sits under somebody's
      // half-written statement, and a warning there that is really about a
      // token is a warning they learn to ignore.
      .catch(function () { costLine.innerHTML = ""; });
  }

  // An attribute value goes into a selector, and a target holds `/` and `:`.
  function css(value) {
    return window.CSS && CSS.escape ? CSS.escape(value) : value.replace(/["\\]/g, "\\$&");
  }
})();
