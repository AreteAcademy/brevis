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
    var gatilho = e.target.closest("[data-dialogo]");
    if (gatilho) {
      var d = document.getElementById(gatilho.dataset.dialogo);
      if (d && typeof d.showModal === "function") d.showModal();
      return;
    }
    // A click on the backdrop closes it. A <dialog> does not tell the backdrop
    // from its content on its own: the click's target IS the dialog itself when
    // the frame is hit.
    if (e.target.tagName === "DIALOG") e.target.close();
  });

  // Opening by link: /runs#erro-<id> arrives with the dialog already open. It
  // serves for sending somebody the exact failure instead of "open the list and
  // look".
  function abrirPeloHash() {
    if (!location.hash) return;
    var d = document.getElementById(location.hash.slice(1));
    if (d && d.tagName === "DIALOG" && !d.open && typeof d.showModal === "function") {
      d.showModal();
    }
  }
  abrirPeloHash();
  window.addEventListener("hashchange", abrirPeloHash);

  // --- Tooltip dos graficos ------------------------------------------------
  //
  // The SVG's <title> does show the value, but only after a second of hovering
  // and with the operating system's appearance. Here the tooltip appears at
  // once, follows the
  // cursor e usa a mesma tipografia do resto da pagina.
  var dica = null;

  function mostrar(texto, x, y) {
    if (!dica) {
      dica = document.createElement("div");
      dica.className = "grafico-dica";
      document.body.appendChild(dica);
    }
    dica.textContent = texto;
    dica.style.display = "block";
    // It sits above and to the right of the cursor, flipping to the other side
    // when
    // esbarra na borda — senao a dica sai da tela nas ultimas colunas.
    var largura = dica.offsetWidth;
    var esquerda = x + 14;
    if (esquerda + largura > window.innerWidth - 8) esquerda = x - largura - 14;
    dica.style.left = esquerda + "px";
    dica.style.top = y - dica.offsetHeight - 12 + "px";
  }

  function esconder() {
    if (dica) dica.style.display = "none";
  }

  document.addEventListener("mousemove", function (e) {
    // `closest` does not exist on every possible event target (the document
    // itself, for instance). Without the guard, a mousemove outside any element
    // throws a TypeError and kills the listener for the rest of the session.
    if (!e.target || typeof e.target.closest !== "function") return;
    var alvo = e.target.closest("[data-dica]");
    if (alvo) mostrar(alvo.dataset.dica, e.clientX, e.clientY);
    else esconder();
  });

  // Scrolling with the tooltip open would leave it floating over another point
  // of the chart.
  window.addEventListener("scroll", esconder, { passive: true });
  document.addEventListener("mouseleave", esconder);
})();
