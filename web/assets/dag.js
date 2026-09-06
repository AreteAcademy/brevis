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

  var h = React.createElement;
  var RF = window.ReactFlow;

  // The colours are READ from the CSS, not repeated here. An installation can
  // have a theme of its own (see internal/branding), and a fixed palette in this
  // file would make the DAG island the one part of the screen that ignores the
  // customization.
  //
  // Resolved once, at load: a getComputedStyle on every re-rendered node would
  // force a style recalculation in the middle of drawing the graph.
  function tema(nome, reserva) {
    try {
      var v = getComputedStyle(document.documentElement).getPropertyValue(nome);
      return v.trim() || reserva;
    } catch (e) {
      return reserva;
    }
  }

  var TINTA = tema("--color-ink", "#21180f");
  var MUDO = tema("--color-muted", "#6e6254");
  var PAPEL = tema("--color-surface", "#fffdf8");
  var LINHA = tema("--color-line", "#21180f1a");
  var OURO = tema("--color-gold", "#aa8450");

  var CORES = {
    success: { anel: tema("--color-state-success", "#4c7a56"), rotulo: "success" },
    failed: { anel: tema("--color-state-failed", "#b0503c"), rotulo: "failed" },
    running: { anel: tema("--color-state-running", "#3f6d8f"), rotulo: "running" },
    retrying: { anel: tema("--color-state-retrying", "#a35f28"), rotulo: "retrying" },
    queued: { anel: tema("--color-state-queued", "#b3822f"), rotulo: "queued" },
    canceled: { anel: tema("--color-state-canceled", "#8a8175"), rotulo: "canceled" },
    pending: { anel: tema("--color-state-pending", "#c9bfae"), rotulo: "pending" },
  };

  function cor(status) {
    return CORES[status] || CORES.pending;
  }

  // A STAGE's state reuses the steps' palette: a stage's `done` has to be the
  // same green as a step's `success`, or the screen teaches two colour grammars
  // for the same idea.
  var CORES_ETAPA = { done: "success", running: "running", failed: "failed", aborted: "canceled" };

  function corDaEtapa(estado) {
    return cor(CORES_ETAPA[estado] || "pending");
  }

  // resumo condenses a stage's numbers to fit on one line. The whole detail
  // stays in the panel; only what is read at a glance fits here.
  function resumo(numeros) {
    if (!numeros) return "";
    var chaves = Object.keys(numeros);
    if (!chaves.length) return "";
    var partes = [];
    for (var i = 0; i < chaves.length && partes.length < 2; i++) {
      var v = numeros[chaves[i]];
      if (v === null || v === undefined || v === "") continue;
      partes.push(typeof v === "number" ? v.toLocaleString("en-US") : String(v));
    }
    return partes.join(" · ");
  }

  function duracao(ms) {
    if (!ms) return "";
    if (ms < 1000) return ms + "ms";
    if (ms < 60000) return (ms / 1000).toFixed(1) + "s";
    return Math.floor(ms / 60000) + "m" + Math.round((ms % 60000) / 1000) + "s";
  }

  // NoBravis is a step's card. A custom node rather than the default because the
  // default only shows a label — and what the operator needs to know during an
  // incident is the state, the duration and whether there was a retry.
  function NoBravis(props) {
    var d = props.data;
    var c = cor(d.status);
    return h(
      "div",
      {
        style: {
          minWidth: 190,
          borderRadius: 14,
          border: "1px solid " + (d.status === "pending" ? LINHA : "color-mix(in srgb, " + c.anel + " 40%, transparent)"),
          background: PAPEL,
          padding: "11px 13px",
          fontFamily: '"Inter", ui-sans-serif, system-ui, sans-serif',
          // `color-mix` rather than concatenating alpha onto the hex: the
          // colour may come from a resolved CSS variable, and "var(--x)1f" is no
          // colour at all.
          boxShadow:
            d.status === "running"
              ? "0 0 0 3px color-mix(in srgb, " + c.anel + " 14%, transparent), 0 8px 24px rgba(33,24,15,.06)"
              : "0 8px 24px rgba(33,24,15,.06)",
        },
      },
      h(RF.Handle, { type: "target", position: RF.Position.Left, style: { background: c.anel } }),
      h(
        "div",
        { style: { display: "flex", alignItems: "center", gap: 8 } },
        h("span", {
          style: {
            width: 8, height: 8, borderRadius: 9999, background: c.anel, flexShrink: 0,
          },
        }),
        h("span", { style: { color: TINTA, fontSize: 13, fontWeight: 600 } }, d.label),
        // The SDK badge. An ACCENT colour, never a state one: the state
        // colours
        // mean "how it went", and a badge painted green would say something it
        // does not know.
        //
        // It carries the VERSION. A badge that only said "SDK" would be true
        // and useless; with the version the screen answers "why does this step
        // behave differently from its neighbour" without anybody opening the
        // Dockerfile.
        d.temEtapas
          ? h(
              "button",
              {
                title: d.recolhido ? "show the stages" : "collapse the stages",
                onClick: function (ev) {
                  // Without this the click would also select the step, and
                  // collapsing
                  // abriria o painel de detalhes junto.
                  ev.stopPropagation();
                  d.alternar();
                },
                style: {
                  marginLeft: 2, padding: 0, width: 14, height: 14, flexShrink: 0,
                  border: "none", background: "none", cursor: "pointer",
                  color: MUDO, fontSize: 9, lineHeight: "14px",
                },
              },
              d.recolhido ? "▸" : "▾"
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
                  border: "1px solid color-mix(in srgb, " + OURO + " 45%, transparent)",
                  background: "color-mix(in srgb, " + OURO + " 10%, transparent)",
                  color: tema("--color-gold-strong", "#8a693d"),
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
                marginTop: 4, color: MUDO, fontSize: 11,
                fontFamily: "ui-monospace, SFMono-Regular, monospace",
                overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
                maxWidth: 200,
              },
            },
            d.acao
          )
        : null,
      h(
        "div",
        { style: { marginTop: 7, display: "flex", gap: 8, fontSize: 11, color: c.anel } },
        h("span", null, c.rotulo),
        d.duracao_ms ? h("span", { style: { color: MUDO } }, duracao(d.duracao_ms)) : null,
        d.tentativa ? h("span", { style: { color: "#a35f28" } }, "retry " + d.tentativa) : null
      ),
      h(RF.Handle, { type: "source", position: RF.Position.Right, style: { background: c.anel } })
    );
  }

  // The numbers worth showing on the line, and their order. The rest stays in
  // the panel: a 20px line fits two magnitudes, not six.
  var DESTAQUE = ["in", "out", "groups", "rows", "pages", "objects"];

  function numerosDaEtapa(numeros) {
    if (!numeros) return [];
    var out = [];
    for (var i = 0; i < DESTAQUE.length && out.length < 2; i++) {
      var k = DESTAQUE[i];
      var v = numeros[k];
      if (v === null || v === undefined || v === "") continue;
      out.push({ chave: k, valor: typeof v === "number" ? v.toLocaleString("en-US") : String(v) });
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
  function NoEtapa(props) {
    var d = props.data;
    var c = corDaEtapa(d.estado);
    var nums = numerosDaEtapa(d.numeros);
    var detalhe = d.numeros && d.numeros.detail;

    return h(
      "div",
      {
        title: detalhe || "",
        style: {
          display: "flex", alignItems: "center", gap: 7,
          width: 210, height: 24, padding: "0 8px",
          borderRadius: 7,
          border: "1px solid " + (d.estado === "pending" ? LINHA : "color-mix(in srgb, " + c.anel + " 28%, transparent)"),
          background: "color-mix(in srgb, " + c.anel + " 7%, transparent)",
          fontFamily: '"Inter", ui-sans-serif, system-ui, sans-serif',
          fontSize: 10,
          // The running stage gets the same ring a running step's card gets --
          // and not an animation of its own. Two grammars for "this is happening
          // now" on the same screen is one too many.
          boxShadow: d.estado === "running"
            ? "0 0 0 2px color-mix(in srgb, " + c.anel + " 22%, transparent)"
            : "none",
        },
      },
      h("span", {
        style: { width: 5, height: 5, borderRadius: 9999, background: c.anel, flexShrink: 0 },
      }),
      // O TIPO da fase: source, map, aggregate, target.
      h("span", {
        style: { color: TINTA, fontWeight: 600, letterSpacing: "0.02em", flexShrink: 0 },
      }, d.rotulo || d.nome),
      // The identity, when there is one: which source, which target. It is
      // what
      // turns "extract, 743ms" from half a card into a whole one.
      detalhe
        ? h("span", {
            style: {
              color: MUDO, fontSize: 9, flex: 1, minWidth: 0,
              overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap",
              fontFamily: "ui-monospace, SFMono-Regular, monospace",
            },
          }, detalhe)
        : h("span", { style: { flex: 1 } }),
      // The numbers, with their name alongside: "216" on its own says nothing
      // about what.
      h("span",
        { style: { display: "flex", gap: 6, flexShrink: 0, whiteSpace: "nowrap" } },
        nums.map(function (n) {
          return h("span", { key: n.chave, style: { color: MUDO } },
            h("span", { style: { color: TINTA, fontVariantNumeric: "tabular-nums" } }, n.valor),
            " " + n.chave
          );
        }),
        d.ms !== null && d.ms !== undefined
          ? h("span", { style: { color: MUDO } }, duracao(d.ms))
          : null
      )
    );
  }

  var TIPOS = { bravis: NoBravis, etapa: NoEtapa };

  // The inspector: the selected node's side panel. It appears only when there is
  // a selection — taking up fixed space with "nothing selected" shrinks the
  // graph's area for nothing.
  function Inspetor(props) {
    var n = props.no;
    if (!n) return null;
    var d = n.data;
    var c = cor(d.status);
    var linhas = [
      ["step", n.id],
      ["state", c.rotulo],
      d.acao ? ["command", d.acao] : null,
      d.duracao_ms ? ["duration", duracao(d.duracao_ms)] : null,
      typeof d.tentativa === "number" ? ["attempt", String(d.tentativa + 1)] : null,
      typeof d.exit_code === "number" ? ["exit code", String(d.exit_code)] : null,
    ].filter(Boolean);

    return h(
      "aside",
      {
        style: {
          position: "absolute", top: 14, right: 14, width: 310, zIndex: 5,
          background: PAPEL, backdropFilter: "blur(6px)",
          border: "1px solid " + LINHA, borderRadius: 20, padding: 16,
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
              color: tema("--color-gold-strong", "#8a693d"), fontSize: 11, fontWeight: 700,
              letterSpacing: "0.14em", textTransform: "uppercase",
            },
          },
          "Detalhes"
        ),
        h(
          "button",
          {
            onClick: props.fechar,
            style: { background: "none", border: "none", color: MUDO, cursor: "pointer", fontSize: 16, lineHeight: 1 },
          },
          "×"
        )
      ),
      h(
        "dl",
        { style: { marginTop: 10, fontSize: 12 } },
        linhas.map(function (l) {
          return h(
            "div",
            { key: l[0], style: { display: "flex", gap: 8, padding: "3px 0" } },
            h("dt", { style: { color: MUDO, width: 80, flexShrink: 0 } }, l[0]),
            h(
              "dd",
              {
                style: {
                  color: TINTA, margin: 0, wordBreak: "break-all",
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
      props.etapas && props.etapas.length
        ? h(
            "div",
            { style: { marginTop: 12 } },
            h(
              "div",
              {
                style: {
                  color: tema("--color-gold-strong", "#8a693d"), fontSize: 10, fontWeight: 700,
                  letterSpacing: "0.14em", textTransform: "uppercase", marginBottom: 6,
                },
              },
              d.sdk ? "Etapas · SDK " + d.sdk : "Etapas"
            ),
            props.etapas.map(function (et) {
              var ce = corDaEtapa(et.data.estado);
              var nums = et.data.numeros || {};
              return h(
                "div",
                {
                  key: et.data.nome,
                  style: {
                    padding: "5px 0", borderTop: "1px solid " + LINHA,
                    display: "flex", gap: 8, alignItems: "baseline", flexWrap: "wrap",
                  },
                },
                h("span", { style: { color: ce.anel, fontSize: 11, fontWeight: 600, width: 74 } },
                  et.data.rotulo || et.data.nome),
                h("span", { style: { color: MUDO, fontSize: 11 } },
                  et.data.ms !== null && et.data.ms !== undefined ? duracao(et.data.ms) : "—"),
                Object.keys(nums).map(function (k) {
                  return h(
                    "span",
                    {
                      key: k,
                      style: {
                        color: TINTA, fontSize: 10,
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

  function Grafo(props) {
    var estado = React.useState({ nodes: [], edges: [], carregando: true, erro: "" });
    var dados = estado[0], setDados = estado[1];
    var sel = React.useState(null);
    var selecionado = sel[0], setSelecionado = sel[1];

    React.useEffect(function () {
      var vivo = true;
      var timer = null;

      function buscar() {
        fetch(props.src, { headers: { Accept: "application/json" } })
          .then(function (r) {
            if (!r.ok) throw new Error("HTTP " + r.status);
            return r.json();
          })
          .then(function (g) {
            if (!vivo) return;
            setDados({ nodes: g.nodes || [], edges: g.edges || [], carregando: false, erro: "" });
            // Live updates by polling, not by WebSocket: the data changes in
            // seconds, not in milliseconds, and a repeated GET needs neither a
            // persistent connection nor reconnection logic.
            //
            // The interval follows the state. `failed` is not terminal in the
            // domain — a retry moves it to `retrying` — but insisting every 2s
            // on a run that has probably run out of attempts is traffic for
            // nothing; 10s still catches the retry at no cost. A genuinely
            // terminal one stops polling.
            var proximo = g.terminal ? 0 : g.status === "failed" ? 10000 : 2000;
            if (proximo && g.run_id) timer = setTimeout(buscar, proximo);
          })
          .catch(function (e) {
            if (!vivo) return;
            setDados(function (d) {
              return { nodes: d.nodes, edges: d.edges, carregando: false, erro: e.message };
            });
            timer = setTimeout(buscar, 5000);
          });
      }

      buscar();
      return function () {
        vivo = false;
        if (timer) clearTimeout(timer);
      };
    }, [props.src]);

    // It reapplies the state to the selected node on every update: without this
    // the panel would freeze at the instant of the click while the graph keeps
    // moving.
    var noAtual = selecionado
      ? dados.nodes.filter(function (n) { return n.id === selecionado; })[0]
      : null;

    // Collapsing belongs to the CLIENT: it is a preference of whoever is
    // looking, not run state, so it goes neither to the server nor to the
    // database. It is the escape valve for a large DAG -- twenty expanded SDK
    // steps are a lot of rows.
    var rec = React.useState({});
    var recolhidos = rec[0], setRecolhidos = rec[1];
    var alternar = React.useCallback(function (id) {
      setRecolhidos(function (m) {
        var novo = Object.assign({}, m);
        if (novo[id]) delete novo[id]; else novo[id] = true;
        return novo;
      });
    }, []);

    var comEtapas = {};
    dados.nodes.forEach(function (n) {
      if (n.parentId) comEtapas[n.parentId] = true;
    });

    var nos = [];
    dados.nodes.forEach(function (n) {
      // A collapsed step's child simply does not go in.
      if (n.parentId && recolhidos[n.parentId]) return;
      if (!comEtapas[n.id]) {
        nos.push(n);
        return;
      }
      var d = Object.assign({}, n.data, {
        temEtapas: true,
        recolhido: !!recolhidos[n.id],
        alternar: function () { alternar(n.id); },
      });
      // Collapsed, the group loses its declared height and fits its content
      // again: without this a tall empty box would be left over.
      nos.push(Object.assign({}, n, {
        data: d,
        style: recolhidos[n.id] ? undefined : n.style,
      }));
    });

    return h(
      "div",
      { style: { position: "relative", width: "100%", height: "100%" } },
      dados.erro
        ? h(
            "div",
            {
              style: {
                position: "absolute", top: 12, left: 12, zIndex: 6, padding: "6px 10px",
                borderRadius: 999, background: "#b0503c14", border: "1px solid #b0503c33",
                color: "#8f4030", fontSize: 12,
              },
            },
            "could not load the graph: " + dados.erro
          )
        : null,
      h(
        RF.ReactFlow,
        {
          nodes: nos,
          edges: dados.edges,
          nodeTypes: TIPOS,
          fitView: true,
          fitViewOptions: { padding: 0.2 },
          minZoom: 0.2,
          proOptions: { hideAttribution: false },
          defaultEdgeOptions: { style: { stroke: OURO, strokeWidth: 1.4 } },
          // Viewing, not editing: dragging a node and reconnecting an edge stay
          // off until the editor phase. Pan and zoom remain free.
          nodesDraggable: false,
          nodesConnectable: false,
          edgesFocusable: false,
          onNodeClick: function (_, n) { setSelecionado(n.id); },
          onPaneClick: function () { setSelecionado(null); },
        },
        h(RF.Background, { color: tema("--color-state-pending", "#c9bfae"), gap: 22, size: 1.4 }),
        h(RF.Controls, { showInteractive: false })
      ),
      h(Inspetor, {
        no: noAtual,
        etapas: dados.nodes.filter(function (n) { return n.parentId === selecionado; }),
        fechar: function () { setSelecionado(null); },
      })
    );
  }

  var raiz = document.getElementById("dag");
  if (raiz) {
    ReactDOM.createRoot(raiz).render(h(Grafo, { src: raiz.dataset.src }));
  }
})();
