// React Flow's UMD bundle depends on `react/jsx-runtime`, which React 18 does
// NOT
// publish as UMD — only as ESM/CJS. Without this shim the xyflow script throws
// "jsxRuntime is not defined" and the screen stays blank.
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
