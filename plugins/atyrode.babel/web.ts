import { BABEL_PLUGIN_ID } from "./contract.ts";

/*
  THE BASELINE, web half. It paints nothing: what the operator reads is `atyrode.babel.feed`
  (Home, a record, a topic) and `atyrode.babel.watch` (what runs), each a view over this
  plugin's doors, so this module registers an id and no channel. The bundle names both halves
  (`entry: { server: true, web: "web.js" }`) because a baseline surface, if one is ever wanted,
  belongs here rather than in a sub-plugin.
*/

export default { id: BABEL_PLUGIN_ID, panels: {} };
