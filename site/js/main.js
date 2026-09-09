/* brevis.sh — landing page.
   Sem dependencia, sem build. O FAQ nao aparece aqui de proposito: <details>
   e um accordion nativo, acessivel e operavel por teclado sem uma linha de
   script. O que sobra e o menu e a entrada dos blocos. */
(function () {
  'use strict';

  /* -------------------------------------------------------- menu mobile ---- */

  var alternar = document.getElementById('nav-toggle');
  var nav = document.getElementById('nav');

  if (alternar && nav) {
    alternar.addEventListener('click', function () {
      var aberto = nav.classList.toggle('is-open');
      alternar.setAttribute('aria-expanded', String(aberto));
      alternar.setAttribute('aria-label', aberto ? 'Fechar menu' : 'Abrir menu');
    });

    /* Uma ancora dentro do menu aberto: navega e fecha. */
    nav.addEventListener('click', function (e) {
      if (e.target.closest('a')) {
        nav.classList.remove('is-open');
        alternar.setAttribute('aria-expanded', 'false');
        alternar.setAttribute('aria-label', 'Abrir menu');
      }
    });

    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && nav.classList.contains('is-open')) {
        nav.classList.remove('is-open');
        alternar.setAttribute('aria-expanded', 'false');
        alternar.setAttribute('aria-label', 'Abrir menu');
        alternar.focus();
      }
    });
  }

  /* --------------------------------------------------------------- abas ---- */

  /* Um grupo por [role=tablist]; os painéis vêm de aria-controls, então a
     ordem no HTML é a única fonte da relação aba/painel. Sem o script, o
     primeiro painel fica visível e os outros ficam `hidden` — degrada para
     uma tela em vez de quatro empilhadas. */
  function ligarAbas(lista) {
    var abas = Array.prototype.slice.call(lista.querySelectorAll('[role="tab"]'));
    if (abas.length < 2) return;

    function selecionar(i, foco) {
      abas.forEach(function (aba, j) {
        var ativa = j === i;
        aba.setAttribute('aria-selected', String(ativa));
        aba.setAttribute('tabindex', ativa ? '0' : '-1');
        var painel = document.getElementById(aba.getAttribute('aria-controls'));
        if (painel) painel.hidden = !ativa;
      });
      if (foco) abas[i].focus();
    }

    abas.forEach(function (aba, i) {
      aba.setAttribute('tabindex', aba.getAttribute('aria-selected') === 'true' ? '0' : '-1');
      aba.addEventListener('click', function () { selecionar(i, false); });
      aba.addEventListener('keydown', function (e) {
        var alvo = -1;
        if (e.key === 'ArrowRight' || e.key === 'ArrowDown') alvo = (i + 1) % abas.length;
        else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') alvo = (i - 1 + abas.length) % abas.length;
        else if (e.key === 'Home') alvo = 0;
        else if (e.key === 'End') alvo = abas.length - 1;
        if (alvo < 0) return;
        e.preventDefault();
        selecionar(alvo, true);
      });
    });
  }

  Array.prototype.forEach.call(document.querySelectorAll('[role="tablist"]'), ligarAbas);

  /* ------------------------------------------------------------ entrada ---- */

  /* Entra uma vez e para de observar. O pulso do .flow-rule usa a mesma
     classe, entao ele percorre o trilho quando a secao chega — e nao antes,
     fora da tela, onde ninguem veria. */
  var alvos = document.querySelectorAll('[data-reveal], .flow-rule');

  if (!('IntersectionObserver' in window)) {
    Array.prototype.forEach.call(alvos, function (el) {
      el.classList.add('is-visible');
    });
    return;
  }

  var observador = new IntersectionObserver(function (entradas) {
    entradas.forEach(function (entrada) {
      if (!entrada.isIntersecting) return;
      entrada.target.classList.add('is-visible');
      observador.unobserve(entrada.target);
    });
  }, { threshold: 0.1, rootMargin: '0px 0px -60px 0px' });

  Array.prototype.forEach.call(alvos, function (el) {
    observador.observe(el);
  });
})();
