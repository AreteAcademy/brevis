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
  if (!area || typeof CodeMirror === "undefined") return;

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
})();
