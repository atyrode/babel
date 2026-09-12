import { describe, expect, test } from "bun:test";
import {
  CEILING_DATABASE_MAX_BYTES,
  MachineHalfSchema,
  PluginManifestSchema,
} from "@manifold/protocol";
import {
  BABEL_PLUGIN_ID,
  EVENTS,
  FEED_PLUGIN_ID,
  INPUT_FIELD,
  OPERATIONS,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  PANELS,
  WATCH_PLUGIN_ID,
} from "../atyrode.babel/contract.ts";
import { ADAPTERS } from "../atyrode.babel/machine/adapters/index.ts";
import { STORE_DATA_VERSION } from "../atyrode.babel/store/schema.ts";
import { plugin } from "../atyrode.babel/server.ts";
import babelManifest from "../atyrode.babel/manifest.json";
import feedManifest from "../atyrode.babel/feed/manifest.json";
import watchManifest from "../atyrode.babel/watch/manifest.json";
import pinnedTools from "../atyrode.babel/tools.json";

/*
  A manifest is JSON and cannot import `contract.ts`, so every id it repeats is pinned here:
  the halves spell the vocabulary from the contract, and a manifest that drifted would publish a
  plugin whose own code names something else — a panel with no component, an event the engine
  refuses to fan out, a store the engine will not open.
*/

const babel = PluginManifestSchema.parse(babelManifest);
const feed = PluginManifestSchema.parse(feedManifest);
const watch = PluginManifestSchema.parse(watchManifest);

describe("the baseline's manifest spells the contract", () => {
  test("its id, the events it originates, and no panel of its own", () => {
    expect(babel.id).toBe(BABEL_PLUGIN_ID);
    expect(babel.contributes.events.map((event) => event.id)).toEqual(Object.values(EVENTS));
    expect(babel.contributes.panels).toEqual([]);
    expect(babel.entry).toEqual({ server: true, web: "web.js" });
  });

  test("the store it asks for is the store the schema declares", () => {
    // Without `database` there is no `ctx.database` at all (ADR 0034 §6), and a dataVersion
    // that disagreed with the schema's would refuse the plugin at assembly rather than migrate.
    expect(babel.dataVersion).toEqual(STORE_DATA_VERSION);
    expect(babel.database?.maxBytes).toBe(1024 * 1024 * 1024);
    expect(babel.database?.maxBytes).toBeLessThanOrEqual(CEILING_DATABASE_MAX_BYTES);
  });

  test("every door it publishes asks for less authority than the manifest's ceiling", () => {
    // Assembly refuses the plugin whole when an action asks for a cap the manifest omits, so
    // this is the ceiling read from the doors that actually exist rather than from the plan.
    const ceiling = new Set<string>(babel.capabilities);
    for (const action of plugin.actions) {
      for (const cap of [...action.caps, ...(action.delegates ?? [])]) {
        expect(ceiling.has(cap)).toBe(true);
      }
    }
  });

  test("the doors are named once: no two claim one name", () => {
    const names = plugin.actions.map((action) => action.name);
    expect(new Set(names).size).toBe(names.length);
    expect(Object.keys(plugin.handlers).sort()).toEqual([...names].sort());
  });
});

