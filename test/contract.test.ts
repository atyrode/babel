import { describe, expect, test } from "bun:test";
import {
  CEILING_DATABASE_MAX_BYTES,
  jobResourceRequirements,
  PluginManifestSchema,
} from "@manifold/protocol";
import {
  BABEL_PLUGIN_ID,
  EVENTS,
  FEED_PLUGIN_ID,
  INGESTIBLE_TABLES,
  INPUT_FIELD,
  JEV_PLUGIN_ID,
  MATERIAL_EXPORT,
  MATERIAL_OUTPUT,
  MAX_MATERIAL_BYTES,
  OPERATIONS,
  OUTPUT_BINDING,
  OUTPUT_LOCATION,
  PANELS,
  MACHINE_OPERATIONS,
  PRESET_OPERATIONS,
  RESTIC_SERVICE,
  RUNTIME_TOOLS,
  WATCH_PLUGIN_ID,
  asLaunchRequest,
} from "../babel/contract.ts";
import { launchRequest } from "../babel/watch/api.ts";
import { CODE_PLUGIN_ID } from "@atyrode/manifold-code";
import { ADAPTERS } from "../babel/machine/adapters/index.ts";
import { STORE_DATA_VERSION } from "../babel/store/schema.ts";
import { importableTables } from "../babel/store/acts.ts";
import { plugin } from "../babel/server.ts";
import babelManifest from "../babel/manifest.json";
import feedManifest from "../babel/feed/manifest.json";
import watchManifest from "../babel/watch/manifest.json";
import jevManifest from "../babel/jev/manifest.json";
import { JEV_SERVICE } from "../babel/jev/server/credential.ts";

/*
  A manifest is JSON and cannot import `contract.ts`, so every id it repeats is pinned here:
  the halves spell the vocabulary from the contract, and a manifest that drifted would publish a
  plugin whose own code names something else — a panel with no component, an event the engine
  refuses to fan out, a store the engine will not open.
*/

