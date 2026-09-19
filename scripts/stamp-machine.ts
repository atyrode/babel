#!/usr/bin/env bun
/*
  STAMPS THE BASELINE'S MANIFEST with the two declarations a pack MEASURES rather than a human
  types: the machine half's digest, and the artifact-managed tools the bundle ships (#303).

  A bundle carries the machine half as a `bundleFile` artifact and an artifact is named by its
  hash, so the digest has to be over the bytes `pack.sh` just built. `machine.tools` is the same
  kind of declaration one step out: the owner fetches the pinned asset, hashes it, extracts
  `entry`, hashes that, and refuses the installation on any difference — which is why those
  figures come from `runtime-tools.json`, written by `scripts/measure-runtime-tools.ts` from
  bytes it actually downloaded, and never from this file or a release page.

  The committed manifest carries the last stamp, so whoever moves the machine half or the pinned
  bun sees the digests move in the diff. That is the point of stamping rather than generating.

  Usage: bun scripts/stamp-machine.ts <plugin directory>
*/
import { join } from "node:path";
import { PluginManifestSchema } from "@manifold/protocol";
import { format, resolveConfig } from "prettier";
import runtime from "../runtime-tools.json";

/** The owner's reviewed native closure. A managed tool is a dynamically linked binary — bun
 *  wants `runtime-tools.json`'s measured interpreter — and a job sandbox carries no libc, so an
 *  operation that runs one without asking for this closure installs and then cannot exec. */
const SYSTEM_CLOSURE = "system";

/** What a stamp rewrites. The committed JSON is mutated in place rather than re-emitted from a
 *  schema's output, so every other key stays where the file has it and a stamp stays a diff
 *  somebody can read. */
interface StampTarget {
  readonly machine: {
    readonly artifacts: Record<string, { sha256: string; entrySha256: string }>;
    tools: unknown;
  };
}

const directory = process.argv[2];
if (process.argv.length !== 3 || directory === undefined) {
  throw new Error("Usage: bun scripts/stamp-machine.ts <plugin directory>");
}
// The half that will run on the machine is the half this checkout's tests ran: `bun test` packs
// the tree and executes the packed member with the LOCAL bun (test/bundle.test.ts), while the
// manifest now declares the machine runs the pinned one. A packer on a third version would ship
// a half nothing ever ran under the runtime it names.
if (Bun.version !== runtime.bunVersion) {
  throw new Error(
    `Packing requires the pinned Bun ${runtime.bunVersion}; this is ${Bun.version}. Rerun` +
      " scripts/measure-runtime-tools.ts to move the pin, and read its provenance first.",
  );
}

const file = join(directory, "manifest.json");
const raw: unknown = await Bun.file(file).json();
// The schema parse is the boundary: it proves the committed file is a manifest with a machine
// half before anything is written back, which is what lets the mutation below be typed.
if (PluginManifestSchema.parse(raw).machine === undefined) {
  throw new Error(`${file} declares no machine half to stamp`);
}
const target = raw as StampTarget;

const machine = await Bun.file(join(directory, "machine.js")).arrayBuffer();
const sha256 = new Bun.CryptoHasher("sha256").update(machine).digest("hex");
for (const artifact of Object.values(target.machine.artifacts)) {
  // A `raw` artifact IS its own entry, so the two digests are one digest.
  artifact.sha256 = sha256;
  artifact.entrySha256 = sha256;
}
target.machine.tools = runtime.tools;

// Parsed again because the pins are an input too: a hand-edited runtime-tools.json must fail
// here rather than reach a bundle whose installation the owner refuses.
const operations = Object.entries(PluginManifestSchema.parse(raw).machine?.operations ?? {});
for (const alias of Object.keys(runtime.tools)) {
  // Pinned bytes no operation names are bytes every owner fetches for nothing.
  if (!operations.some(([, operation]) => operation.runtimeTools.includes(alias))) {
    throw new Error(`${file} pins the '${alias}' tool and declares no operation that runs it`);
  }
}
for (const [id, operation] of operations) {
  const managed = operation.runtimeTools.filter((alias) => alias in runtime.tools);
  if (managed.length > 0 && !operation.runtimeTools.includes(SYSTEM_CLOSURE)) {
    throw new Error(
      `${id} runs the pinned ${managed.join(", ")} and must declare '${SYSTEM_CLOSURE}'`,
    );
  }
}

await Bun.write(
  file,
  await format(JSON.stringify(raw), { ...(await resolveConfig(file)), filepath: file }),
);
console.log(
  `machine.js ${String(machine.byteLength)} bytes sha256=${sha256}; tools pinned:` +
    ` ${Object.keys(runtime.tools).join(", ")} (bun ${runtime.bunVersion})`,
);
