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

  // The tree is wired whether or not CodeMirror loaded, so a blocked CDN
  // costs the highlighting and not the screen.
  wireTree();
  if (typeof CodeMirror === "undefined") return;

  var editor = CodeMirror.fromTextArea(area, {
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
    },
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

  // The textarea is what the handler reads, so it has to hold the text at
  // the moment of submit and not at the moment somebody stopped typing.
  if (area.form) {
    area.form.addEventListener("submit", function () {
      editor.save();
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
  // just the DOM, so three relations in three schemas stay open at once --
  // which is the complaint this slice exists for.
  function wireTree() {
    document.addEventListener("click", function (ev) {
      var el = ev.target.closest ? ev.target.closest("[data-insert]") : null;
      if (el) {
        ev.preventDefault();
        insert(el.getAttribute("data-insert"));
        return;
      }
      var btn = ev.target.closest ? ev.target.closest("button[name]") : null;
      if (!btn) return;
      if (btn.name === "expand") {
        ev.preventDefault();
        openRelation(btn);
      } else if (btn.name === "connect") {
        ev.preventDefault();
        openConnection(btn);
      }
    });

    var box = document.querySelector("[data-tree-search]");
    if (box) box.addEventListener("input", function () { filter(box.value); });
  }

  function insert(name) {
    if (editor) {
      editor.replaceSelection(name);
      editor.focus();
      return;
    }
    // No editor: the textarea itself, at its own cursor.
    var at = area.selectionStart === null ? area.value.length : area.selectionStart;
    var to = area.selectionEnd === null ? at : area.selectionEnd;
    area.value = area.value.slice(0, at) + name + area.value.slice(to);
    area.selectionStart = area.selectionEnd = at + name.length;
    area.focus();
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

  // A connection's schemas. Opening one also makes it the connection a query
  // runs on, which the label beside Run says out loud -- an invisible change
  // of warehouse is the one thing a picker was good at preventing.
  function openConnection(btn) {
    var target = btn.value;
    var slot = document.querySelector('[data-objects="' + css(target) + '"]');
    if (!slot) return;
    var hidden = document.querySelector('#workbench input[name="target"]');
    if (hidden) hidden.value = target;
    var node = btn.closest("[data-connection]");
    var label = document.querySelector("[data-running-on]");
    if (label && node) label.textContent = node.getAttribute("data-connection");
    toggle(btn, slot, "/api/sql/objects?target=" + encodeURIComponent(target), node);
  }

  function toggle(btn, slot, url, node) {
    var arrow = btn.querySelector("[data-arrow]");
    if (slot.innerHTML.trim() !== "") {
      var shut = slot.hidden;
      slot.hidden = !shut;
      btn.setAttribute("aria-expanded", shut ? "true" : "false");
      if (arrow) arrow.textContent = shut ? "\u25BE" : "\u25B8";
      return;
    }
    btn.setAttribute("aria-busy", "true");
    fetch(url, { headers: { Accept: "text/html" } })
      .then(function (r) { return r.ok ? r.text() : ""; })
      .then(function (html) {
        // AN EMPTY ANSWER IS AN ANSWER. A warehouse that cannot list or
        // cannot describe is not an error here either -- the server draws
        // the sentence that says so, and a refusal draws nothing rather
        // than leaving the button spinning.
        slot.innerHTML = html;
        slot.hidden = false;
        btn.setAttribute("aria-expanded", "true");
        if (arrow) arrow.textContent = "\u25BE";
        if (node) node.setAttribute("data-loaded", "true");
      })
      .catch(function () { slot.innerHTML = ""; })
      .then(function () { btn.removeAttribute("aria-busy"); });
  }

  // THE FILTER SEES WHAT IS LOADED, AND SAYS WHEN THAT IS NOT EVERYTHING.
  //
  // A connection nobody opened holds names this cannot read. A search that
  // quietly skipped one would be how somebody concludes a table does not
  // exist, so the ones it could not look inside are named instead.
  function filter(term) {
    var q = term.trim().toLowerCase();
    var unsearched = [];
    document.querySelectorAll("[data-connection][data-loaded]").forEach(function (node) {
      if (node.getAttribute("data-loaded") !== "true") {
        unsearched.push(node.getAttribute("data-connection"));
      }
      node.querySelectorAll("[data-relation]").forEach(function (row) {
        var name = row.getAttribute("data-relation").toLowerCase();
        row.hidden = q !== "" && name.indexOf(q) < 0;
      });
      node.querySelectorAll("details").forEach(function (d) {
        var any = false;
        d.querySelectorAll("[data-relation]").forEach(function (row) { if (!row.hidden) any = true; });
        d.hidden = q !== "" && !any;
        if (any) d.open = true;
      });
    });
    var note = document.querySelector("[data-tree-unsearched]");
    if (!note) return;
    if (q === "" || unsearched.length === 0) {
      note.hidden = true;
      return;
    }
    note.hidden = false;
    note.textContent = "Not searched, because nothing has been loaded from " +
      (unsearched.length === 1 ? "it" : "them") + ": " + unsearched.join(", ") + ".";
  }

  // An attribute value goes into a selector, and a target holds `/` and `:`.
  function css(value) {
    return window.CSS && CSS.escape ? CSS.escape(value) : value.replace(/["\\]/g, "\\$&");
  }
})();
