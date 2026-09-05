import { defineWebPlugin } from "@manifold/plugin-kit/web";
import { BASELINE_ID } from "./contract.ts";

/*
  BABEL'S BASELINE, web half. It contributes no panel: the baseline owns the doors and the
  record, and the panels that call them are sub-plugins (`atyrode.babel.sessions`). The half
  exists so the bundle's `entry.web` is served and the worker answers `ready` with no panels,
  which is exactly what the manifest declares.
 */

defineWebPlugin({ id: BASELINE_ID, panels: {} });
