import { readdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

/*
  The release rebuilds dependencies independently of the plugin gate. A matching source pin
  does not mean matching bundle bytes: record the gate's closure only after verify succeeds,
  then compare the rebuilt artifact before attaching or installing anything. Roles are part
  of membership, so an omp bundle moved from hardened to in-realm is not the verified closure.
*/
type Digest = { file: string; sha256: string };

async function digests(directory: string, prefix: string, recursive: boolean): Promise<Digest[]> {
  const result: Digest[] = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    const file = `${prefix}${entry.name}`;
    if (entry.isDirectory() && recursive) {
      result.push(...(await digests(path, `${file}/`, true)));
    } else if (entry.name.endsWith(".manifold-plugin.json")) {
      if (!entry.isFile()) throw new Error(`${path}: expected a regular bundle file`);
      const sha256 = new Bun.CryptoHasher("sha256").update(await readFile(path)).digest("hex");
      result.push({ file, sha256 });
    } else if (entry.isSymbolicLink()) {
      throw new Error(`${path}: a dependency artifact may not hide bundles behind a symlink`);
    }
  }
  return result;
}

function canonical(entries: Digest[]): string {
  return entries
    .sort((a, b) => (a.file < b.file ? -1 : a.file > b.file ? 1 : 0))
    .map(({ file, sha256 }) => `${sha256}  ${file}\n`)
    .join("");
}

/** The same flat bundle sets the SDK verifier receives, named by their delivery role. */
export async function recordDependencyReceipt(
  receipt: string,
  hardened: string,
  inRealm: string,
): Promise<void> {
  const omp = await digests(hardened, "hardened/", false);
  const code = await digests(inRealm, "in-realm/", false);
  if (omp.length === 0 || code.length === 0) {
    throw new Error("both hardened and in-realm dependency bundles are required");
  }
  await writeFile(receipt, canonical([...omp, ...code]));
}

/** Walk the whole downloaded artifact, not only paths listed in its own checksum file. */
export async function checkDependencyReceipt(receipt: string, dependencies: string): Promise<void> {
  const expected = await readFile(receipt, "utf8");
  const actual = canonical(await digests(dependencies, "", true));
  if (expected.length === 0 || actual !== expected) {
    const wanted = new Set(expected.trimEnd().split("\n"));
    const found = new Set(actual.trimEnd().split("\n"));
    throw new Error(
      `rebuilt dependency closure differs from the verified receipt ${receipt}\n` +
        `only in gate receipt:\n${[...wanted].filter((line) => !found.has(line)).join("\n")}\n` +
        `only in rebuilt closure:\n${[...found].filter((line) => !wanted.has(line)).join("\n")}`,
    );
  }
}

if (import.meta.main) {
  const [mode, receipt, first, second, ...extra] = process.argv.slice(2);
  try {
    if (mode === "record" && receipt && first && second && extra.length === 0) {
      await recordDependencyReceipt(receipt, first, second);
    } else if (mode === "check" && receipt && first && second === undefined) {
      await checkDependencyReceipt(receipt, first);
      console.log("Rebuilt dependency bytes and membership match the plugin gate.");
    } else {
      throw new Error(
        "usage: bun scripts/dependency-receipt.ts record <receipt> <hardened-dir> <in-realm-dir>\n" +
          "       bun scripts/dependency-receipt.ts check <receipt> <dependencies-dir>",
      );
    }
  } catch (error) {
    process.stderr.write(
      `dependency-receipt: ${error instanceof Error ? error.message : String(error)}\n`,
    );
    process.exit(1);
  }
}
