// React Flow's UMD bundle depends on `react/jsx-runtime`, which React 18 does
// NOT
// publica em UMD — so em ESM/CJS. Sem este shim o script do xyflow lanca
// "jsxRuntime is not defined" e a tela fica em branco.
//
// The reimplementation is faithful: `jsx`/`jsxs` differ from `createElement`
// only in taking the children inside props and the key as a third argument.
// Passing the whole config (with `children`) to createElement preserves both —
// it only overrides `props.children` when there are extra arguments, which here
// there never are.
(function () {
  "use strict";
  function criar(tipo, props, key) {
    if (key === undefined) return React.createElement(tipo, props);
    return React.createElement(tipo, Object.assign({}, props, { key: key }));
  }
  window.jsxRuntime = {
    jsx: criar,
    jsxs: criar,
    jsxDEV: criar,
    Fragment: React.Fragment,
  };
})();
