import type { ComponentType } from "react";
import type { PanelProps } from "@manifold/plugin";
import { FEED_PLUGIN_ID, PANELS } from "../contract.ts";
import { HomePanel } from "./home.tsx";
import { RecordPanel } from "./record.tsx";
import { TopicPanel } from "./topic.tsx";

/*
  THE READING SURFACE, registered.

  `atyrode.babel.feed` is a sub-plugin of the baseline: it holds no storage and reaches
  everything through the baseline's doors (`ask` in api.ts). Its web half is IN-REALM — the
  installer's choice, and the one the plan takes (§6) — so this module default-exports the
  registration object the loader reads, with ordinary React components in it, and the shell's
  own React, `@manifold/plugin` and `@manifold/ui` are the shared externals `pack` rewires.

  Three panels, and the ids are the contract's:

    home    the one list, its sentence, the topics rail and the pulse
    record  the peel, the filing desk and the thread — §8.7's peek pane, tiled beside Home
    topic   a topic's header, the operator's stance, Ask Babel, and the feed narrowed to it

  A panel takes no argument in this revision, so what the reader has opened lives in the
  module the three of them share (api.ts's selection) rather than in a ref nobody can spell.
*/

export default {
  id: FEED_PLUGIN_ID,
  panels: {
    [PANELS.home]: HomePanel,
    [PANELS.record]: RecordPanel,
    [PANELS.topic]: TopicPanel,
  },
} satisfies { id: string; panels: Record<string, ComponentType<PanelProps>> };
