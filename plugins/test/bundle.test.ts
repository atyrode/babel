import { dirname, join } from "node:path";
import { beforeAll, expect, test } from "bun:test";
import { PluginBundleSchema, type PluginBundle } from "@manifold/protocol";
import { BABEL_PLUGIN_ID, FEED_PLUGIN_ID, WATCH_PLUGIN_ID } from "../atyrode.babel/contract.ts";

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
    [BABEL_PLUGIN_ID]: ["server.js", "web.js"],
    [FEED_PLUGIN_ID]: ["styles.css", "web.js"],
    [WATCH_PLUGIN_ID]: ["styles.css", "web.js"],
  };
  for (const id of expected) {
    expect(Object.keys(bundles[id]?.files ?? {}).sort()).toEqual(members[id] ?? []);
  }
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
