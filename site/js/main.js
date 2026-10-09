/* brevis.sh — landing page.
   Sem dependencia, sem build: as ancoras antigas, o menu e a entrada
   dos blocos. */
(function () {
  'use strict';

  /* ------------------------------------------------ ancoras antigas ---- */

  /* As secoes que sairam da home estao na docs: um link antigo com o hash
     delas segue para a pagina certa, no idioma da pagina. */
  var antigas = {
    'mesma-linha': 'ecosystem/', 'produtos': 'ecosystem/', 'como-executa': 'pod-per-step/',
    'funciona-com': 'integrations/', 'console': 'console/', 'faq': 'faq/',
    'contraste': 'philosophy/', 'principios': 'philosophy/', 'editorial': 'philosophy/',
    'open-source': 'introduction/'
  };
  var destino = antigas[location.hash.slice(1)];
  if (destino && document.body.classList.contains('page-landing')) {
    location.replace((document.documentElement.lang === 'en' ? '/en' : '') + '/docs/' + destino);
    return;
  }

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
