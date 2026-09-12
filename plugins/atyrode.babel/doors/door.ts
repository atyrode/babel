import type { GuestCtx, ServerActionDef, ServerHandler } from "@manifold/plugin-kit/server";

/*
  ONE DOOR. The kit keeps a declaration and its behaviour apart — `ServerPluginDef` takes an
  `actions` array and a `handlers` map keyed by action name — so a module that serves a family of
  doors has to hand back both halves or the plugin's entry has to repeat every name a second time.
  This is that pair, and it is the whole of this file: `doors/index.ts` walks the doors of every
  module once and splits them, so no name is written twice and no handler can be registered under
  a door that was never declared.
*/

export interface Door {
  readonly action: ServerActionDef;
  readonly handler: ServerHandler;
}

/**
 * Binds a declaration to the handler that serves it, inferring the handler's argument from the
 * action's own input schema: the runtime has already parsed the arguments by the time the handler
 * runs, so the handler states the type it was promised rather than re-validating it. A resolved
 * `{ refused }` denies the dispatch by rule, which is how a door says "this is a caller's
 * mistake" without raising.
 */
export function defineDoor<In, Out>(
  action: ServerActionDef<In, Out>,
  handler: (ctx: GuestCtx, args: In) => Promise<Out | { refused: string }>,
): Door {
  return { action, handler };
}
