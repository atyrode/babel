import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import {
  PLUGIN_BUNDLE_SERVER_FILE,
  PluginBundleSchema,
  type IsolateChildFrame,
  type IsolateHostFrame,
  type PluginBundle,
} from "@manifold/protocol";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { BASELINE_ID, LIST_RUNS_DOOR, RUN_DOOR, SESSIONS_ID } from "../atyrode.babel/contract.ts";

/*
  The deliverable is the ARTIFACT, so this packs both plugins the way `pack.sh` does — the
  kit's `pack` CLI from the sibling checkout, one process per bundle — and runs the baseline's
  `server.js` exactly as the engine's loader will: `Bun.spawn(["bun", "--smol", file], { ipc,
  serialization: "json" })`, driven with the supervisor's frames. The web members are imported
  once, which proves they load; a Worker's `ready` is a browser fact the kit's own tests own.
 */

const root = new URL("../", import.meta.url).pathname;
const PACK = new URL("../../../manifold/packages/plugin-kit/src/pack.ts", import.meta.url).pathname;
let out = "";
let baseline: PluginBundle;
let sessions: PluginBundle;

async function pack(dir: string, id: string): Promise<PluginBundle> {
  const file = `${out}/${id}.manifold-plugin.json`;
  const proc = Bun.spawn(["bun", PACK, dir, "--out", file], { stdout: "pipe", stderr: "pipe" });
  const [code, stderr] = await Promise.all([proc.exited, new Response(proc.stderr).text()]);
  if (code !== 0) throw new Error(`pack ${dir} exited ${String(code)}: ${stderr}`);
  return PluginBundleSchema.parse(await Bun.file(file).json());
}

beforeAll(async () => {
  out = mkdtempSync(`${tmpdir()}/babel-plugins-`);
  [baseline, sessions] = await Promise.all([
    pack(`${root}atyrode.babel`, BASELINE_ID),
    pack(`${root}atyrode.babel/sessions`, SESSIONS_ID),
  ]);
});

afterAll(() => {
  rmSync(out, { recursive: true, force: true });
});

async function member(bundle: PluginBundle, name: string): Promise<string> {
  const encoded = bundle.files[name];
  if (encoded === undefined) throw new Error(`no member ${name}`);
  const file = `${out}/${bundle.manifest.id}-${name}`;
  await Bun.write(file, Buffer.from(encoded, "base64"));
  return file;
}

describe("the artifacts", () => {
  test("carry the manifests' entries and exactly the members they name", () => {
    expect(baseline.manifest.id).toBe(BASELINE_ID);
    expect(Object.keys(baseline.files).sort()).toEqual([PLUGIN_BUNDLE_SERVER_FILE, "web.js"]);
    expect(sessions.manifest.id).toBe(SESSIONS_ID);
    expect(Object.keys(sessions.files)).toEqual(["web.js"]);
    expect(sessions.manifest.dependencies?.[BASELINE_ID]?.type).toBe("required");
  });

  test("both web members load as modules", async () => {
    // Dynamic on purpose: the module is a file this test just wrote from the packed artifact.
    await import(await member(baseline, "web.js"));
    await import(await member(sessions, "web.js"));
  });
});

interface Child {
  send(frame: IsolateHostFrame): void;
  next(): Promise<IsolateChildFrame>;
  exited(): Promise<number>;
}

function spawn(file: string): Child {
  const queue: IsolateChildFrame[] = [];
  const waiting: ((frame: IsolateChildFrame) => void)[] = [];
  const proc = Bun.spawn(["bun", "--smol", file], {
    serialization: "json",
    stdio: ["ignore", "inherit", "inherit"],
    ipc: (message: IsolateChildFrame) => {
      const waiter = waiting.shift();
      if (waiter === undefined) queue.push(message);
      else waiter(message);
    },
  });
  return {
    send: (frame) => proc.send(frame),
    next: () => {
      const queued = queue.shift();
      if (queued !== undefined) return Promise.resolve(queued);
      const { promise, resolve } = Promise.withResolvers<IsolateChildFrame>();
      waiting.push(resolve);
      return promise;
    },
    exited: () => proc.exited,
  };
}

const ctx = {
  principal: { id: "p1", kind: "human", name: "Ada", color: "#e03131" },
  caps: ["terminals:spawn", "containers:read"],
  isRoot: false,
  containerScope: null,
  now: Date.UTC(2026, 8, 5, 22, 0, 0),
} as const;

describe("the packed server half, spawned as the loader spawns it", () => {
  test("loads, refuses a report outside the map, records a run, shuts down", async () => {
    const child = spawn(await member(baseline, PLUGIN_BUNDLE_SERVER_FILE));
    child.send({ t: "load", pluginId: BASELINE_ID, manifest: baseline.manifest, dir: out });
    const loaded = await child.next();
    expect(loaded.t).toBe("loaded");
    if (loaded.t !== "loaded") return;
    expect(loaded.actions.map((action) => action.name)).toEqual([RUN_DOOR, LIST_RUNS_DOOR]);

    child.send({
      t: "dispatch",
      id: "d1",
      action: "run",
      args: { machineId: "m-dev", report: "archive-verify" },
      ctx: { ...ctx, caps: [...ctx.caps] },
    });
    expect(await child.next()).toMatchObject({
      t: "dispatched",
      id: "d1",
      outcome: { ok: false, rule: "invalid_args" },
    });

    child.send({
      t: "dispatch",
      id: "d2",
      action: "run",
      args: { machineId: "m-dev", report: "storage-status" },
      ctx: { ...ctx, caps: [...ctx.caps] },
    });
    // JSON ipc drops `undefined`, and the reply schema demands `result`: a void call answers null.
    const answers: Record<string, unknown> = {
      "machines.isOnline": true,
      newId: "run-1",
      "storage.set": null,
      "storage.keys": ["runs/2026-09-05T22:00:00.000Z-run-1"],
    };
    for (;;) {
      const frame = await child.next();
      if (frame.t === "dispatched") {
        expect(frame).toMatchObject({
          id: "d2",
          outcome: {
            ok: true,
            result: {
              runId: "run-1",
              argv: ["babel", "storage", "status"],
              recordedAt: "2026-09-05T22:00:00.000Z",
            },
          },
        });
        break;
      }
      if (frame.t !== "call") throw new Error(`unexpected ${frame.t}`);
      child.send({ t: "reply", id: frame.id, ok: true, result: answers[frame.method] ?? null });
    }

    child.send({ t: "shutdown" });
    expect(await child.exited()).toBe(0);
  }, 20_000);
});
