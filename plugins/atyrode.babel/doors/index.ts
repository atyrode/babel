import type { ServerActionDef, ServerHandler } from "@manifold/plugin-kit/server";
import type { BabelStore } from "../store/store.ts";
import { actDoors } from "./acts.ts";
import type { Door } from "./door.ts";
import { readDoors } from "./read.ts";

/*
  EVERY DOOR OF THE BASELINE, in one list. The kit takes a plugin's actions and its handlers as
  two members of one definition (`ServerPluginDef`), while a door is really one thing — a
  declaration and the code that answers it — so each module hands back `Door`s and this file is
  where the pair is split into the shape the definition wants.

  Reading (`read.ts`) and ruling (`acts.ts`) are the two halves, in that order, because a reader
  of the roster should meet the nine answers before the nine acts.
*/

export interface BabelDoors {
  readonly actions: readonly ServerActionDef[];
  readonly handlers: Readonly<Record<string, ServerHandler>>;
}

export function babelDoors(store: BabelStore): BabelDoors {
  const actions: ServerActionDef[] = [];
  const handlers: Record<string, ServerHandler> = {};
  const doors: readonly Door[] = [...readDoors(store), ...actDoors(store)];
  for (const door of doors) {
    const { name } = door.action;
    // Assembly would refuse the duplicate too, at boot, naming the plugin; this names the door.
    if (Object.hasOwn(handlers, name)) throw new Error(`two doors claim the name "${name}"`);
    actions.push(door.action);
    handlers[name] = door.handler;
  }
  return { actions, handlers };
}
