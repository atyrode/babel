import {
  defineServerAction,
  defineServerPlugin,
  type GuestCtx,
  type ServerPluginDef,
} from "@manifold/plugin-kit/server";
import { PluginManifestSchema } from "@manifold/protocol";
import manifestJson from "./manifest.json";
import {
  LIST_RUNS_ACTION,
  ListRunsInput,
  ListRunsResult,
  REPORT_ARGV,
  RUN_ACTION,
  RUN_RECORDED_EVENT,
  RUNS_CAP,
  RUNS_KEY_PREFIX,
  RunInput,
  RunRecord,
  RunResult,
  type ListRunsResult as ListRunsResultT,
  type RunInput as RunInputT,
  type RunResult as RunResultT,
} from "./contract.ts";

/*
  BABEL'S BASELINE, server half. Two doors over the plugin's own storage:

    run       records that one of babel's read-only reports is about to run on a machine —
              refuses an offline machine, appends `runs/<recordedAt>-<runId>`, trims to
              `RUNS_CAP`, emits `run_recorded` — and answers the exact argv the caller must
              launch. The door records; it does not spawn. An isolated server half has no
              exec verb (docs/manifold-transition.md N9), so the terminal is born in the web
              half of `atyrode.babel.sessions` through `host.openTerminal`, graded as the
              viewer, on the machine the viewer chose.
    listRuns  the records, newest first.
 */

const run = defineServerAction({
  name: RUN_ACTION,
  title: "Record a babel report run on a machine",
  caps: ["terminals:spawn"],
  input: RunInput,
  result: RunResult,
});

const listRuns = defineServerAction({
  name: LIST_RUNS_ACTION,
  title: "List recorded babel report runs",
  caps: ["containers:read"],
  input: ListRunsInput,
  result: ListRunsResult,
});

export const handlers = {
  async [RUN_ACTION](
    ctx: GuestCtx,
    args: RunInputT,
  ): Promise<RunResultT | { readonly refused: string }> {
    if (!(await ctx.machines.isOnline(args.machineId))) {
      return { refused: `machine "${args.machineId}" is not online` };
    }
    const runId = await ctx.newId();
    const recordedAt = new Date(ctx.now()).toISOString();
    const record = RunRecord.parse({
      runId,
      machineId: args.machineId,
      report: args.report,
      argv: REPORT_ARGV[args.report],
      recordedAt,
    });
    await ctx.storage.set(`${RUNS_KEY_PREFIX}${recordedAt}-${runId}`, JSON.stringify(record));
    // Keys under `runs/` sort as time because `recordedAt` is ISO-8601 UTC: oldest first.
    const keys = [...(await ctx.storage.keys(RUNS_KEY_PREFIX))].sort();
    for (const stale of keys.slice(0, Math.max(0, keys.length - RUNS_CAP))) {
      await ctx.storage.delete(stale);
    }
    ctx.emit({ kind: "plugin", pluginId: ctx.pluginId }, RUN_RECORDED_EVENT, {
      runId,
      machineId: args.machineId,
      report: args.report,
      recordedAt,
    });
    return { runId, argv: record.argv, recordedAt };
  },

  async [LIST_RUNS_ACTION](ctx: GuestCtx): Promise<ListRunsResultT> {
    const keys = [...(await ctx.storage.keys(RUNS_KEY_PREFIX))].sort().reverse().slice(0, RUNS_CAP);
    const runs: RunRecord[] = [];
    for (const key of keys) {
      const value = await ctx.storage.get(key);
      if (value === null) continue;
      const parsed = RunRecord.safeParse(JSON.parse(value));
      if (parsed.success) runs.push(parsed.data);
    }
    return { runs };
  },
};

export const def: ServerPluginDef = {
  manifest: PluginManifestSchema.parse(manifestJson),
  actions: [run, listRuns],
  handlers,
};

defineServerPlugin(def);
