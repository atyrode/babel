#!/usr/bin/env bun
/*
  MEASURES THE ONE TOOL THIS BUNDLE PINS and writes `runtime-tools.json` (#303).

  `machine.tools` is a declaration of exact bytes: the owner fetches the asset, hashes it
  against `sha256`, extracts `entry` and hashes THAT against `entrySha256`, and refuses the
  installation on any difference. Every number in that file therefore has to come from bytes
  somebody actually held — a digest copied off a release page is a digest nobody measured, and
  it fails on the machine, at install time, on a host the operator cannot read.

  So this is the only place those numbers are produced: it downloads the asset, hashes what it
  received, and reads the zip's own central directory for the member count and the expanded
  bytes rather than trusting a written-down figure. `pack.sh` then stamps the result into the
  manifest and measures nothing itself, which keeps packing offline and deterministic.

  It also records what the extracted binary needs from the machine, read out of the ELF headers:
  the interpreter and the DT_NEEDED list are the whole argument for declaring the owner's
  reviewed `system` closure, since a Manifold job sandbox carries no libc and a dynamically
  linked bun cannot exec in one (manifold docs/SELF-HOST.md).

  Usage: bun scripts/measure-runtime-tools.ts   (rerun whenever the pinned bun version moves)
*/
import { inflateRawSync } from "node:zlib";
import { join } from "node:path";
import { MachineArtifactSchema, type MachineArtifact } from "@manifold/protocol";

/** The asset per platform, and the ELF `e_machine` it must turn out to be: an arm64 pin that
 *  quietly names an x86-64 asset is a deployment that installs and then cannot exec. */
const ASSETS = {
  "linux-x64": { asset: "bun-linux-x64-baseline", elfMachine: 0x3e },
  "linux-arm64": { asset: "bun-linux-aarch64", elfMachine: 0xb7 },
} as const;
type Platform = keyof typeof ASSETS;

/*
  The BASELINE x64 asset, not the default one: the default requires AVX2 and the fleet is not
  one CPU generation. aarch64 publishes no such split.
*/
const version = Bun.version;
const release = `https://github.com/oven-sh/bun/releases/tag/bun-v${version}`;
const hash = (bytes: Uint8Array): string =>
  new Bun.CryptoHasher("sha256").update(bytes).digest("hex");

interface ZipMember {
  readonly name: string;
  readonly method: number;
  readonly compressedSize: number;
  readonly uncompressedSize: number;
  readonly localOffset: number;
}

/** The central directory is the archive's own account of itself; the local headers are where
 *  the bytes are. Anything zip64 would need 64-bit fields this refuses rather than misreads. */
function members(zip: Buffer): readonly ZipMember[] {
  const end = zip.lastIndexOf(Buffer.of(0x50, 0x4b, 0x05, 0x06));
  if (end < 0 || end + 22 > zip.byteLength) throw new Error("no end-of-central-directory record");
  const count = zip.readUInt16LE(end + 10);
  const size = zip.readUInt32LE(end + 12);
  const offset = zip.readUInt32LE(end + 16);
  if (count === 0xffff || size === 0xffffffff || offset === 0xffffffff) {
    throw new Error("zip64 archives are not measured here");
  }
  const found: ZipMember[] = [];
  let cursor = offset;
  for (let index = 0; index < count; index += 1) {
    if (zip.readUInt32LE(cursor) !== 0x02014b50) throw new Error("central directory is malformed");
    const nameLength = zip.readUInt16LE(cursor + 28);
    const member: ZipMember = {
      name: zip.toString("utf8", cursor + 46, cursor + 46 + nameLength),
      method: zip.readUInt16LE(cursor + 10),
      compressedSize: zip.readUInt32LE(cursor + 20),
      uncompressedSize: zip.readUInt32LE(cursor + 24),
      localOffset: zip.readUInt32LE(cursor + 42),
    };
    if (member.compressedSize === 0xffffffff || member.uncompressedSize === 0xffffffff) {
      throw new Error(`zip64 member sizes are not measured here: ${member.name}`);
    }
    found.push(member);
    cursor += 46 + nameLength + zip.readUInt16LE(cursor + 30) + zip.readUInt16LE(cursor + 32);
  }
  if (cursor !== offset + size) throw new Error("central directory size disagrees with its records");
  return found;
}

function extract(zip: Buffer, member: ZipMember): Buffer {
  if (zip.readUInt32LE(member.localOffset) !== 0x04034b50) {
    throw new Error(`local header is malformed: ${member.name}`);
  }
  const data = member.localOffset +
    30 +
    zip.readUInt16LE(member.localOffset + 26) +
    zip.readUInt16LE(member.localOffset + 28);
  const stored = zip.subarray(data, data + member.compressedSize);
  const bytes = member.method === 0 ? Buffer.from(stored) : inflateRawSync(stored);
  if (member.method !== 0 && member.method !== 8) {
    throw new Error(`unsupported compression method ${String(member.method)}: ${member.name}`);
  }
  if (bytes.byteLength !== member.uncompressedSize) {
    throw new Error(`member does not inflate to its declared size: ${member.name}`);
  }
  return bytes;
}

interface NativeRequirements {
  readonly interpreter: string;
  readonly needed: readonly string[];
}

