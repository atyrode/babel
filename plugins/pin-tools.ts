#!/usr/bin/env bun
import { createHash } from "node:crypto";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { gunzipSync, inflateRawSync } from "node:zlib";
import { MachineHalfSchema, type MachineArtifact } from "@manifold/protocol";
import { extractArtifact } from "@manifold/plugin-kit/artifacts";

/*
  THE RUNTIME TOOL PIN (`atyrode.babel/tools.json`), written by downloading the assets.

      bun pin-tools.ts            # rewrites atyrode.babel/tools.json from the table below

  A machine operation runs no interpreter of its own: `bun` runs the machine half and `code` is
  the engine `explore` and `evaluate` launch. The manifest declares both as MANAGED tools —
  `machine.tools.<alias>.<platform>` — so the enrolled machine's owner downloads each release
  asset itself and refuses anything whose bytes are not the pinned ones (manifold
  `packages/agent/src/job-artifacts.ts`). That refusal is only as good as the hashes here, and a
  hash nobody obtained by download is a hash nobody can trust: this script is the one thing that
  writes them, and its output is committed so that packing needs no network.

  What it pins per platform is what `MachineArtifactSchema` demands: the archive's sha256, the
  sha256 of the ONE member the tool is, and the ceilings the owner's extractor enforces
  (compressed bytes, expanded bytes, member count) — measured from the asset rather than
  guessed, so a release that grew past a made-up ceiling fails here instead of on a machine.

  Every pin is then handed to the kit's OWN extractor, the same code the machine runs at
  install. A pin this script emits has therefore already been accepted once by the verifier that
  will see it next, and a second witness is checked where the publisher offers one: the release's
  `checksums.txt` for `code`, the registry's `dist.integrity` (sha512) for `bun`.

  WHY BUN COMES FROM npm AND NOT FROM ITS GITHUB RELEASE. Oven publishes Linux builds as zip
  only, and those zips carry Info-ZIP extended-timestamp (0x5455) and new-Unix (0x7875) extra
  fields, which the kit's extractor refuses outright — `zipMetadata` admits one 0x5855 field and
  nothing else (`packages/plugin-kit/src/artifacts.ts`, "artifact_unsupported_zip"). The same
  build is published to the npm registry as a gzipped tar, which the extractor does take, and
  `package/bin/bun` in it is byte-identical to `bun-linux-x64/bun` in the zip — the entry sha256
  below is the proof. Do not "fix" this back to the release URL without checking that refusal.

  Re-run it on a version bump: change the versions and urls below, run, commit `tools.json`, then
  `./pack.sh` — which stamps `machine.tools` into the manifest from this file.

  `restic` is deliberately absent, and so is the `archive` operation: upstream ships
  `restic_0.19.1_linux_amd64.bz2` and `restic_0.19.1_linux_arm64.bz2`, bare bzip2 streams, and the
  artifact vocabulary has exactly three formats — `raw`, `zip`, `tar.gz`. There is no honest pin
  to write for it (README.md, "The machine half").
*/

const PLATFORMS = ["linux-x64", "linux-arm64"] as const;
type Platform = (typeof PLATFORMS)[number];

interface Asset {
  /** The published archive, downloaded and hashed as it is; HTTPS, as the schema requires. */
  readonly url: string;
  /** The one member of the archive the tool is, as its path components inside it. */
  readonly entry: readonly string[];
  /** A `<sha256>  <asset>` manifest published beside the archive. */
  readonly sums?: string;
  /** An npm registry document whose `dist.integrity` is the sha512 of exactly this tarball. */
  readonly npm?: string;
}

interface Tool {
  readonly version: string;
  readonly format: "zip" | "tar.gz";
  readonly assets: Record<Platform, Asset>;
}

const BUN = "https://registry.npmjs.org/@oven";
const CODE = "https://github.com/atyrode/code/releases/download/v0.19.0";
const CODE_SUMS = `${CODE}/checksums.txt`;

