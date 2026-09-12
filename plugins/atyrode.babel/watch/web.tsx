import type { PanelProps } from "@manifold/plugin";
import { Stack } from "@manifold/ui";
import { PANELS, WATCH_PLUGIN_ID } from "../contract.ts";

/*
  WHAT IS RUNNING, AND WHAT WILL RUN. React in the shell's own realm, painting under
  `.plugin-atyrode_babel_watch` (styles.css).

  STUB (Scaffold, P0): this file and its sheet belong to the watch slice; replace them with that
  version.
*/

function Waiting({ host }: PanelProps) {
  return (
    <Stack className="plugin-atyrode_babel_watch" gap="0.5rem">
      <p>A run states its model, its profile and its ceiling here before it starts.</p>
      <p className="plugin-atyrode_babel_watch__note">{host.principal.id}</p>
    </Stack>
  );
}

export default { id: WATCH_PLUGIN_ID, panels: { [PANELS.watch]: Waiting } };