/** Read the loader and the shared libraries out of the program headers: `system` is required
 *  because of these, and the list is what the owner's closure has to actually contain. */
function nativeRequirements(elf: Buffer, expectedMachine: number): NativeRequirements {
  if (elf.readUInt32BE(0) !== 0x7f454c46 || elf[4] !== 2 || elf[5] !== 1) {
    throw new Error("not a little-endian 64-bit ELF binary");
  }
  const machine = elf.readUInt16LE(0x12);
  if (machine !== expectedMachine) {
    throw new Error(`asset is built for ELF machine ${String(machine)}, not ${String(expectedMachine)}`);
  }
  const headers = Number(elf.readBigUInt64LE(0x20));
  const entrySize = elf.readUInt16LE(0x36);
  const count = elf.readUInt16LE(0x38);
  const loads: { vaddr: bigint; offset: bigint; size: bigint }[] = [];
  let interpreter = "";
  let dynamic: { offset: number; size: number } | undefined;
  for (let index = 0; index < count; index += 1) {
    const header = headers + index * entrySize;
    const type = elf.readUInt32LE(header);
    const offset = elf.readBigUInt64LE(header + 8);
    const vaddr = elf.readBigUInt64LE(header + 16);
    const size = elf.readBigUInt64LE(header + 32);
    if (type === 1) loads.push({ vaddr, offset, size });
    if (type === 2) dynamic = { offset: Number(offset), size: Number(size) };
    if (type === 3) {
      interpreter = elf.toString("utf8", Number(offset), Number(offset + size)).replace(/\0.*$/, "");
    }
  }
  if (!dynamic) return { interpreter, needed: [] };
  // DT_NEEDED and DT_STRTAB are virtual addresses; only a PT_LOAD segment maps one to a file
  // offset, so a binary whose string table is not loaded is a binary this cannot read.
  const fileOffset = (vaddr: bigint): number => {
    const load = loads.find((entry) => vaddr >= entry.vaddr && vaddr < entry.vaddr + entry.size);
    if (!load) throw new Error(`dynamic address ${vaddr.toString(16)} is in no loaded segment`);
    return Number(load.offset + (vaddr - load.vaddr));
  };
  const names: bigint[] = [];
  let strtab: bigint | undefined;
  for (let cursor = dynamic.offset; cursor + 16 <= dynamic.offset + dynamic.size; cursor += 16) {
    const tag = elf.readBigUInt64LE(cursor);
    const value = elf.readBigUInt64LE(cursor + 8);
    if (tag === 0n) break;
    if (tag === 1n) names.push(value);
    if (tag === 5n) strtab = value;
  }
  if (strtab === undefined) throw new Error("dynamic section declares no string table");
  const table = fileOffset(strtab);
  const needed = names.map((name) => {
    const start = table + Number(name);
    const end = elf.indexOf(0, start);
    return elf.toString("utf8", start, end);
  });
  return { interpreter, needed: [...new Set(needed)].sort() };
}

const tools: Partial<Record<Platform, MachineArtifact>> = {};
const system: Partial<Record<Platform, NativeRequirements>> = {};
for (const platform of Object.keys(ASSETS) as Platform[]) {
  const { asset, elfMachine } = ASSETS[platform];
  const url = `https://github.com/oven-sh/bun/releases/download/bun-v${version}/${asset}.zip`;
  const response = await fetch(url, { redirect: "follow" });
  if (!response.ok) throw new Error(`${url} answered ${String(response.status)}`);
  const zip = Buffer.from(await response.arrayBuffer());
  const contents = members(zip);
  const entry = [asset, "bun"];
  const member = contents.find((candidate) => candidate.name === entry.join("/"));
  if (!member) throw new Error(`${url} carries no ${entry.join("/")}`);
  const binary = extract(zip, member);
  system[platform] = nativeRequirements(binary, elfMachine);
  tools[platform] = MachineArtifactSchema.parse({
    url,
    sha256: hash(zip),
    format: "zip",
    entry,
    entrySha256: hash(binary),
    maxBytes: zip.byteLength,
    maxExpandedBytes: contents.reduce((total, item) => total + item.uncompressedSize, 0),
    maxMembers: contents.length,
  });
  console.log(`${platform} ${asset}.zip ${String(zip.byteLength)} bytes sha256=${hash(zip)}`);
}

const file = join(import.meta.dir, "..", "runtime-tools.json");
await Bun.write(
  file,
  `${JSON.stringify(
    {
      bunVersion: version,
      measuredOn: new Date().toISOString().slice(0, 10),
      release,
      measurement:
        "scripts/measure-runtime-tools.ts downloaded each asset over HTTPS, hashed the bytes it" +
        " received, inflated the named entry and hashed that, and read the member count and the" +
        " expanded bytes out of the archive's own central directory. No figure here was" +
        " transcribed from a release page.",
      system: {
        requirement:
          "The `system` runtime tool is the machine owner's reviewed, digest-promoted native" +
          " closure. These are the extracted binary's DIRECT requirements, not a transitive" +
          " closure: the owner supplies and reviews the closure at these exact paths, and an" +
          " owner who has not must refuse admission rather than run bun without a loader.",
        platforms: system,
      },
      tools: { bun: tools },
    },
    null,
    2,
  )}\n`,
);
console.log(`wrote ${file}`);