const TOOLS: Record<string, Tool> = {
  bun: {
    version: "1.3.13",
    format: "tar.gz",
    assets: {
      "linux-x64": {
        url: `${BUN}/bun-linux-x64/-/bun-linux-x64-1.3.13.tgz`,
        entry: ["package", "bin", "bun"],
        npm: `${BUN}/bun-linux-x64/1.3.13`,
      },
      "linux-arm64": {
        url: `${BUN}/bun-linux-aarch64/-/bun-linux-aarch64-1.3.13.tgz`,
        entry: ["package", "bin", "bun"],
        npm: `${BUN}/bun-linux-aarch64/1.3.13`,
      },
    },
  },
  code: {
    version: "0.19.0",
    format: "tar.gz",
    assets: {
      "linux-x64": { url: `${CODE}/code-linux-amd64.tar.gz`, entry: ["code"], sums: CODE_SUMS },
      "linux-arm64": { url: `${CODE}/code-linux-arm64.tar.gz`, entry: ["code"], sums: CODE_SUMS },
    },
  },
};

const digest = (bytes: Uint8Array, algorithm = "sha256"): string =>
  createHash(algorithm).update(bytes).digest("hex");

/** What the archive costs to open and which bytes the entry is: measured, never assumed. */
interface Probe {
  readonly members: number;
  readonly expanded: number;
  readonly entry: Buffer;
}

function member(bytes: Buffer): string {
  const nul = bytes.indexOf(0);
  return bytes.subarray(0, nul === -1 ? bytes.length : nul).toString("utf8");
}

/** The central directory is the identity of a zip; the local headers only follow it. */
function probeZip(archive: Buffer, wanted: string): Probe {
  let end = archive.length - 22;
  while (
    end >= 0 &&
    (archive.readUInt32LE(end) !== 0x06054b50 ||
      end + 22 + archive.readUInt16LE(end + 20) !== archive.length)
  )
    end--;
  if (end < 0) throw new Error("not a zip archive");
  const members = archive.readUInt16LE(end + 10);
  let cursor = archive.readUInt32LE(end + 16);
  let expanded = 0;
  let entry: Buffer | null = null;
  for (let index = 0; index < members; index++) {
    const method = archive.readUInt16LE(cursor + 10);
    const compressed = archive.readUInt32LE(cursor + 20);
    const size = archive.readUInt32LE(cursor + 24);
    const nameLength = archive.readUInt16LE(cursor + 28);
    const extra = archive.readUInt16LE(cursor + 30);
    const comment = archive.readUInt16LE(cursor + 32);
    const local = archive.readUInt32LE(cursor + 42);
    expanded += size;
    if (member(archive.subarray(cursor + 46, cursor + 46 + nameLength)) === wanted) {
      const start = local + 30 + archive.readUInt16LE(local + 26) + archive.readUInt16LE(local + 28);
      const body = archive.subarray(start, start + compressed);
      entry = method === 0 ? body : inflateRawSync(body);
    }
    cursor += 46 + nameLength + extra + comment;
  }
  if (entry === null) throw new Error(`no member named ${wanted}`);
  return { members, expanded, entry };
}

/** A ustar walk: the expanded size the owner bounds is the whole tar, not the sum of members. */
function probeTarGz(archive: Buffer, wanted: string): Probe {
  const tar = gunzipSync(archive);
  let offset = 0;
  let members = 0;
  let entry: Buffer | null = null;
  while (offset + 512 <= tar.length) {
    const header = tar.subarray(offset, offset + 512);
    if (header.every((byte) => byte === 0)) break;
    members++;
    const prefix = member(header.subarray(345, 500));
    const name = member(header.subarray(0, 100));
    const size = Number.parseInt(member(header.subarray(124, 136)).trim() || "0", 8);
    if ((prefix === "" ? name : `${prefix}/${name}`) === wanted)
      entry = tar.subarray(offset + 512, offset + 512 + size);
    offset += 512 + Math.ceil(size / 512) * 512;
  }
  if (entry === null) throw new Error(`no member named ${wanted}`);
  return { members, expanded: tar.length, entry };
}

async function get(url: string): Promise<Response> {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`${url}: ${String(response.status)} ${response.statusText}`);
  return response;
}

