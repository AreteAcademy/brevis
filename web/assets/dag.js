// Ilha interativa da DAG (secao 20 do plano).
//
// Escrita em JS puro, sem JSX e sem bundler, de proposito: a secao 15 proibe
// Node no build ("Tailwind standalone, sem npm"), e um passo de transpilacao so
// para esta tela traria toolchain inteira de volta. `React.createElement` e
// verboso, mas o custo fica nesta unica pagina.
//
// O React Flow NAO e fonte da verdade: posicao dos nos, arestas e estado vem
// prontos do servidor, calculados pelo mesmo `graph.Niveis` que o executor usa.
// Aqui so se desenha.
(function () {
  "use strict";

  var h = React.createElement;
  var RF = window.ReactFlow;

  // As cores sao LIDAS do CSS, nao repetidas aqui. A instalacao pode ter tema
  // proprio (ver internal/branding), e uma paleta fixa neste arquivo faria a
  // ilha da DAG ser a unica parte da tela a ignorar a customizacao.
  //
  // Resolvidas uma vez, no carregamento: getComputedStyle a cada nó re-renderizado
  // forcaria recalculo de estilo no meio do desenho do grafo.
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
    success: { anel: tema("--color-state-success", "#4c7a56"), rotulo: "sucesso" },
    failed: { anel: tema("--color-state-failed", "#b0503c"), rotulo: "falha" },
    running: { anel: tema("--color-state-running", "#3f6d8f"), rotulo: "executando" },
    retrying: { anel: tema("--color-state-retrying", "#a35f28"), rotulo: "repetindo" },
    queued: { anel: tema("--color-state-queued", "#b3822f"), rotulo: "na fila" },
    canceled: { anel: tema("--color-state-canceled", "#8a8175"), rotulo: "cancelado" },
    pending: { anel: tema("--color-state-pending", "#c9bfae"), rotulo: "aguardando" },
  };

  function cor(status) {
    return CORES[status] || CORES.pending;
  }

  // O estado de uma ETAPA reusa a paleta dos passos: um `done` de etapa tem de
  // ser o mesmo verde de um passo `success`, senao a tela ensina duas
  // gramaticas de cor para a mesma ideia.
  var CORES_ETAPA = { done: "success", running: "running", failed: "failed", aborted: "canceled" };

  function corDaEtapa(estado) {
    return cor(CORES_ETAPA[estado] || "pending");
  }

  // resumo condensa os numeros de uma etapa para caber numa linha. O detalhe
  // inteiro fica no painel; aqui so cabe o que se le de relance.
  function resumo(numeros) {
    if (!numeros) return "";
    var chaves = Object.keys(numeros);
    if (!chaves.length) return "";
    var partes = [];
    for (var i = 0; i < chaves.length && partes.length < 2; i++) {
      var v = numeros[chaves[i]];
      if (v === null || v === undefined || v === "") continue;
      partes.push(typeof v === "number" ? v.toLocaleString("pt-BR") : String(v));
    }
    return partes.join(" · ");
  }

  function duracao(ms) {
    if (!ms) return "";
    if (ms < 1000) return ms + "ms";
    if (ms < 60000) return (ms / 1000).toFixed(1) + "s";
    return Math.floor(ms / 60000) + "m" + Math.round((ms % 60000) / 1000) + "s";
  }

  // NoBravis e o card de um passo. Custom node em vez do default porque o
  // default so mostra um rotulo — e o que o operador precisa saber num incidente
  // e o estado, a duracao e se houve retry.
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
          // `color-mix` em vez de concatenar alfa ao hexadecimal: a cor pode
          // vir de uma variavel CSS resolvida, e "var(--x)1f" nao e cor nenhuma.
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
        // O selo do SDK. Cor de ACENTO, nunca de estado: as cores de estado
        // significam "como foi", e um selo pintado de verde diria uma coisa
        // que ele nao sabe.
        //
        // Ele carrega a VERSAO. Um selo que so dissesse "SDK" seria verdadeiro
        // e inutil; com a versao a tela responde "por que este passo se comporta
        // diferente do vizinho" sem ninguem abrir o Dockerfile.
        d.temEtapas
          ? h(
              "button",
              {
                title: d.recolhido ? "mostrar as etapas" : "recolher as etapas",
                onClick: function (ev) {
                  // Sem isto o clique tambem selecionaria o passo, e recolher
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
                title: "construido com o Brevis SDK " + d.sdk,
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

  // NoEtapa e uma fase DENTRO de um passo do SDK: extract, transform, load.
  //
  // Linha, e nao caixa lado a lado. Tres caixas horizontais num card de 230px
  // viram tres selos ilegiveis; em linha cabe o nome, o estado, a duracao e o
  // numero que a etapa produziu -- que e o que serve as tres da manha.
  function NoEtapa(props) {
    var d = props.data;
    var c = corDaEtapa(d.estado);
    var texto = resumo(d.numeros);
    return h(
      "div",
      {
        style: {
          display: "flex", alignItems: "center", gap: 7,
          width: 210, height: 20, padding: "0 8px",
          borderRadius: 7,
          border: "1px solid " + (d.estado === "pending" ? LINHA : "color-mix(in srgb, " + c.anel + " 28%, transparent)"),
          background: "color-mix(in srgb, " + c.anel + " 7%, transparent)",
          fontFamily: '"Inter", ui-sans-serif, system-ui, sans-serif',
          fontSize: 10,
          // A etapa que corre ganha o mesmo anel que o card de um passo em
          // execucao -- e nao uma animacao propria. Duas gramaticas para "esta
          // acontecendo agora" na mesma tela e uma a mais, e movimento aqui
          // ainda obrigaria a respeitar prefers-reduced-motion num arquivo que
          // nao tem build de CSS.
          boxShadow: d.estado === "running"
            ? "0 0 0 2px color-mix(in srgb, " + c.anel + " 22%, transparent)"
            : "none",
        },
      },
      h("span", {
        style: { width: 5, height: 5, borderRadius: 9999, background: c.anel, flexShrink: 0 },
      }),
      h("span", { style: { color: TINTA, fontWeight: 600, letterSpacing: "0.02em" } }, d.nome),
      h("span", { style: { marginLeft: "auto", color: MUDO, whiteSpace: "nowrap" } },
        [texto, d.ms !== null && d.ms !== undefined ? duracao(d.ms) : ""]
          .filter(Boolean).join("  ")
      )
    );
  }

  var TIPOS = { bravis: NoBravis, etapa: NoEtapa };

  // Inspetor: painel lateral do no selecionado. Aparece so quando ha selecao —
  // ocupar espaco fixo com "nada selecionado" reduz a area do grafo a toa.
  function Inspetor(props) {
    var n = props.no;
    if (!n) return null;
    var d = n.data;
    var c = cor(d.status);
    var linhas = [
      ["passo", n.id],
      ["estado", c.rotulo],
      d.acao ? ["comando", d.acao] : null,
      d.duracao_ms ? ["duracao", duracao(d.duracao_ms)] : null,
      typeof d.tentativa === "number" ? ["tentativa", String(d.tentativa + 1)] : null,
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
      // As etapas, com TODOS os numeros. Na caixa do grafo cabe o relance; o
      // detalhe e aqui, que e onde se vai quando a linha do relance nao bastou.
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
                h("span", { style: { color: ce.anel, fontSize: 11, fontWeight: 600, width: 74 } }, et.data.nome),
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
            // Live update por polling, nao por WebSocket: o dado muda em
            // segundos, nao em milissegundos, e um GET repetido nao precisa de
            // conexao persistente nem de reconexao.
            //
            // O intervalo segue o estado. `failed` nao e terminal no dominio —
            // um retry o move para `retrying` — mas insistir de 2 em 2s numa run
            // que provavelmente esgotou as tentativas e trafego a toa; 10s ainda
            // pega o retry sem custar nada. Terminal de verdade nao consulta mais.
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

    // Reaplica o estado no no selecionado a cada atualizacao: sem isto o painel
    // congelaria no instante do clique enquanto o grafo continua avancando.
    var noAtual = selecionado
      ? dados.nodes.filter(function (n) { return n.id === selecionado; })[0]
      : null;

    // Recolher e do CLIENTE: e preferencia de quem olha, nao estado da
    // execucao, entao nao vai ao servidor nem ao banco. E a valvula de escape
    // para um DAG grande -- vinte passos do SDK expandidos sao muita linha.
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
      // Filho de um passo recolhido simplesmente nao entra.
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
      // Recolhido, o grupo perde a altura declarada e volta a caber no
      // conteudo: sem isto sobraria uma caixa alta e vazia.
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
            "falha ao carregar o grafo: " + dados.erro
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
          // Visualizacao, nao edicao: arrastar no e reconectar aresta ficam
          // desligados ate a fase do editor. Pan e zoom seguem livres.
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
