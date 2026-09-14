import type { ServerActionDef, ServerHandler } from "@manifold/plugin-kit/server";
import type { BabelStore } from "../store/store.ts";
import { actDoors } from "./acts.ts";
import type { Door } from "./door.ts";
import { inferenceDoors } from "./inference.ts";
import { launchDoors, type LaunchDeps } from "./launch.ts";
import { readDoors } from "./read.ts";

/*
  EVERY DOOR OF THE BASELINE, in one list. The kit takes a plugin's actions and its handlers as
  two members of one definition (`ServerPluginDef`), while a door is really one thing — a
  declaration and the code that answers it — so each module hands back `Door`s and this file is
  where the pair is split into the shape the definition wants.

  Reading (`read.ts`), ruling (`acts.ts`), starting (`launch.ts`) and the model lane
  (`inference.ts`) are the four halves — a reader of the roster should meet the answers before
  the acts, and the doors that spend money last. A launch reaches the machines through the
  dispatch's own job authority, and an act that writes a bound is judged against
  `concurrentJobs`, the ceiling this plugin's manifest declares for the operations it launches.
  `inference.ts` reads and writes SERVICES through the dispatch's own service authority and owns
  no policy state of its own — it keeps ONE fact in the store: what the hub answered the last
  time an owner installed a policy, which is what the launch preview reads to tell a hub that
  refused Babel's meter kind from a machine nobody has set up (#284).
*/

export interface BabelDoors {
  readonly actions: readonly ServerActionDef[];
  readonly handlers: Readonly<Record<string, ServerHandler>>;
}

export function babelDoors(store: BabelStore, deps: LaunchDeps, concurrentJobs: number): BabelDoors {
  const actions: ServerActionDef[] = [];
  const handlers: Record<string, ServerHandler> = {};
  const doors: readonly Door[] = [
    ...readDoors(store),
    ...actDoors(store, concurrentJobs),
    ...launchDoors(store, deps),
    ...inferenceDoors(store, { now: deps.now }),
  ];
  for (const door of doors) {
    const { name } = door.action;
    // Assembly would refuse the duplicate too, at boot, naming the plugin; this names the door.
    if (Object.hasOwn(handlers, name)) throw new Error(`two doors claim the name "${name}"`);
    actions.push(door.action);
    handlers[name] = door.handler;
  }
  return { actions, handlers };
}
