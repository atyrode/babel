import type { ServerActionDef, ServerHandler } from "@manifold/plugin-kit/server";
import type { BabelStore } from "../store/store.ts";
import { actDoors } from "./acts.ts";
import type { Door } from "./door.ts";
import { drainDoors, type DrainDoorDeps } from "./drain.ts";
import { exportDoors } from "./export.ts";
import { launchDoors, type LaunchDeps } from "./launch.ts";
import { readDoors } from "./read.ts";

/*
  EVERY DOOR OF THE BASELINE, in one list. The kit takes a plugin's actions and its handlers as
  two members of one definition (`ServerPluginDef`), while a door is really one thing — a
  declaration and the code that answers it — so each module hands back `Door`s and this file is
  where the pair is split into the shape the definition wants.

  Reading (`read.ts`), ruling (`acts.ts`), rendering out (`export.ts`), starting (`launch.ts`)
  and draining (`drain.ts`) are the five groups — a reader of the roster should meet the answers
  before the acts, and the doors that spend money last. Rendering out is beside the reads because
  it writes nothing and PUBLISHES nothing: an export is a file the operator takes (§4.6), and the
  door holds no authority to reach a destination itself.

  An act that writes a bound is judged against `concurrentJobs`,
  the ceiling this plugin's manifest declares — `null` when it declares none, in which case
  there is no bound to judge against — and a drain needs that and a fan of launches at
  once. There is no model lane here: a run that reaches a model is a Code session, and what
  starts one is Code's `runSession` door (#279), so `launch.ts` refuses and `drain.ts` refuses
  through it.
*/

export interface BabelDoors {
  readonly actions: readonly ServerActionDef[];
  readonly handlers: Readonly<Record<string, ServerHandler>>;
}

export function babelDoors(
  store: BabelStore,
  deps: LaunchDeps,
  drain: DrainDoorDeps,
  concurrentJobs: number | null,
): BabelDoors {
  const actions: ServerActionDef[] = [];
  const handlers: Record<string, ServerHandler> = {};
  const doors: readonly Door[] = [
    ...readDoors(store),
    ...actDoors(store, concurrentJobs, deps.jobs),
    ...exportDoors(store),
    ...launchDoors(store, deps),
    ...drainDoors(store, drain),
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