/**
 * The publisher's own word on these bytes, when there is one. Absent is allowed and reported;
 * present and disagreeing is a refusal — a pin over an asset that is not the published one is
 * exactly what pinning exists to prevent.
 */
async function witness(asset: Asset, file: string, archive: Buffer): Promise<string> {
  if (asset.sums !== undefined) {
    const published = (await (await get(asset.sums)).text())
      .split("\n")
      .map((line) => line.trim().split(/\s+/))
      .find(([, name]) => name === file)?.[0];
    if (published === undefined) throw new Error(`${asset.sums}: no line for ${file}`);
    if (published !== digest(archive))
      throw new Error(`${file}: ${digest(archive)} is not the published ${published}`);
    return "checksums.txt";
  }
  if (asset.npm !== undefined) {
    const document = (await (await get(asset.npm)).json()) as {
      dist?: { tarball?: string; integrity?: string };
    };
    const integrity = document.dist?.integrity ?? "";
    if (document.dist?.tarball !== asset.url)
      throw new Error(`${asset.npm}: publishes ${String(document.dist?.tarball)}, not ${asset.url}`);
    if (!integrity.startsWith("sha512-")) throw new Error(`${asset.npm}: no sha512 integrity`);
    const published = Buffer.from(integrity.slice("sha512-".length), "base64").toString("hex");
    if (published !== digest(archive, "sha512"))
      throw new Error(`${file}: sha512 ${digest(archive, "sha512")} is not the published ${published}`);
    return "dist.integrity";
  }
  return "";
}

async function pin(tool: Tool, asset: Asset): Promise<MachineArtifact> {
  const into = await mkdtemp(join(tmpdir(), "babel-pin-"));
  try {
    const file = asset.url.slice(asset.url.lastIndexOf("/") + 1);
    const path = join(into, file);
    // Through a file, so that a pin is always over bytes that landed on disk whole.
    await Bun.write(path, await (await get(asset.url)).arrayBuffer());
    const archive = Buffer.from(await Bun.file(path).arrayBuffer());
    const attested = await witness(asset, file, archive);
    const wanted = asset.entry.join("/");
    const probe = tool.format === "zip" ? probeZip(archive, wanted) : probeTarGz(archive, wanted);
    const artifact: MachineArtifact = {
      url: asset.url,
      sha256: digest(archive),
      format: tool.format,
      entry: [...asset.entry],
      entrySha256: digest(probe.entry),
      maxBytes: archive.length,
      maxExpandedBytes: probe.expanded,
      maxMembers: probe.members,
    };
    // The verifier the machine runs at acquisition, run here first: a pin this refuses is a pin
    // that would have refused on the machine, where the only report is an unavailable operation.
    const extracted = await extractArtifact(archive, artifact, AbortSignal.timeout(300_000));
    if (digest(extracted.executable) !== artifact.entrySha256)
      throw new Error(`${file}: the extractor read a different ${wanted}`);
    console.log(
      `  ${file}  ${String(archive.length)} bytes  sha256=${artifact.sha256}` +
        (attested === "" ? "" : `  (${attested})`) +
        `\n    ${wanted}  ${String(probe.entry.length)} bytes  sha256=${artifact.entrySha256}` +
        `  members=${String(probe.members)}  expanded=${String(probe.expanded)}`,
    );
    return artifact;
  } finally {
    await rm(into, { recursive: true, force: true });
  }
}

if (import.meta.main) {
  const tools: Record<string, Record<string, MachineArtifact>> = {};
  for (const [alias, tool] of Object.entries(TOOLS)) {
    console.log(`${alias} ${tool.version} (${tool.format})`);
    const platforms: Record<string, MachineArtifact> = {};
    for (const platform of PLATFORMS) platforms[platform] = await pin(tool, tool.assets[platform]);
    tools[alias] = platforms;
  }
  // The file is exactly `machine.tools`, so `pack.sh` stamps it into the manifest unchanged.
  MachineHalfSchema.shape.tools.parse(tools);
  const out = join(import.meta.dir, "atyrode.babel", "tools.json");
  await Bun.write(out, `${JSON.stringify(tools, null, 2)}\n`);
  console.log(`wrote ${out}`);
}
