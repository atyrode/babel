import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { beforeAll, expect, test } from "bun:test";
import { PluginBundleSchema, type PluginBundle } from "@manifold/protocol";
import {
  BABEL_PLUGIN_ID,
  FEED_PLUGIN_ID,
  JOB_OUTPUT_FILES,
  OPERATIONS,
  WATCH_PLUGIN_ID,
} from "../atyrode.babel/contract.ts";

/*
  ONE BUNDLE PER MANIFEST, cut by `pack` — the artifact the release ships and
  `engine.plugins.install` takes by hash. What this proves that `check` cannot: every half a
  manifest NAMES exists and builds (a `web.js` whose entry does not compile, a `styles: true`
  with no sheet beside it, and a sheet the manifest never declared are all pack failures), the
  shared floor is rewritten rather than inlined — a bundle carrying its own React would render
  against a second copy of the shell's — and `SHA256SUMS` is over the artifacts' exact bytes,
  which is the pin the install door demands.

  `pack.sh` is spawned rather than `packPlugin` imported: the kit's packer runs nested
  `Bun.build`s, which the test runtime's own loader does not survive, and the command CI runs is
  the thing worth proving anyway.
*/

const plugins = dirname(import.meta.dir);
const dist = join(plugins, "dist");
const expected: readonly string[] = [BABEL_PLUGIN_ID, FEED_PLUGIN_ID, WATCH_PLUGIN_ID];

const bundles: Record<string, PluginBundle> = {};
let sums: readonly string[] = [];

beforeAll(async () => {
  const packed = Bun.spawn(["./pack.sh"], { cwd: plugins, stdout: "pipe", stderr: "pipe" });
  const [code, stderr] = await Promise.all([packed.exited, new Response(packed.stderr).text()]);
  expect(`${String(code)} ${stderr}`).toBe("0 ");
  for (const id of expected) {
    const file = join(dist, `${id}.manifold-plugin.json`);
    bundles[id] = PluginBundleSchema.parse(await Bun.file(file).json());
  }
  sums = (await Bun.file(join(dist, "SHA256SUMS")).text()).trimEnd().split("\n");
}, 300_000);

test("the family packs, one bundle per manifest, and nothing else lands in dist", async () => {
  const written = [...new Bun.Glob("*.manifold-plugin.json").scanSync({ cwd: dist })].sort();
  expect(written).toEqual([...expected].map((id) => `${id}.manifold-plugin.json`).sort());
  for (const id of expected) expect(bundles[id]?.manifest.id).toBe(id);
});

test("each bundle carries exactly the members its manifest names", () => {
  const members: Record<string, string[]> = {
    [BABEL_PLUGIN_ID]: ["machine.js", "server.js", "web.js"],
    [FEED_PLUGIN_ID]: ["styles.css", "web.js"],
    [WATCH_PLUGIN_ID]: ["styles.css", "web.js"],
  };
  for (const id of expected) {
    expect(Object.keys(bundles[id]?.files ?? {}).sort()).toEqual(members[id] ?? []);
  }
});

test("the machine half travels inside the baseline's bundle, under the hash its manifest pins", () => {
  // What `pack.sh` built is what the machine will run: the engine takes the bundled member,
  // hashes it against the artifact declaration and refuses the installation on any difference
  // (manifold packages/server/src/plugin-installs.ts). Both platforms name the same member, so
  // one set of bytes is delivered for either machine.
  const machine = bundles[BABEL_PLUGIN_ID]?.manifest.machine;
  const bytes = Buffer.from(bundles[BABEL_PLUGIN_ID]?.files["machine.js"] ?? "", "base64");
  expect(bytes.byteLength).toBeGreaterThan(0);
  const sha256 = new Bun.CryptoHasher("sha256").update(bytes).digest("hex");
  const artifacts = Object.values(machine?.artifacts ?? {});
  expect(artifacts.length).toBe(2);
  for (const artifact of artifacts) {
    expect(artifact.bundleFile).toBe("machine.js");
    expect(artifact.sha256).toBe(sha256);
    expect(artifact.entrySha256).toBe(sha256);
    expect(bytes.byteLength).toBeLessThanOrEqual(artifact.maxBytes);
  }
});

test("the packed machine half runs the argv its manifest declares", async () => {
  // The member is only the machine half if it behaves as one. This is the guest invocation with
  // the three guest paths swapped for temporary ones — the artifact bound at /job/artifact, the
  // input document materialized at /inputs/input, the sealed lease at /outputs/outputs — so a
  // bundle that carried another build, an argv naming an operation the dispatcher does not
  // know, or a `--input` the half reads differently fails here rather than on a machine.
  const machine = bundles[BABEL_PLUGIN_ID]?.manifest.machine;
  const work = mkdtempSync(join(tmpdir(), "babel-bundle-"));
  const artifact = join(work, "artifact");
  const input = join(work, "input");
  const outputs = join(work, "outputs");
  const roots = join(work, "roots");
  await Bun.write(artifact, Buffer.from(bundles[BABEL_PLUGIN_ID]?.files["machine.js"] ?? "", "base64"));
  await Bun.write(input, JSON.stringify({ machineId: "bundle-test", roots: [roots] }));
  const argv = (machine?.operations[OPERATIONS.scan]?.argv ?? []).map((slot) => {
    const literal = "literal" in slot ? slot.literal : "";
    return literal === "/job/artifact"
      ? artifact
      : literal === "/inputs/input"
        ? input
        : literal === "/outputs/outputs"
          ? outputs
          : literal;
  });
  const run = Bun.spawnSync(["bun", ...argv], { cwd: work, stdout: "pipe", stderr: "pipe" });
  expect(`${String(run.exitCode)} ${run.stderr.toString()}`).toBe("0 ");
  const receipt = (await Bun.file(join(outputs, JOB_OUTPUT_FILES.receipt)).json()) as {
    kind: string;
    machineId: string;
    closure: string;
  };
  expect(receipt).toMatchObject({ kind: OPERATIONS.scan, machineId: "bundle-test", closure: "completed" });
});

test("a web half is built against the shell's own floor, not its own copy", () => {
  for (const id of expected) {
    const builtAgainst = bundles[id]?.builtAgainst ?? {};
    expect(builtAgainst["react"]).toBeDefined();
    expect(builtAgainst["@manifold/ui"]).toBeDefined();
  }
});

test("SHA256SUMS pins the bytes that were written", async () => {
  expect(sums.length).toBe(expected.length);
  for (const line of sums) {
    const [sha = "", name = ""] = line.split(/\s+/);
    const bytes = await Bun.file(join(dist, name)).arrayBuffer();
    expect(new Bun.CryptoHasher("sha256").update(bytes).digest("hex")).toBe(sha);
  }
});