describe("the parts are parts of the baseline", () => {
  test("the feed: its id, its three panels, the required edge, and its own skin", () => {
    expect(feed.id).toBe(FEED_PLUGIN_ID);
    expect(feed.id.startsWith(`${BABEL_PLUGIN_ID}.`)).toBe(true);
    expect(feed.contributes.panels.map((panel) => panel.id)).toEqual([
      PANELS.home,
      PANELS.record,
      PANELS.topic,
    ]);
    expect(feed.dependencies?.[BABEL_PLUGIN_ID]?.type).toBe("required");
    expect(feed.entry).toEqual({ web: "web.js", styles: true });
    // A panel calls the baseline's doors as the viewer; it needs no authority of its own.
    expect(feed.capabilities).toEqual([]);
  });

  test("watch: its id, its one panel, the required edge, and its own skin", () => {
    expect(watch.id).toBe(WATCH_PLUGIN_ID);
    expect(watch.id.startsWith(`${BABEL_PLUGIN_ID}.`)).toBe(true);
    expect(watch.contributes.panels.map((panel) => panel.id)).toEqual([PANELS.watch]);
    expect(watch.dependencies?.[BABEL_PLUGIN_ID]?.type).toBe("required");
    expect(watch.entry).toEqual({ web: "web.js", styles: true });
    expect(watch.capabilities).toEqual([]);
  });

  test("the family is three plugins and every panel of it is declared once", () => {
    const declared = [babel, feed, watch].flatMap((manifest) =>
      manifest.contributes.panels.map((panel) => `${manifest.id}.${panel.id}`),
    );
    expect(new Set(declared).size).toBe(declared.length);
    expect(declared).toEqual([
      `${FEED_PLUGIN_ID}.${PANELS.home}`,
      `${FEED_PLUGIN_ID}.${PANELS.record}`,
      `${FEED_PLUGIN_ID}.${PANELS.topic}`,
      `${WATCH_PLUGIN_ID}.${PANELS.watch}`,
    ]);
  });

  test("a workspace nobody arranged shows Home, with Watch beside it", () => {
    // The engine composes a fresh workspace from every enabled plugin's seats, in `order`, as
    // one row weighted by `ratio`: the one list the operator rules on is the page, and what is
    // running sits next to it. A panel with no seat (a record, a topic) opens on demand.
    const seated = [feed, watch]
      .flatMap((manifest) =>
        (manifest.contributes.seats ?? []).map((seat) => ({ ...seat, id: `${manifest.id}.${seat.panel}` })),
      )
      .sort((left, right) => left.order - right.order);
    expect(seated.map((seat) => seat.id)).toEqual([
      `${FEED_PLUGIN_ID}.${PANELS.home}`,
      `${WATCH_PLUGIN_ID}.${PANELS.watch}`,
    ]);
    expect(seated[0]?.ratio).toBe(2);
  });
});

