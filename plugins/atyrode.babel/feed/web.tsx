import type { PanelProps } from "@manifold/plugin";
import { Stack } from "@manifold/ui";
import { FEED_PLUGIN_ID, PANELS } from "../contract.ts";

/*
  WHAT THE OPERATOR READS: Home, a record, a topic — React in the shell's own realm, on
  `@manifold/ui`'s layout primitives, painting under `.plugin-atyrode_babel_feed` (styles.css).

  STUB (Scaffold, P0): this file and its sheet belong to the feed slice; replace them with that
  version. It registers the three panels the manifest declares so the roster has a component
  for each, and says so on the page rather than leaving an unlabelled hole.
*/

function Waiting({ host }: PanelProps) {
  return (
    <Stack className="plugin-atyrode_babel_feed" gap="0.5rem">
      <p>Babel's store is on this hub; its pages land here.</p>
      <p className="plugin-atyrode_babel_feed__note">{host.principal.id}</p>
    </Stack>
  );
}

export default {
  id: FEED_PLUGIN_ID,
  panels: { [PANELS.home]: Waiting, [PANELS.record]: Waiting, [PANELS.topic]: Waiting },
};
