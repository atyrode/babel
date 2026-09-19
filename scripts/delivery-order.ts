#!/usr/bin/env bun
/*
  THE ORDER A HUB WILL ACCEPT, READ OFF THE BUNDLES THEMSELVES.

  The preview's receiver takes one bundle per call and a hub refuses a part whose baseline is not
  installed yet, so the sequence a release hands over is load-bearing: dependencies first, then
  each baseline before its own parts. `dist`'s alphabetical order does not give that — at equal
  depth `atyrode.babel.jev` sorts before `atyrode.babel`, because `.` precedes `/` — and neither
  does `pack.sh`'s walk. The order was therefore written out by hand in `release.yml`, beside four
  globs, which is how a plugin came to be attached to a release and never delivered to a preview.

  This prints the delivery order so the workflow can stay a loop over something it did not have to
  know. The order is the kit's own `familyOrder`, the function `verify` installs by on every pull
  request and `dev` installs by on every save, rather than a second answer to the same question:
  what CI proves against a real engine is then exactly what a tag delivers.

  AN ABSENT DEPENDENCY DOES NOT BLOCK, and that is the cross-family case rather than an oversight.
  `familyOrder` orders a bundle by the prerequisites present in the set it was given and leaves
  the rest to the hub, so a bundle whose required dependency is not here keeps its own place
  instead of sinking to the end — the dependency is either installed already or is another
  family's, and only the hub knows which. Those ids are named on stderr, because a closure that
  quietly lost a member is worth seeing in a log. A CYCLE and a DUPLICATE id are refusals: an
  order that cannot exist is never guessed at.

      bun scripts/delivery-order.ts deps dist      # directories, bundle files, or both
      atyrode.omp deps/atyrode.omp.manifold-plugin.json 9f2c…

  One line per bundle — its id, the path it was read from, and the sha256 over the bytes that
  were read, which is the pin `engine.plugins.install` demands. The caller matches the paths it
  globbed against the paths printed here; a bundle that was packed and is not in this list is the
  failure this command exists to make visible.
*/

import { readdir, stat } from "node:fs/promises";
import { join } from "node:path";
import { familyOrder, inspectBundle, type BundleFacts } from "@manifold/plugin-kit/install";

/** What `pack.sh` and the dependency closure both name their artifacts. */
const SUFFIX = ".manifold-plugin.json";

/** A bundle's facts, plus the path it was found at — as given, so a caller can match its glob. */
export interface Packed extends BundleFacts {
  readonly file: string;
}

export interface Delivery {
  /** Every bundle, dependencies and parents before their consumers. */
  readonly order: readonly Packed[];
  /** Ids required by a bundle here and supplied by none: the hub must already have them. */
  readonly external: readonly string[];
}

/** Every bundle in the given directories, plus any bundle named outright. */
export async function packedIn(paths: readonly string[]): Promise<Packed[]> {
  const files: string[] = [];
  for (const path of paths) {
    const entry = await stat(path).catch(() => undefined);
    if (entry === undefined) throw new Error(`${path}: no such file or directory`);
    if (!entry.isDirectory()) {
      files.push(path);
      continue;
    }
    for (const name of (await readdir(path)).filter((n) => n.endsWith(SUFFIX)).sort()) {
      files.push(join(path, name));
    }
  }
  return await Promise.all(files.map(async (file) => ({ ...(await inspectBundle(file)), file })));
}

/**
 * Delivery order and the prerequisites nobody here supplies. Throws the kit's `BundleOrderError`
 * on a cycle or a duplicate id, because neither has a right answer to fall back to.
 */
export function deliveryOrder(packed: readonly Packed[]): Delivery {
  const order = familyOrder(packed);
  const here = new Set(packed.map((bundle) => bundle.id));
  const external = [...new Set(packed.flatMap((bundle) => [...bundle.requiredDependencies]))]
    .filter((id) => !here.has(id))
    .sort();
  return { order, external };
}

if (import.meta.main) {
  const args = process.argv.slice(2);
  if (args.length === 0) {
    process.stderr.write("usage: bun scripts/delivery-order.ts <directory | bundle>...\n");
    process.exit(2);
  }
  try {
    const packed = await packedIn(args);
    if (packed.length === 0) throw new Error(`no ${SUFFIX} bundle in ${args.join(", ")}`);
    const { order, external } = deliveryOrder(packed);
    if (external.length > 0) {
      process.stderr.write(
        `delivery-order: required here and delivered by nobody, so the hub must already have ${external.join(", ")}\n`,
      );
    }
    for (const bundle of order) {
      process.stdout.write(`${bundle.id} ${bundle.file} ${bundle.sha256}\n`);
    }
  } catch (error) {
    process.stderr.write(
      `delivery-order: ${error instanceof Error ? error.message : String(error)}\n`,
    );
    process.exit(1);
  }
}