describe("the machine half is declared as the machine half is built", () => {
  /*
    The manifest's `machine` block and `machine/main.ts` are one contract written twice: the
    hub composes a job request from the block, the enrolled machine runs the argv it names, and
    the operator only ever learns that the two disagreed from a job that failed on a machine he
    cannot read. Everything below is a disagreement that would otherwise reach that far.

    `archive` is absent on purpose: restic ships a bare `.bz2`, which the artifact vocabulary
    has no format for, so there is no honest pin for the tool it needs (README.md).
  */
  const machine = babel.machine!;
  const declared = Object.keys(machine.operations);

  test("it declares every operation it can pin a runtime for, and no other", () => {
    expect(declared).toEqual([OPERATIONS.scan, OPERATIONS.prepare, OPERATIONS.explore, OPERATIONS.evaluate]);
    expect(declared).not.toContain(OPERATIONS.archive);
  });

  test("each operation runs the machine half with its own name and one input document", () => {
    // `bun /job/artifact <op> --input <file> --out <dir>`: the raw artifact is bound at
    // /job/artifact, the input document is materialized as a file at /inputs/<key> and NOT
    // substituted into argv (the engine would pass its bytes), and the output directory is the
    // sealed lease at /outputs/<name> rather than the location the lease is cut from.
    for (const operation of declared) {
      const op = machine.operations[operation]!;
      expect(op.argv).toEqual([
        { literal: "/job/artifact" },
        { literal: operation },
        { literal: "--input" },
        { literal: `/inputs/${INPUT_FIELD}` },
        { literal: "--out" },
        { literal: `/outputs/${OUTPUT_BINDING}` },
      ]);
      expect(op.input[INPUT_FIELD]).toEqual({ type: "string", required: true, maxLength: 65536 });
      expect(op.inputFiles).toEqual({ [INPUT_FIELD]: { input: INPUT_FIELD } });
      expect(op.outputs).toEqual([OUTPUT_BINDING]);
      expect(op.executable).toEqual({ runtimeTool: "bun" });
      expect(op.stdin).toBe(false);
      // The lease is cut from a location the operation may write, or the hub refuses the launch.
      expect(op.locations).toContainEqual({ locationId: OUTPUT_LOCATION, access: "write" });
    }
  });

  test("the roots the adapters read are the roots the job mounts", () => {
    // Inside the sandbox HOME is /home/job, so a read location is only the right one if its
    // guest path is what the adapter will build there. A root that moved on one side and not
    // the other is a scan that finds nothing and reports success.
    const home = process.env["HOME"];
    const codexHome = process.env["CODEX_HOME"];
    process.env["HOME"] = "/home/job";
    delete process.env["CODEX_HOME"];
    try {
      const mounted = machine.operations[OPERATIONS.scan]!.locations
        .filter((location) => location.access === "read")
        .map((location) => machine.locations[location.locationId]?.guestPath);
      expect(mounted.toSorted()).toEqual(ADAPTERS.flatMap((adapter) => adapter.defaultRoots()).toSorted());
    } finally {
      if (home === undefined) delete process.env["HOME"];
      else process.env["HOME"] = home;
      if (codexHome !== undefined) process.env["CODEX_HOME"] = codexHome;
    }
  });

  test("only the operations that launch the engine are given the network", () => {
    expect(machine.operations[OPERATIONS.scan]?.network).toBe("none");
    expect(machine.operations[OPERATIONS.prepare]?.network).toBe("none");
    expect(machine.operations[OPERATIONS.explore]?.network).toBe("host");
    expect(machine.operations[OPERATIONS.evaluate]?.network).toBe("host");
  });

  test("the ceiling on a run is the ceiling the operator was promised", () => {
    // The conductor launches with the operation's own limits; the hub refuses anything above
    // them. These four numbers are therefore the whole answer to "how long can this run".
    const minutes = (operation: string): number =>
      machine.operations[operation]!.limits.timeoutMs / 60_000;
    expect(minutes(OPERATIONS.scan)).toBe(10);
    expect(minutes(OPERATIONS.prepare)).toBe(5);
    expect(minutes(OPERATIONS.explore)).toBe(60);
    expect(minutes(OPERATIONS.evaluate)).toBe(60);
  });

  test("every tool an operation runs is pinned, and pinned to what pin-tools.ts downloaded", () => {
    // An alias no `tools` entry declares is an operation that can only run where the operator
    // built the closure himself; `pack.sh` stamps this block from tools.json, so a manifest
    // that disagrees with it was edited by hand and ships hashes nobody obtained.
    // Parsed, not merely compared: tools.json is stamped in verbatim, so it must be a valid
    // `machine.tools` on its own before it is worth asking whether the manifest agrees.
    expect(machine.tools).toEqual(MachineHalfSchema.shape.tools.parse(pinnedTools));
    for (const operation of declared) {
      for (const alias of machine.operations[operation]!.runtimeTools) {
        expect(Object.keys(machine.tools ?? {})).toContain(alias);
      }
    }
    for (const platforms of Object.values(machine.tools ?? {})) {
      for (const artifact of Object.values(platforms)) {
        expect(artifact.url?.startsWith("https://")).toBe(true);
        expect(artifact.maxBytes).toBeGreaterThan(0);
      }
    }
  });

  test("the machine half is carried in the bundle, one raw artifact per platform", () => {
    // A `raw` artifact IS its entry, so the two digests are one digest — the machine refuses
    // the artifact outright when they differ — and both platforms run the same portable JS.
    const platforms = Object.values(machine.artifacts);
    expect(Object.keys(machine.artifacts)).toEqual(["linux-x64", "linux-arm64"]);
    for (const artifact of platforms) {
      expect(artifact.bundleFile).toBe("machine.js");
      expect(artifact.url).toBeUndefined();
      expect(artifact.format).toBe("raw");
      expect(artifact.entry).toEqual(["machine.js"]);
      expect(artifact.sha256).toMatch(/^[a-f0-9]{64}$/);
      expect(artifact.entrySha256).toBe(artifact.sha256);
      expect(artifact.maxExpandedBytes).toBeGreaterThanOrEqual(artifact.maxBytes);
    }
    expect(platforms[0]?.sha256).toBe(platforms[1]?.sha256);
  });
});
