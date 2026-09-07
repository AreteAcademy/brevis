// The DAG's interactive island (section 20 of the plan).
//
// Written in plain JS, with no JSX and no bundler, on purpose: section 15
// forbids Node in the build ("standalone Tailwind, no npm"), and a transpile
// step just for this screen would bring the whole toolchain back.
// `React.createElement` is verbose, but the cost stays on this one page.
//
// React Flow is NOT the source of truth: node positions, edges and state arrive
// ready from the server, computed by the same `graph.Niveis` the executor uses.
// Here they are only drawn.
(function () {
  "use strict";

  // The keys read off the payload below -- `nome`, `estado`, `numeros`,
  // `rotulo`, `acao`, `erro`, `tentativa`, `duracao_ms`, and the node type
  // `etapa` -- stay Portuguese on purpose while this file is English.
  //
  // They are the wire between internal/api/graph.go and this island. Renaming
  // them is safe only if both sides move together, and both sides DO ship in
  // one image -- but a browser holding the page across a deploy keeps the old
  // script and gets the new JSON, and every stage box loses its name until
  // somebody reloads. That is a poor trade for eight words, so they move when
  // there is a reason to touch the payload anyway.

  var h = React.createElement;
  var RF = window.ReactFlow;

  // The colours are READ from the CSS, not repeated here. An installation can
  // have a theme of its own (see internal/branding), and a fixed palette in this
  // file would make the DAG island the one part of the screen that ignores the
  // customization.
  //
  // Resolved once, at load: a getComputedStyle on every re-rendered node would
  // force a style recalculation in the middle of drawing the graph.
  function themeVar(cssVar, fallback) {
    try {
      var v = getComputedStyle(document.documentElement).getPropertyValue(cssVar);
      return v.trim() || fallback;
    } catch (e) {
      return fallback;
    }
  }

  var INK = themeVar("--color-ink", "#21180f");
  var MUTED = themeVar("--color-muted", "#6e6254");
  var SURFACE = themeVar("--color-surface", "#fffdf8");
  var LINE = themeVar("--color-line", "#21180f1a");
  var GOLD = themeVar("--color-gold", "#aa8450");

  var COLOURS = {
    success: { ring: themeVar("--color-state-success", "#4c7a56"), label: "success" },
    failed: { ring: themeVar("--color-state-failed", "#b0503c"), label: "failed" },
    running: { ring: themeVar("--color-state-running", "#3f6d8f"), label: "running" },
    retrying: { ring: themeVar("--color-state-retrying", "#a35f28"), label: "retrying" },
    queued: { ring: themeVar("--color-state-queued", "#b3822f"), label: "queued" },
    canceled: { ring: themeVar("--color-state-canceled", "#8a8175"), label: "canceled" },
    pending: { ring: themeVar("--color-state-pending", "#c9bfae"), label: "pending" },
  };

  function colour(status) {
    return COLOURS[status] || COLOURS.pending;
  }

  // A STAGE's state reuses the steps' palette: a stage's `done` has to be the
  // same green as a step's `success`, or the screen teaches two colour grammars
  // for the same idea.
  var STAGE_STATE_COLOURS = { done: "success", running: "running", failed: "failed", aborted: "canceled" };

  function stageColour(state) {
    return colour(STAGE_STATE_COLOURS[state] || "pending");
  }

  // ---------------------------------------------------------------------------
  // What the step runs in
  // ---------------------------------------------------------------------------

  // The runtime palette, read from the CSS like every other colour here, so an
  // installation with its own theme (internal/branding) can move them. The
  // fallbacks are each language's own convention, which is what makes the chip
  // readable before anybody reads the label.
  function langColour(id) {
    return themeVar("--color-lang-" + id, LANG_FALLBACK[id] || MUTED);
  }

  var LANG_FALLBACK = {
    python: "#3572a5", go: "#00add8", node: "#3c873a", java: "#b07219",
    rust: "#a04b28", php: "#4f5b93", ruby: "#9b1c22", dotnet: "#5c2d91",
    sql: "#5a6b7b", shell: "#6e6254",
  };

  // The names a person reads. The payload carries ids -- stable keys shared with
  // the YAML and the CSS -- and an id the screen has no label for falls back to
  // itself rather than rendering blank.
  var LANG_LABEL = {
    python: "Python", go: "Go", node: "Node.js", java: "Java", rust: "Rust",
    php: "PHP", ruby: "Ruby", dotnet: ".NET", sql: "SQL", shell: "Shell",
    dbt: "dbt", spark: "Spark", airbyte: "Airbyte", soda: "Soda",
    sqlmesh: "SQLMesh", meltano: "Meltano", duckdb: "DuckDB",
    pandas: "pandas", polars: "Polars", airflow: "Airflow",
    terraform: "Terraform",
  };

  function labelOf(id) { return LANG_LABEL[id] || id; }

  // chip draws one runtime or tool.
  //
  // `inferred` is not decoration. A dotted border and a title saying so is the
  // difference between "this step runs Python" and "this command starts with
  // python", and the screen has to keep them apart: the SDK badge two lines up
  // is trustworthy because it is OBSERVED and cannot lie, and anything drawn
  // beside it borrows that credibility.
  function chip(id, opts) {
    var strong = opts.strong;
    var tint = strong ? langColour(id) : MUTED;
    return h(
      "span",
      {
        key: id,
        title: opts.title,
        style: {
          display: "inline-flex", alignItems: "center", gap: 4,
          padding: "1px 7px", borderRadius: 999,
          border: (opts.inferred ? "1px dashed " : "1px solid ") +
            "color-mix(in srgb, " + tint + " 45%, transparent)",
          background: strong
            ? "color-mix(in srgb, " + tint + " 12%, transparent)"
            : "transparent",
          color: tint,
          fontSize: 10, fontWeight: strong ? 700 : 600,
          whiteSpace: "nowrap",
        },
      },
      labelOf(id)
    );
  }

  // contextCount is what a step told the steps below it, on the card.
  //
  // A COUNT and not the object: the card has room for one line, and a JSON blob
  // would swallow it. The values are in the panel, which is where somebody goes
  // when the count surprises them.
  //
  // Nothing at all when the step published nothing, which is most steps -- so
  // their card stays exactly what it was before this existed. No "0 published",
  // no reserved space.
  function contextCount(d) {
    var keys = d.context ? Object.keys(d.context) : [];
    if (!keys.length) return null;
    return h(
      "div",
      {
        title: "click the step to see what it published",
        style: { marginTop: 6, fontSize: 10, color: MUTED },
      },
      keys.length + (keys.length === 1 ? " value published" : " values published")
    );
  }

  // chipRow is the runtime and its tools, or nothing at all.
  //
  // Nothing at all is the point: a step the engine cannot read renders exactly
  // as it did before this feature -- no empty row, no reserved space, no chip
  // saying "unknown".
  function chipRow(d) {
    var tools = d.tools || [];
    if (!d.runtime && !tools.length) return null;

    var inferred = d.runtime_source === "inferred";
    var why = inferred ? "inferred from the command" : d.runtime_source;
    var out = [];
    if (d.runtime) {
      out.push(chip(d.runtime, { strong: true, inferred: inferred, title: labelOf(d.runtime) + " — " + why }));
    }
    for (var i = 0; i < tools.length; i++) {
      out.push(chip(tools[i], { strong: false, inferred: inferred, title: labelOf(tools[i]) + " — " + why }));
    }
    return h("div", { style: { marginTop: 7, display: "flex", gap: 5, flexWrap: "wrap" } }, out);
  }

  // summarise condenses a stage's numbers to fit on one line. The whole detail
  // stays in the panel; only what is read at a glance fits here.
  function summarise(numbers) {
    if (!numbers) return "";
    var keys = Object.keys(numbers);
    if (!keys.length) return "";
    var parts = [];
    for (var i = 0; i < keys.length && parts.length < 2; i++) {
      var v = numbers[keys[i]];
      if (v === null || v === undefined || v === "") continue;
      parts.push(typeof v === "number" ? v.toLocaleString("en-US") : String(v));
    }
    return parts.join(" · ");
  }

  function formatDuration(ms) {
    if (!ms) return "";
    if (ms < 1000) return ms + "ms";
    if (ms < 60000) return (ms / 1000).toFixed(1) + "s";
    return Math.floor(ms / 60000) + "m" + Math.round((ms % 60000) / 1000) + "s";
  }

  // NoBravis is a step's card. A custom node rather than the default because the
  // default only shows a label — and what the operator needs to know during an
  // incident is the state, the duration and whether there was a retry.
  function BrevisNode(props) {
    var d = props.data;
    var c = colour(d.status);
    return h(
      "div",
      {
        style: {
          minWidth: 190,
          borderRadius: 14,
          border: "1px solid " + (d.status === "pending" ? LINE : "color-mix(in srgb, " + c.ring + " 40%, transparent)"),
          background: SURFACE,
          padding: "11px 13px",
          fontFamily: '"Inter", ui-sans-serif, system-ui, sans-serif',
          // `color-mix` rather than concatenating alpha onto the hex: the
          // colour may come from a resolved CSS variable, and "var(--x)1f" is no
          // colour at all.
          boxShadow:
            d.status === "running"
              ? "0 0 0 3px color-mix(in srgb, " + c.ring + " 14%, transparent), 0 8px 24px rgba(33,24,15,.06)"
              : "0 8px 24px rgba(33,24,15,.06)",
        },
      },
      h(RF.Handle, { type: "target", position: RF.Position.Left, style: { background: c.ring } }),
      h(
        "div",
        { style: { display: "flex", alignItems: "center", gap: 8 } },
        h("span", {
          style: {
            width: 8, height: 8, borderRadius: 9999, background: c.ring, flexShrink: 0,
          },
        }),
        h("span", { style: { color: INK, fontSize: 13, fontWeight: 600 } }, d.label),
        // The SDK badge. An ACCENT colour, never a state one: the state
        // colours
        // mean "how it went", and a badge painted green would say something it
        // does not know.
        //
        // It carries the VERSION. A badge that only said "SDK" would be true
        // and useless; with the version the screen answers "why does this step
        // behave differently from its neighbour" without anybody opening the
        // Dockerfile.
        d.hasStages
          ? h(
              "button",
              {
                title: d.collapsed ? "show the stages" : "collapse the stages",
                onClick: function (ev) {
                  // Without this the click would also select the step, and
                  // collapsing would open the details panel along with it.
                  ev.stopPropagation();
                  d.toggle();
                },
                style: {
                  marginLeft: 2, padding: 0, width: 14, height: 14, flexShrink: 0,
                  border: "none", background: "none", cursor: "pointer",
                  color: MUTED, fontSize: 9, lineHeight: "14px",
                },
              },
              d.collapsed ? "▸" : "▾"
            )
          : null,
        d.sdk
          ? h(
              "span",
              {
                title: "built with the Brevis SDK " + d.sdk,
                style: {
                  marginLeft: "auto", flexShrink: 0,
                  padding: "1px 6px", borderRadius: 999,
                  border: "1px solid color-mix(in srgb, " + GOLD + " 45%, transparent)",
                  background: "color-mix(in srgb, " + GOLD + " 10%, transparent)",
                  color: themeVar("--color-gold-strong", "#8a693d"),
                  fontSize: 9, fontWeight: 700, letterSpacing: "0.08em",
                  textTransform: "uppercase", whiteSpace: "nowrap",
                },
              },
              "SDK " + d.sdk
            )
          : null
      ),
      d.acao
        ? h(
            "div",
            {
              style: {
                marginTop: 4, color: MUTED, fontSize: 11,
                fontFamily: "ui-monospace, SFMono-Regular, monospace",
                overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
                maxWidth: 200,
              },
            },
            d.acao
          )
        : null,
      chipRow(d),
      contextCount(d),
      h(
        "div",
        { style: { marginTop: 7, display: "flex", gap: 8, fontSize: 11, color: c.ring } },
        h("span", null, c.label),
        d.duracao_ms ? h("span", { style: { color: MUTED } }, formatDuration(d.duracao_ms)) : null,
        d.tentativa ? h("span", { style: { color: "#a35f28" } }, "retry " + d.tentativa) : null
      ),
      h(RF.Handle, { type: "source", position: RF.Position.Right, style: { background: c.ring } })
    );
  }

  // The numbers worth showing on the line, and their order. The rest stays in
  // the panel: a 20px line fits two magnitudes, not six.
  var HIGHLIGHT = ["in", "out", "groups", "rows", "pages", "objects"];

  function stageNumbers(numbers) {
    if (!numbers) return [];
    var out = [];
    for (var i = 0; i < HIGHLIGHT.length && out.length < 2; i++) {
      var k = HIGHLIGHT[i];
      var v = numbers[k];
      if (v === null || v === undefined || v === "") continue;
      out.push({ key: k, value: typeof v === "number" ? v.toLocaleString("en-US") : String(v) });
    }
    return out;
  }

  // NoEtapa is a phase INSIDE an SDK step: the source, each stage in the order
  // it runs, the target.
  //
  // A row, and not boxes side by side. Three horizontal boxes in a 230px card
  // become three illegible badges; in a row there is space for the type, the
  // identity and the number the phase produced -- which is what serves at three
  // in the morning.
  function StageNode(props) {
    var d = props.data;
    var c = stageColour(d.estado);
    var nums = stageNumbers(d.numeros);
    var detail = d.numeros && d.numeros.detail;

    return h(
      "div",
      {
        title: detail || "",
        style: {
          display: "flex", alignItems: "center", gap: 7,
          width: 210, height: 24, padding: "0 8px",
          borderRadius: 7,
          border: "1px solid " + (d.estado === "pending" ? LINE : "color-mix(in srgb, " + c.ring + " 28%, transparent)"),
          background: "color-mix(in srgb, " + c.ring + " 7%, transparent)",
          fontFamily: '"Inter", ui-sans-serif, system-ui, sans-serif',
          fontSize: 10,
          // The running stage gets the same ring a running step's card gets --
          // and not an animation of its own. Two grammars for "this is happening
          // now" on the same screen is one too many.
          boxShadow: d.estado === "running"
            ? "0 0 0 2px color-mix(in srgb, " + c.ring + " 22%, transparent)"
            : "none",
        },
      },
      h("span", {
        style: { width: 5, height: 5, borderRadius: 9999, background: c.ring, flexShrink: 0 },
      }),
      // O TIPO da fase: source, map, aggregate, target.
      h("span", {
        style: { color: INK, fontWeight: 600, letterSpacing: "0.02em", flexShrink: 0 },
      }, d.rotulo || d.nome),
      // The identity, when there is one: which source, which target. It is
      // what
      // turns "extract, 743ms" from half a card into a whole one.
      detail
        ? h("span", {
            style: {
              color: MUTED, fontSize: 9, flex: 1, minWidth: 0,
              overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
              fontFamily: "ui-monospace, SFMono-Regular, monospace",
            },
          }, detail)
        : h("span", { style: { flex: 1 } }),
      // The numbers, with their name alongside: "216" on its own says nothing
      // about what.
      h("span",
        { style: { display: "flex", gap: 6, flexShrink: 0, whiteSpace: "nowrap" } },
        nums.map(function (n) {
          return h("span", { key: n.key, style: { color: MUTED } },
            h("span", { style: { color: INK, fontVariantNumeric: "tabular-nums" } }, n.value),
            " " + n.key
          );
        }),
        d.ms !== null && d.ms !== undefined
          ? h("span", { style: { color: MUTED } }, formatDuration(d.ms))
          : null
      )
    );
  }

  var NODE_TYPES = { bravis: BrevisNode, etapa: StageNode };

  // contextRows renders what a step published, keys sorted so two visits to the
  // same run read the same way.
  //
  // Everything here is visible to anyone who can see the run, and that is the
  // reason the documentation says the context is not a secret store. The panel
  // is where that stops being an abstract warning.
  function contextRows(d) {
    if (!d.context) return [];
    return Object.keys(d.context)
      .sort()
      .map(function (k) {
        var v = d.context[k];
        // JSON.stringify only for what is not already a string: it would put
        // quotes around a path and make it look like it carries them.
        return ["context." + k, typeof v === "string" ? v : JSON.stringify(v)];
      });
  }

  // availableRows is what the engine handed this step, with the step that wrote
  // each value.
  //
  // The label says AVAILABLE and not "read", and the difference is not
  // pedantry: the engine does not observe get() calls. It knows what it put in
  // BREVIS_INPUT, not what the code asked for -- a step can be handed a value it
  // never touches. "Read" would be a claim the data does not support, and a
  // panel that overstates what it knows is worth less than one that does not.
  //
  // The provenance is the point of the arrow. On a wide DAG, "where did this
  // value come from" is the question, and without it somebody clicks through
  // every parent to find out.
  function availableRows(d) {
    if (!d.available) return [];
    var out = [];
    Object.keys(d.available)
      .sort()
      .forEach(function (step) {
        var values = d.available[step] || {};
        Object.keys(values)
          .sort()
          .forEach(function (k) {
            var v = values[k];
            out.push([
              "available " + step + "." + k,
              (typeof v === "string" ? v : JSON.stringify(v)) + "  \u2190 " + step,
            ]);
          });
      });
    return out;
  }

  // The inspector: the selected node's side panel. It appears only when there is
  // a selection — taking up fixed space with "nothing selected" shrinks the
  // graph's area for nothing.
  function Inspector(props) {
    var n = props.no;
    if (!n) return null;
    var d = n.data;
    var c = colour(d.status);
    var lines = [
      ["step", n.id],
      ["state", c.label],
      d.acao ? ["command", d.acao] : null,
      d.duracao_ms ? ["duration", formatDuration(d.duracao_ms)] : null,
      typeof d.tentativa === "number" ? ["attempt", String(d.tentativa + 1)] : null,
      typeof d.exit_code === "number" ? ["exit code", String(d.exit_code)] : null,
      // The card is cramped and the panel is not, so here the provenance is
      // spelled out. This is where somebody goes when a chip surprises them,
      // and it has to answer why without their opening the YAML.
      d.runtime ? ["runtime", labelOf(d.runtime) + " (" + (d.runtime_source || "?") + ")"] : null,
      d.tools && d.tools.length ? ["tools", d.tools.map(labelOf).join(", ")] : null,
    ]
      // What this step published, one row per key.
      //
      // The value is rendered AS IT WAS WRITTEN: a number stays a number. A
      // step reading 48213 through the SDK and seeing 48213.0 on the screen
      // would be the same class of difference this project has already paid for
      // once, in ingestion_id -- and the screen is where somebody checks their
      // assumption before touching the code.
      .concat(contextRows(d))
      .concat(availableRows(d))
      .filter(Boolean);

    return h(
      "aside",
      {
        style: {
          position: "absolute", top: 14, right: 14, width: 310, zIndex: 5,
          background: SURFACE, backdropFilter: "blur(6px)",
          border: "1px solid " + LINE, borderRadius: 20, padding: 16,
          boxShadow: "0 20px 60px rgba(33,24,15,.08)",
          fontFamily: '"Inter", ui-sans-serif, system-ui, sans-serif',
        },
      },
      h(
        "div",
        { style: { display: "flex", justifyContent: "space-between", alignItems: "center" } },
        h(
          "span",
          {
            style: {
              color: themeVar("--color-gold-strong", "#8a693d"), fontSize: 11, fontWeight: 700,
              letterSpacing: "0.14em", textTransform: "uppercase",
            },
          },
          "Detalhes"
        ),
        h(
          "button",
          {
            onClick: props.close,
            style: { background: "none", border: "none", color: MUTED, cursor: "pointer", fontSize: 16, lineHeight: 1 },
          },
          "×"
        )
      ),
      h(
        "dl",
        { style: { marginTop: 10, fontSize: 12 } },
        lines.map(function (l) {
          return h(
            "div",
            { key: l[0], style: { display: "flex", gap: 8, padding: "3px 0" } },
            h("dt", { style: { color: MUTED, width: 80, flexShrink: 0 } }, l[0]),
            h(
              "dd",
              {
                style: {
                  color: INK, margin: 0, wordBreak: "break-all",
                  fontFamily: "ui-monospace, SFMono-Regular, monospace",
                },
              },
              l[1]
            )
          );
        })
      ),
      // The stages, with EVERY number. The graph's box fits the glance; the
      // detail is here, which is where you go when the glance's line was not
      // enough.
      props.stages && props.stages.length
        ? h(
            "div",
            { style: { marginTop: 12 } },
            h(
              "div",
              {
                style: {
                  color: themeVar("--color-gold-strong", "#8a693d"), fontSize: 10, fontWeight: 700,
                  letterSpacing: "0.14em", textTransform: "uppercase", marginBottom: 6,
                },
              },
              d.sdk ? "Stages · SDK " + d.sdk : "Stages"
            ),
            props.stages.map(function (et) {
              var ce = stageColour(et.data.estado);
              var nums = et.data.numeros || {};
              return h(
                "div",
                {
                  key: et.data.nome,
                  style: {
                    padding: "5px 0", borderTop: "1px solid " + LINE,
                    display: "flex", gap: 8, alignItems: "baseline", flexWrap: "wrap",
                  },
                },
                h("span", { style: { color: ce.ring, fontSize: 11, fontWeight: 600, width: 74 } },
                  et.data.rotulo || et.data.nome),
                h("span", { style: { color: MUTED, fontSize: 11 } },
                  et.data.ms !== null && et.data.ms !== undefined ? formatDuration(et.data.ms) : "—"),
                Object.keys(nums).map(function (k) {
                  return h(
                    "span",
                    {
                      key: k,
                      style: {
                        color: INK, fontSize: 10,
                        fontFamily: "ui-monospace, SFMono-Regular, monospace",
                      },
                    },
                    k + "=" + nums[k]
                  );
                })
              );
            })
          )
        : null,
      d.erro
        ? h(
            "pre",
            {
              style: {
                marginTop: 12, padding: 10, borderRadius: 12,
                background: "#b0503c14", border: "1px solid #b0503c33",
                color: "#8f4030", fontSize: 11, whiteSpace: "pre-wrap", wordBreak: "break-word",
                maxHeight: 160, overflow: "auto",
              },
            },
            d.erro
          )
        : null
    );
  }

  function Graph(props) {
    var graphState = React.useState({ nodes: [], edges: [], loading: true, erro: "" });
    var payload = graphState[0], setPayload = graphState[1];
    var selState = React.useState(null);
    var selected = selState[0], setSelected = selState[1];

    React.useEffect(function () {
      var alive = true;
      var timer = null;

      function fetchGraph() {
        fetch(props.src, { headers: { Accept: "application/json" } })
          .then(function (r) {
            if (!r.ok) throw new Error("HTTP " + r.status);
            return r.json();
          })
          .then(function (g) {
            if (!alive) return;
            setPayload({ nodes: g.nodes || [], edges: g.edges || [], loading: false, erro: "" });
            // Live updates by polling, not by WebSocket: the data changes in
            // seconds, not in milliseconds, and a repeated GET needs neither a
            // persistent connection nor reconnection logic.
            //
            // The interval follows the state. `failed` is not terminal in the
            // domain — a retry moves it to `retrying` — but insisting every 2s
            // on a run that has probably run out of attempts is traffic for
            // nothing; 10s still catches the retry at no cost. A genuinely
            // terminal one stops polling.
            var next = g.terminal ? 0 : g.status === "failed" ? 10000 : 2000;
            if (next && g.run_id) timer = setTimeout(fetchGraph, next);
          })
          .catch(function (e) {
            if (!alive) return;
            setPayload(function (d) {
              return { nodes: d.nodes, edges: d.edges, loading: false, erro: e.message };
            });
            timer = setTimeout(fetchGraph, 5000);
          });
      }

      fetchGraph();
      return function () {
        alive = false;
        if (timer) clearTimeout(timer);
      };
    }, [props.src]);

    // It reapplies the state to the selected node on every update: without this
    // the panel would freeze at the instant of the click while the graph keeps
    // moving.
    var currentNode = selected
      ? payload.nodes.filter(function (n) { return n.id === selected; })[0]
      : null;

    // Collapsing belongs to the CLIENT: it is a preference of whoever is
    // looking, not run state, so it goes neither to the server nor to the
    // database. It is the escape valve for a large DAG -- twenty expanded SDK
    // steps are a lot of rows.
    var collapsedState = React.useState({});
    var collapsed = collapsedState[0], setCollapsed = collapsedState[1];
    var toggle = React.useCallback(function (id) {
      setCollapsed(function (m) {
        var fresh = Object.assign({}, m);
        if (fresh[id]) delete fresh[id]; else fresh[id] = true;
        return fresh;
      });
    }, []);

    var withStages = {};
    payload.nodes.forEach(function (n) {
      if (n.parentId) withStages[n.parentId] = true;
    });

    var nodes = [];
    payload.nodes.forEach(function (n) {
      // A collapsed step's child simply does not go in.
      if (n.parentId && collapsed[n.parentId]) return;
      if (!withStages[n.id]) {
        nodes.push(n);
        return;
      }
      var d = Object.assign({}, n.data, {
        hasStages: true,
        collapsed: !!collapsed[n.id],
        toggle: function () { toggle(n.id); },
      });
      // Collapsed, the group loses its declared height and fits its content
      // again: without this a tall empty box would be left over.
      nodes.push(Object.assign({}, n, {
        data: d,
        style: collapsed[n.id] ? undefined : n.style,
      }));
    });

    return h(
      "div",
      { style: { position: "relative", width: "100%", height: "100%" } },
      payload.erro
        ? h(
            "div",
            {
              style: {
                position: "absolute", top: 12, left: 12, zIndex: 6, padding: "6px 10px",
                borderRadius: 999, background: "#b0503c14", border: "1px solid #b0503c33",
                color: "#8f4030", fontSize: 12,
              },
            },
            "could not load the graph: " + payload.erro
          )
        : null,
      h(
        RF.ReactFlow,
        {
          nodes: nodes,
          edges: payload.edges,
          nodeTypes: NODE_TYPES,
          fitView: true,
          fitViewOptions: { padding: 0.2 },
          minZoom: 0.2,
          proOptions: { hideAttribution: false },
          defaultEdgeOptions: { style: { stroke: GOLD, strokeWidth: 1.4 } },
          // Viewing, not editing: dragging a node and reconnecting an edge stay
          // off until the editor phase. Pan and zoom remain free.
          nodesDraggable: false,
          nodesConnectable: false,
          edgesFocusable: false,
          onNodeClick: function (_, n) { setSelected(n.id); },
          onPaneClick: function () { setSelected(null); },
        },
        h(RF.Background, { color: themeVar("--color-state-pending", "#c9bfae"), gap: 22, size: 1.4 }),
        h(RF.Controls, { showInteractive: false })
      ),
      h(Inspector, {
        no: currentNode,
        stages: payload.nodes.filter(function (n) { return n.parentId === selected; }),
        close: function () { setSelected(null); },
      })
    );
  }

  var root = document.getElementById("dag");
  if (root) {
    ReactDOM.createRoot(root).render(h(Graph, { src: root.dataset.src }));
  }
})();
