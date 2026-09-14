import { describe, expect, test } from "bun:test";
import { CEILING_DATABASE_MAX_BYTES, PluginManifestSchema } from "@manifold/protocol";
import {
  BABEL_PLUGIN_ID,
  EVENTS,
  FEED_PLUGIN_ID,
  INPUT_FIELD,
  MATERIAL_OUTPUT,
  OPERATIONS,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  PANELS,
  MACHINE_OPERATIONS,
  RESTIC_SERVICE,
  RUNTIME_TOOLS,
  WATCH_PLUGIN_ID,
} from "../atyrode.babel/contract.ts";
import { CODE_PLUGIN_ID } from "@atyrode/manifold-code";
import { ADAPTERS } from "../atyrode.babel/machine/adapters/index.ts";
import { STORE_DATA_VERSION } from "../atyrode.babel/store/schema.ts";
import { MAX_MATERIAL_BYTES } from "../atyrode.babel/doors/launch.ts";
import { plugin } from "../atyrode.babel/server.ts";
import babelManifest from "../atyrode.babel/manifest.json";
import feedManifest from "../atyrode.babel/feed/manifest.json";
import watchManifest from "../atyrode.babel/watch/manifest.json";

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
  */
  const machine = babel.machine!;
  const declared = Object.keys(machine.operations);

  /** The engine's own bound on one job's input record (`doors/launch.ts` MAX_INPUT_BYTES). */
  const MAX_INPUT_BYTES = 65_536;

  /** The declared bytes of one operation's whole input record. */
  function inputBytes(operation: NonNullable<(typeof machine.operations)[string]>): number {
    let bytes = 0;
    for (const field of Object.values(operation.input)) {
      if (field.type === "string") bytes += field.maxLength ?? 0;
    }
    return bytes;
  }

  test("it declares every operation the machine half implements, in the contract's order", () => {
    // Three, not five: `explore` and `evaluate` are NAMED by this plugin and DECLARED by
    // nobody (#279). A Babel run is a Code session — the operator picks a Code profile or
    // parametrizes one in Code's generator, and Code's `runSession` door posts the omp job —
    // so neither is an operation of this bundle's machine half any more.
    expect(declared).toEqual(Object.values(MACHINE_OPERATIONS));
    expect(declared).toEqual([OPERATIONS.scan, OPERATIONS.archive, OPERATIONS.prepare]);
  });

  test("each operation runs the machine half with its own name and one input document", () => {
    // `bun /job/artifact <op> --input <file> --out <dir>`: the raw artifact is bound at
    // /job/artifact, the input document is materialized as a file at /inputs/<key> and NOT
    // substituted into argv (the engine would pass its bytes), and the output directory is the
    // sealed lease at /outputs/<name> rather than the location the lease is cut from.
    //
    // The VERB is the operation's short word, never its declared id: the hub needs a namespaced
    // id to tell two plugins' operations apart, and the binary behind the id belongs to one
    // plugin and takes `scan`.
    //
    // `prepare` alone takes a SECOND lease (#279). The material a Code session reads is a
    // separate sealed output, because a session binds one named output of one job and Babel's
    // ordinary `outputs` lease carries the receipt and the catalog rows the hub ingests — a
    // model handed that directory would be reading Babel's bookkeeping as if it were evidence.
    for (const [word, operation] of Object.entries(OPERATIONS)) {
      if (!declared.includes(operation)) continue;
      const op = machine.operations[operation]!;
      const material = operation === OPERATIONS.prepare;
      expect(op.argv).toEqual([
        { literal: "/job/artifact" },
        { literal: word },
        { literal: "--input" },
        { literal: `/inputs/${INPUT_FIELD}` },
        { literal: "--out" },
        { literal: `/outputs/${OUTPUT_BINDING}` },
        ...(material
          ? [{ literal: "--material" }, { literal: `/outputs/${MATERIAL_OUTPUT}` }]
          : []),
      ]);
      // The document is one required string, and what is FIXED across the operations is not its
      // bound but the record's: the hub admits the whole record.
      expect(op.input[INPUT_FIELD]?.type).toBe("string");
      expect(op.input[INPUT_FIELD]?.required).toBe(true);
      expect(inputBytes(op)).toBeLessThanOrEqual(MAX_INPUT_BYTES);
      expect(op.inputFiles?.[INPUT_FIELD]).toEqual({ input: INPUT_FIELD });
      expect(op.outputs).toEqual(material ? [OUTPUT_BINDING, MATERIAL_OUTPUT] : [OUTPUT_BINDING]);
      expect(op.executable).toEqual({ runtimeTool: "bun" });
      expect(op.stdin).toBe(false);
      // The lease is cut from a location the operation may write, or the hub refuses the launch.
      expect(op.locations).toContainEqual({ locationId: OUTPUT_LOCATION, access: "write" });
    }
  });

  /*
    THE TWO DECLARATIONS A CODE SESSION RESTS ON (#279), and the one that is not there yet.

    The dependency is REQUIRED, and that is the operator's architecture rather than a
    convenience: `atyrode.babel` depends on `atyrode.code`, which depends on `atyrode.omp`,
    and a hub that enabled Babel without Code would offer a Start section whose every press
    the HOST refuses (`undeclared_dependency`/`dependency_unavailable`) — assembly refusing
    the install is the earlier and better answer. It is also what makes `verify` install the
    three families in order, which is what `bun run deps:code` is for.

    `prepare` seals the material as a second output; binding that output into ANOTHER plugin's
    job additionally needs `exports: ["material"]` beside it, and `MachineOperationSchema` has
    no such key at this pin. That is the whole of `MATERIAL_INPUT_PENDING`, and this test pins
    the half that exists so the day the pin moves the other half is a one-line diff here too.
  */
  test("the baseline requires Code, and prepare seals the material as its own output", () => {
    expect(babel.dependencies).toEqual({
      [CODE_PLUGIN_ID]: {
        type: "required",
        reason: expect.stringContaining("runSession") as unknown as string,
      },
    });
    /*
      THE HUB'S OWN BOUND IS AT OR UNDER THE MACHINE'S. `doors/launch.ts` refuses a selection
      whose catalogued bytes exceed `MAX_MATERIAL_BYTES`, BEFORE a job is posted; the machine
      refuses a lease over `outputBytes`, AFTER it has read every log in the selection. The
      first must be the one that fires, or the operator learns his window was too wide from a
      twenty-minute job that failed at the seal.
    */
    expect(MAX_MATERIAL_BYTES).toBeLessThanOrEqual(
      machine.operations[OPERATIONS.prepare]!.limits?.outputBytes ?? 0,
    );
    const prepare = machine.operations[OPERATIONS.prepare]!;
    expect(prepare.outputs).toEqual([OUTPUT_BINDING, MATERIAL_OUTPUT]);
    // The material's lease is cut from the same managed location the ordinary one is: a second
    // anchor would be a second thing an operator has to arrange per machine.
    expect(prepare.locations).toContainEqual({ locationId: OUTPUT_LOCATION, access: "write" });
    expect(Object.hasOwn(prepare, "exports")).toBe(false);
  });

  /*
    ONE CODE REVISION, NAMED IN TWO PLACES. `package.json`'s `@atyrode/manifold-code` is where
    the TYPES come from — the schemas `server/engine/session.ts` parses every call and reply
    with — and `CODE_REV` is what `deps:code` fetches and builds the bundles `verify` composes
    Babel on top of. Verifying against one revision while compiling against another proves
    nothing about either, so the two are held together here rather than by remembering.
  */
  test("the Code the types come from is the Code verification composes", async () => {
    const pinned = (await Bun.file(new URL("../CODE_REV", import.meta.url)).text()).trim();
    expect(pinned).toMatch(/^[a-f0-9]{40}$/);
    const dependency = (
      JSON.parse(await Bun.file(new URL("../package.json", import.meta.url)).text()) as {
        dependencies: Record<string, string>;
      }
    ).dependencies["@atyrode/manifold-code"];
    expect(dependency).toBe(`github:atyrode/code#${pinned}`);
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
      for (const operation of [OPERATIONS.scan, OPERATIONS.archive]) {
        const mounted = machine.operations[operation]!.locations
          .filter((location) => location.access === "read")
          .map((location) => machine.locations[location.locationId]?.guestPath);
        expect(mounted.toSorted()).toEqual(ADAPTERS.flatMap((adapter) => adapter.defaultRoots()).toSorted());
      }
    } finally {
      if (home === undefined) delete process.env["HOME"];
      else process.env["HOME"] = home;
      if (codexHome !== undefined) process.env["CODEX_HOME"] = codexHome;
    }
  });

  test("only the operations that reach off the machine are given the network", () => {
    expect(machine.operations[OPERATIONS.scan]?.network).toBe("none");
    expect(machine.operations[OPERATIONS.prepare]?.network).toBe("none");
    // archive reaches the repository, and its storage service proxy is loopback HTTP the
    // engine refuses to open without it (`service_proxy_requires_host_network`).
    expect(machine.operations[OPERATIONS.archive]?.network).toBe("host");
  });

  test("archive is handed its repository by a service binding, never by an environment value", () => {
    // An operation's `environment` is fixed reviewed values in committed code, so neither the
    // repository password nor this deployment's locator can live there. The binding is the
    // whole delivery: the engine writes {url, bearer} of the job's own service proxy into one
    // input file, and the operation asks that service for the storage document that carries
    // the locator and its secrets together (machine/restic.ts).
    const op = machine.operations[OPERATIONS.archive]!;
    expect(op.services).toEqual([
      {
        serviceId: RESTIC_SERVICE.serviceId,
        revision: RESTIC_SERVICE.revision,
        operationIds: [RESTIC_SERVICE.operationId],
      },
    ]);
    const bound = op.inputFiles?.[RESTIC_SERVICE.inputFile];
    expect(bound?.literal).toBe('{"url":"","bearer":""}');
    expect(bound?.jsonValues).toEqual([
      { path: ["url"], serviceId: RESTIC_SERVICE.serviceId, value: "url" },
      { path: ["bearer"], serviceId: RESTIC_SERVICE.serviceId, value: "bearer" },
    ]);
    // The one environment value is not a secret and not a locator: it is where restic keeps
    // its index cache, which must be inside a location this operation may write or every
    // backup re-reads every byte it already archived.
    const cache = op.environment?.["BABEL_RESTIC_CACHE_DIR"] ?? "";
    expect(cache).not.toBe("");
    expect(Object.keys(op.environment ?? {})).toEqual(["BABEL_RESTIC_CACHE_DIR"]);
    const writable = op.locations
      .filter((location) => location.access === "write")
      .map((location) => machine.locations[location.locationId]?.guestPath ?? "\0");
    expect(writable.some((guestPath) => cache.startsWith(`${guestPath}/`))).toBe(true);
    // Archive is the only operation with either, again (#279): the two that bound the
    // inference service and fixed the CA bundle `SSL_CERT_FILE` names went with the launcher,
    // because a run that reaches a model is a job Code posts under Code's own policy.
    for (const other of declared.filter((operation) => operation !== OPERATIONS.archive)) {
      expect(machine.operations[other]!.services).toBeUndefined();
      expect(machine.operations[other]!.environment).toBeUndefined();
    }
  });

  test("the ceiling on a run is the ceiling the operator was promised", () => {
    // The loop launches with the operation's own limits; the hub refuses anything above them.
    // These three numbers are therefore the whole answer to "how long can this run".
    //
    // `prepare` is thirty minutes and half a gigabyte because it SEALS THE MATERIAL now
    // (#279): it reads every selected log and writes the normalized record stream into a
    // second lease, and the bound `doors/launch.ts` refuses a selection against
    // (`MAX_MATERIAL_BYTES`) has to fit under this one or the machine is what discovers the
    // window was too wide.
    const minutes = (operation: string): number =>
      machine.operations[operation]!.limits.timeoutMs / 60_000;
    expect(minutes(OPERATIONS.scan)).toBe(10);
    expect(minutes(OPERATIONS.prepare)).toBe(30);
    expect(minutes(OPERATIONS.archive)).toBe(10);
    expect(machine.operations[OPERATIONS.prepare]!.limits.outputBytes).toBe(512 * 1024 * 1024);
  });

  test("this bundle pins no tool at all: every one is the owner's to provide", () => {
    // A Manifold job sandbox has no libc (docs/SELF-HOST.md: "a dynamically linked executable
    // without its loader cannot run in the empty sandbox"), so `bun`, `git`, `ca-certificates`
    // and the reviewed `system` closure are the owner's, bound WITH their closures; restic
    // cannot be pinned for a second reason — upstream's whole Linux distribution is bare bzip2
    // and the artifact vocabulary takes `raw`, `zip` or `tar.gz` only.
    //
    // `omp` WAS pinned, by url and digest, because #284 had Babel driving that exact build.
    // Babel drives no engine now (#279): a run that reaches a model is a Code session, and the
    // build that answers it is pinned by whoever posts it. A pin here would be a second one.
    expect(machine.tools).toBeUndefined();
    for (const operation of declared) {
      for (const alias of machine.operations[operation]!.runtimeTools) {
        expect(RUNTIME_TOOLS as readonly string[]).toContain(alias);
      }
    }
    expect(machine.operations[OPERATIONS.archive]!.runtimeTools).toEqual(["bun", "restic"]);
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
