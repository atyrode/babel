import { GlobalRegistrator } from "@happy-dom/global-registrator";

/*
  A DOCUMENT FOR THE PANEL TO MOUNT INTO.

  This module is a side effect with no exports on purpose: every test file imports it FIRST, so
  the globals exist before `react-dom/client` is evaluated by `./render.tsx`. ESM evaluates a
  module's dependencies in the order they are imported, which is the whole mechanism — a
  `register()` call inside `render.tsx` would run after its own import of react-dom.
*/
GlobalRegistrator.register();
/** React's own flag, which no lib.dom type declares; the harness asserts nothing about it. */
const scope = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
scope.IS_REACT_ACT_ENVIRONMENT = true;
