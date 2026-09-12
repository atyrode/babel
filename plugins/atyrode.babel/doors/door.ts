import type { GuestCtx, ServerActionDef, ServerHandler } from "@manifold/plugin-kit/server";

/*
  ONE DOOR: the declaration the roster publishes and the code that answers it, as one value.
  The kit's definition takes the two apart (`actions` beside `handlers`), and `doors/index.ts`
  is where that split happens — a module that adds a door should not have to remember to add
  it twice.

  STUB (Scaffold, P0): this file belongs to the acts slice; replace it with that version.
*/

export interface Door {
  readonly action: ServerActionDef;
  readonly handler: ServerHandler;
}

export function defineDoor<In, Out>(
  action: ServerActionDef<In, Out>,
  handler: (ctx: GuestCtx, args: In) => Promise<Out>,
): Door {
  return { action: action as ServerActionDef, handler: handler as ServerHandler };
}
