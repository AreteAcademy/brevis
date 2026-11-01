/* Seletor de idioma (<details class="lang-dd">): fecha ao clicar fora ou com Esc.
   Sem script ele continua funcionando -- abre e fecha pelo próprio summary. */
(function () {
  'use strict';
  var menu = document.querySelector('.lang-dd');
  if (!menu) return;
  document.addEventListener('click', function (e) {
    if (menu.open && !menu.contains(e.target)) menu.open = false;
  });
  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && menu.open) {
      menu.open = false;
      menu.querySelector('summary').focus();
    }
  });
})();
