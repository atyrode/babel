import { defineServerPlugin, type ServerPluginDef } from "@manifold/plugin-kit/server";
import { PluginManifestSchema } from "@manifold/protocol";
import manifestJson from "./manifest.json";

/*
  THE SERVER HALF OF atyrode.babel.jev, AND IT PUBLISHES NOTHING.

  Jev is a part: typed judgement over the records the baseline holds, enabled and removed on its
  own. The whole design constraint is that Babel never depends on it — absent, disabled or out of
  credit, every door, panel and conductor path answers exactly as it does today — and this file
  is the cheapest structural proof of that, standing before a single voter exists to complicate
  the question. So the roster it registers is empty: no door, no lifecycle hook, no store, no
  capability, no tool grant and no credential. Each of those arrives with the child that needs
  it, and one acquired here "because it will be needed" is how an optional part stops being one.

  IT REACHES THE BASELINE ONLY THROUGH ITS DOORS (`ctx.actions.call`), never as a library, and
  the host is what enforces that rather than a convention: a call to a plugin this manifest does
  not declare is refused `undeclared_dependency`, and a call to one it declares that is absent or
  disabled is refused `dependency_unavailable`. The edge is one-way — this manifest names
  `atyrode.babel` required, the baseline's names nothing of this part — which is why removing the
  directory removes a plugin and not a dependency.

  The id is spelled once in the family's vocabulary (`JEV_PLUGIN_ID`, `atyrode.babel/contract.ts`)
  and `test/contract.test.ts` pins this manifest to it. Nothing here imports that file: the kit
  inlines every imported module into the bundle, and a part with no code of its own yet has no
  reason to carry the baseline's whole contract for one string it does not use at runtime.
*/

export const plugin: ServerPluginDef = {
  manifest: PluginManifestSchema.parse(manifestJson),
  actions: [],
  handlers: {},
};

export default plugin;
defineServerPlugin(plugin);