const babel = PluginManifestSchema.parse(babelManifest);
const feed = PluginManifestSchema.parse(feedManifest);
const watch = PluginManifestSchema.parse(watchManifest);
const jev = PluginManifestSchema.parse(jevManifest);

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

  test("a run's output reaches no table that holds a ruling", () => {
    // #340's load-bearing constraint, as a pin rather than a habit. `INGESTIBLE_TABLES` is the
    // closed set `server/conductor.ts`'s ingest map may name — its `TableIngest.table` is typed
    // as `IngestibleTable`, so an entry pointing elsewhere does not compile — and the two
    // ledgers a run may never write are `dispositions` (a verdict on a record) and
    // `next_action_rulings` (an answer to something a run proposed). A Babel that could write
    // its own acceptance is an agent that agrees with itself, and the acceptance rate stops
    // being evidence about anything.
    expect(INGESTIBLE_TABLES).not.toContain("dispositions");
    expect(INGESTIBLE_TABLES).not.toContain("next_action_rulings");
    // And every name in it is a table the migration actually creates, so the boundary cannot be
    // widened by a typo into a table nothing would refuse.
    const tables = Object.keys(importableTables());
    for (const table of INGESTIBLE_TABLES) expect(tables).toContain(table);
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

  test("the judgement part: its id, the required edge, and its two authorities", () => {
    expect(jev.id).toBe(JEV_PLUGIN_ID);
    expect(jev.id.startsWith(`${BABEL_PLUGIN_ID}.`)).toBe(true);
    expect(jev.dependencies?.[BABEL_PLUGIN_ID]?.type).toBe("required");
    // A server half and no surface: the judgement is work, not a page, and the baseline's own
    // panels are where its answers would show.
    expect(jev.entry).toEqual({ server: true });
    expect(jev.contributes.panels).toEqual([]);
    expect(jev.contributes.seats).toBeUndefined();
    expect(jev.contributes.events).toEqual([]);
    /*
      TWO AUTHORITIES, AND NEITHER IS SPARE. A capability acquired "because a child will need
      it" is how an optional part stops being optional, so each arrives with the child that
      spends it.

      `services:invoke` arrived with the credential path, which is the only code in the part's
      bundle that can exercise it. It buys nothing on its own: authority over a service nobody
      installed reaches nothing, which is why no binding is no call rather than a policy
      somebody has to remember.

      `containers:read` is what the declared edge is worth (#404). A cross-plugin call is
      bounded by the CALLER's own ceiling, and every reading door of Babel's demands that one
      — so a part declaring only the service authority could not open a single door of the
      baseline it declares required. `test/part-ceiling.test.ts` dispatches that, against a
      host that grades the ceiling; this line is only the set.

      `containers:write` is deliberately absent, and #360 is why: whether a dependent plugin
      may write at all is an open decision, not a manifest edit. A store, a machine block or a
      purge target would each be a part the operator cannot reason about the removal of.
    */
    expect(jev.capabilities).toEqual(["containers:read", "services:invoke"]);
    expect(jev.database).toBeUndefined();
    expect(jev.machine).toBeUndefined();
    expect(jev.purges).toBeUndefined();
    // The service it invokes is namespaced under the part, the way `atyrode.babel.restic` is
    // namespaced under the baseline. `server/credential.ts` spells the id literally rather than
    // importing the vocabulary — the kit would inline the baseline's whole contract into the
    // part's bundle for one string — so this is where the two are held together.
    expect(JEV_SERVICE.serviceId.startsWith(`${JEV_PLUGIN_ID}.`)).toBe(true);
  });

  test("the family is four plugins and every panel of it is declared once", () => {
    const declared = [babel, feed, watch, jev].flatMap((manifest) =>
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
    const seated = [feed, watch, jev]
      .flatMap((manifest) =>
        (manifest.contributes.seats ?? []).map((seat) => ({
          ...seat,
          id: `${manifest.id}.${seat.panel}`,
        })),
      )
      .sort((left, right) => left.order - right.order);
    expect(seated.map((seat) => seat.id)).toEqual([
      `${FEED_PLUGIN_ID}.${PANELS.home}`,
      `${WATCH_PLUGIN_ID}.${PANELS.watch}`,
    ]);
    expect(seated[0]?.ratio).toBe(2);
  });
});

/*
  TWO SURFACES POST A RUN, AND THEY POST THE SAME DOCUMENT (#330).

  Watch's Start form is one; a never-looked lens on a topic page is the other, and it exists
  because reading a blank coverage cell is not acting on one. A cell is a shorter way to ASK for
  a run and must not be a shorter way to get one — a control reaching the door by a shorter
  route is a hole in the spend discipline, not a convenience. This is the only file that may
  compare them: a part imports the baseline's `contract.ts` and never the other part, which is
  what `eslint.config.js`'s import restriction holds.
*/
describe("what a press posts", () => {
  test("the coverage cell's launch is the document Watch's own form builds", () => {
    const machineId = "m-dev-01";
    const entityId = "ent_0000beef";
    const profile = { containerId: "ctr_workbench", expectedRevision: 11 };

    // What `feed/topic.tsx` sends when the operator presses a lens nobody has pointed here…
    const cell = asLaunchRequest({
      machineId,
      preset: "explore-topic",
      entityId,
      recipes: ["test-economics"],
      profile,
    });
    // …and what Watch's form sends for the same topic, the same lens and the same profile.
    const form = launchRequest(
      {
        preset: "explore-topic",
        machineId,
        containerId: profile.containerId,
        entityId,
        sinceDays: 1,
        minutes: 60,
        recipes: ["test-economics"],
      },
      {
        containerId: profile.containerId,
        revision: profile.expectedRevision,
        model: "anthropic/claude-opus-5",
        thinking: "high",
        lastMachineId: machineId,
        accounts: [],
        resolved: true,
      },
    );

    expect(cell).toEqual(form);
    // And the node the door's `machines:run` is discharged at is the preset's own operation,
    // derived from the request rather than named by whichever surface built it.
    expect(cell.operation).toEqual({
      kind: "operation",
      machineId,
      operationId: PRESET_OPERATIONS["explore-topic"],
    });
    expect(cell.profile).toEqual(profile);
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
    // Model lanes remain Code sessions, not native Babel operations. Recall is a native
    // archive service, so its declaration must match the same machine dispatcher contract.
    expect(declared).toEqual(Object.values(MACHINE_OPERATIONS));
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
        ...(op.providesService
          ? []
          : [{ literal: "--out" }, { literal: `/outputs/${OUTPUT_BINDING}` }]),
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
      expect(op.outputs).toEqual(
        op.providesService ? [] : material ? [OUTPUT_BINDING, MATERIAL_OUTPUT] : [OUTPUT_BINDING],
      );
      expect(op.executable).toEqual({ runtimeTool: "bun" });
      expect(op.stdin).toBe(false);
      // The lease is cut from a location the operation may write, or the hub refuses the launch.
      if (!op.providesService) {
        expect(op.locations).toContainEqual({ locationId: OUTPUT_LOCATION, access: "write" });
      }
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

    `prepare` seals the material as a second output and DECLARES it exportable. The export is
    what lets ANOTHER plugin's job bind it: a same-plugin binding needs none, and Code's job
    is `atyrode.omp`'s, so admission refuses `input_not_exported:material` without it (ADR
    0044). Outputs and exports are asserted together because an output nobody may bind is a
    lease this plugin writes and nothing reads.
  */
  test("the baseline requires Code, and prepare exports the material it seals", () => {
    expect(babel.dependencies).toEqual({
      [CODE_PLUGIN_ID]: {
        type: "required",
        reason: expect.stringContaining("runSession") as unknown as string,
      },
    });
    /*
      THE HUB'S OWN BOUND IS UNDER THE MACHINE'S, WITH ROOM. `doors/launch.ts` refuses a
      selection whose catalogued bytes exceed `MAX_MATERIAL_BYTES`, BEFORE a job is posted;
      the machine refuses at the seal, AFTER it has read every log in the selection. The
      first must be the one that fires, or the operator learns his window was too wide from a
      twenty-minute job that failed at the end.

      And `<=` would not be enough: `outputBytes` is the AGGREGATE the owner seals against —
      stdout, stderr and BOTH of this operation's leases come out of one running budget, and
      each lease is a ustar archive carrying 512 bytes of header and padding per member
      (`agent/src/job-owner.ts`). A selection admitted at exactly the job's bound packs to it
      and is refused `output_collection_refused` after the full read. A tenth of the job is
      the margin this pins; the constant currently leaves an eighth.
    */
    const outputBytes = machine.operations[OPERATIONS.prepare]!.limits?.outputBytes ?? 0;
    expect(MAX_MATERIAL_BYTES).toBeLessThanOrEqual(Math.floor(outputBytes * 0.9));
    const prepare = machine.operations[OPERATIONS.prepare]!;
    expect(prepare.outputs).toEqual([OUTPUT_BINDING, MATERIAL_OUTPUT]);
    expect(prepare.exports).toEqual([MATERIAL_EXPORT]);
    // Every exported name is one this operation actually writes: an export of a lease that is
    // never cut is a binding that resolves to nothing at the consumer's admission.
    for (const exported of prepare.exports ?? []) expect(prepare.outputs).toContain(exported);
    // The material's lease is cut from the same managed location the ordinary one is: a second
    // anchor would be a second thing an operator has to arrange per machine.
    expect(prepare.locations).toContainEqual({ locationId: OUTPUT_LOCATION, access: "write" });
    // …and no other operation exports anything: `scan` and `archive` write for this hub alone.
    for (const operation of [OPERATIONS.scan, OPERATIONS.archive]) {
      expect(machine.operations[operation]!.exports ?? []).toEqual([]);
    }
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
        const mounted = machine.operations[operation]!.locations.filter(
          (location) => location.access === "read",
        ).map((location) => machine.locations[location.locationId]?.guestPath);
        expect(mounted.toSorted()).toEqual(
          ADAPTERS.flatMap((adapter) => adapter.defaultRoots()).toSorted(),
        );
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
    // archive and verify reach the repository, and their storage service proxy is loopback
    // HTTP the engine refuses to open without it (`service_proxy_requires_host_network`).
    expect(machine.operations[OPERATIONS.archive]?.network).toBe("host");
    expect(machine.operations[MACHINE_OPERATIONS.verify]?.network).toBe("host");
  });

  test("the repository is handed to an operation by a service binding, never by an environment value", () => {
    // An operation's `environment` is fixed reviewed values in committed code, so neither the
    // repository password nor this deployment's locator can live there. The binding is the
    // whole delivery: the engine writes {url, bearer} of the job's own service proxy into one
    // input file, and the operation asks that service for the storage document that carries
    // the locator and its secrets together (machine/restic.ts).
    //
    // Archive, verification and Recall share one storage-binding contract; no operation
    // may invent an alternative path to its repository coordinates or credentials.
    const touching: readonly string[] = [
      OPERATIONS.archive,
      MACHINE_OPERATIONS.verify,
      MACHINE_OPERATIONS.recall,
    ];
    for (const operation of touching) {
      const op = machine.operations[operation]!;
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
    }
    // Environment values name writable cache directories or an exact materialized input
    // file. A bearer FILE reference is allowed; a bearer value or unmounted path is not.
    for (const operation of declared) {
      const op = machine.operations[operation]!;
      const writable = op.locations
        .filter((location) => location.access === "write")
        .map((location) => machine.locations[location.locationId]?.guestPath ?? "\0");
      const inputs = new Set(Object.keys(op.inputFiles ?? {}).map((key) => `/inputs/${key}`));
      for (const [name, value] of Object.entries(op.environment ?? {})) {
        expect({
          name,
          inside: inputs.has(value) || writable.some((guest) => value.startsWith(`${guest}/`)),
        }).toEqual({
          name,
          inside: true,
        });
      }
    }
    // No other operation acquires storage or model authority through a service binding.
    for (const other of declared.filter((operation) => !touching.includes(operation))) {
      expect(machine.operations[other]!.services).toBeUndefined();
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
    // `verify` is an hour because `--read-data` reads every stored byte of the repository,
    // which is the whole point of asking for it; the door posts under this ceiling and the
    // structural check that most verifications ask for finishes in a fraction of it.
    expect(minutes(MACHINE_OPERATIONS.verify)).toBe(60);
    expect(machine.operations[OPERATIONS.prepare]!.limits.outputBytes).toBe(512 * 1024 * 1024);
  });

  test("every operation asks the machine only for resources the fleet advertises", () => {
    // WHAT #303 WAS: a machine answers for tool resources BY NAME, dev-01 advertises two
    // (`development` and `system`), and this half asked for `bun`, `git` and `restic` — so
    // `engine.jobs.reviewDeployment` answered `resource_evidence_unknown` and Babel had no
    // native installation at all. `jobResourceRequirements` is the engine's own answer to what
    // an operation needs from the host, and it drops every alias the installation's immutable
    // declaration pins itself, which is why pinning bun is what makes these satisfiable.
    //
    // `git` is not gone: it is inside the `development` closure the owner already binds, and
    // machine/repository.ts resolves it at RUNTIME_TOOL_BIN first and on PATH second.
    for (const platform of Object.keys(machine.artifacts) as (keyof typeof machine.artifacts)[]) {
      for (const operation of [OPERATIONS.scan, OPERATIONS.prepare]) {
        const required = jobResourceRequirements(machine, operation, platform);
        expect(required.tools).toEqual(["development", "system"]);
        expect(required.services).toEqual([]);
      }
      // restic is the one alias still asked for by name, and only the two operations that
      // touch the repository ask: upstream's whole Linux distribution is bare bzip2 while
      // `MachineArtifactSchema` takes `raw`, `zip` or `tar.gz`, so there is nothing honest to
      // pin. Per-operation is the point — a machine that binds no restic disables those two
      // and leaves scan and prepare installable.
      for (const operation of [OPERATIONS.archive, MACHINE_OPERATIONS.verify]) {
        expect(jobResourceRequirements(machine, operation, platform).tools).toEqual([
          "restic",
          "system",
        ]);
      }
    }
    for (const operation of declared) {
      for (const alias of machine.operations[operation]!.runtimeTools) {
        expect(RUNTIME_TOOLS as readonly string[]).toContain(alias);
      }
    }
  });

  test("the interpreter every operation runs is pinned by this bundle, on both platforms", () => {
    // `jobResourceRequirements` decides "managed" from the tool map's KEYS alone, so a pin that
    // covered one platform would make the other ask the host for nothing and then find no bun
    // to exec: a tool named by an operation has to be declared for every platform the artifacts
    // declare. `omp` was pinned here once, for #284's launcher; the revert (#279) took it, and
    // Babel drives no engine now — bun is the interpreter of its OWN half, which is why it is
    // the one pin left.
    expect(Object.keys(machine.tools ?? {})).toEqual(["bun"]);
    expect(Object.keys(machine.tools?.bun ?? {})).toEqual(Object.keys(machine.artifacts));
    for (const pinned of Object.values(machine.tools?.bun ?? {})) {
      expect(pinned.url?.startsWith("https://github.com/oven-sh/bun/releases/download/")).toBe(
        true,
      );
      expect(pinned.bundleFile).toBeUndefined();
      expect(pinned.format).toBe("zip");
      // The archive's digest and the extracted binary's are two different measurements; one
      // value in both fields is a pin somebody wrote by hand rather than measured.
      expect(pinned.entrySha256).not.toBe(pinned.sha256);
      expect(pinned.entry.at(-1)).toBe("bun");
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
