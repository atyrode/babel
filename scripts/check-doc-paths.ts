#!/usr/bin/env bun
/*
  EVERY PATH A DOCUMENT NAMES EXISTS, AND EVERY SCRIPT IT NAMES IS A SCRIPT.

  This is the mechanical half of documentation drift, and it is the half worth automating. A
  document that restates behaviour goes stale silently and a reader has to know the code to catch
  it; a document that names `store/schema.ts` after `store/` moved is wrong in a way a program can
  see. Three such misses were found by hand in this repository's own sweep — a page that had been
  deleted, a directory that never existed, and a file that had been split in two — and all three
  had been wrong for weeks.

  WHAT IS CHECKED: every backticked path with a `/` in it, and every `bun run <script>`, in every
  tracked Markdown file.

  WHAT IS NOT, and each exemption is a rule rather than a list of forgiven lines:

  - A BARE FILENAME (`manifest.json`, `web.tsx`) is prose, not a path. It names a shape that
    recurs in several directories, and resolving it would mean guessing which one was meant.
  - AN ABSOLUTE PATH (`/run/code/account-pool.json`) is a runtime location on a machine, not a
    file in this repository. A postmortem naming one is citing evidence.
  - A FOREIGN PREFIX belongs to another repository, and this checkout has no opinion on whether
    it exists. Each is listed below WITH ITS OWNER, so the list cannot quietly become a
    dumping ground for local paths somebody could not make resolve.
  - A GENERATED ARTIFACT is named because the build writes it, and it is absent in a clean tree
    by construction.

  A path resolves against the repository root, the document's own directory, or `babel/` —
  that last one because a document about the plugin writes `store/schema.ts` and means the
  baseline's, which is the convention these documents already use and is worth keeping: the
  alternative is every line carrying a prefix that never varies.
*/

import { readFile } from "node:fs/promises";
import { join } from "node:path";

/** Where a path may resolve from. The plugin root is the shorthand these documents already use. */
const ROOTS = ["", "babel/"] as const;

/** Another repository's paths, by owner. A local path may never be added here. */
const FOREIGN: readonly { readonly prefix: string; readonly owner: string }[] = [
  { prefix: "packages/", owner: "atyrode/manifold" },
  { prefix: "docs/PLUGINS.md", owner: "atyrode/manifold" },
  { prefix: "docs/decisions/", owner: "atyrode/manifold" },
  { prefix: "docs/ENROLL.md", owner: "atyrode/manifold" },
  { prefix: "agent/src/", owner: "atyrode/manifold" },
  { prefix: "scripts/gate.sh", owner: "atyrode/code" },
  { prefix: "plugins/", owner: "atyrode/code, whose plugin family is under plugins/" },
  { prefix: "dotfiles/", owner: "atyrode/dotfiles" },
  { prefix: "modules/home/", owner: "atyrode/dotfiles" },
  { prefix: "docs/agent-tools.md", owner: "atyrode/dotfiles" },
];

/**
 * THE CHANGELOG IS A LEDGER, NOT A DESCRIPTION.
 *
 * An entry saying what `cookbook/preamble.md` held in a released version is a record of that
 * release, and it stays true after the file is deleted. Checking it would force either rewriting
 * history or never naming a file in a release note, and both are worse than the drift this
 * checker exists to catch — which is a document that describes the tree as it is NOW.
 */
const LEDGERS: readonly string[] = ["CHANGELOG.md"];

/** Written by the build, absent in a clean checkout. Each says what writes it. */
const GENERATED: readonly { readonly path: string; readonly by: string }[] = [
  { path: "babel/machine.js", by: "pack.sh, which deletes it again" },
  { path: "dist/SHA256SUMS", by: "pack.sh" },
];

const PATH_IN_TICKS =
  /`([A-Za-z0-9_@./-]+\.(?:ts|tsx|js|json|jsonc|md|yml|yaml|sh|css|nix|lock|db|go))`/g;
const MARKDOWN_LINK = /\]\(([A-Za-z0-9_./-]+\.md)(?:#[A-Za-z0-9_-]*)?\)/g;
const BUN_SCRIPT = /`bun run ([a-z:-]+)`/g;

interface Miss {
  readonly file: string;
  readonly line: number;
  readonly what: string;
  readonly why: string;
}

async function tracked(): Promise<readonly string[]> {
  const git = Bun.spawn(["git", "ls-files", "*.md"], { stdout: "pipe" });
  const [status, out] = await Promise.all([git.exited, new Response(git.stdout).text()]);
  if (status !== 0) throw new Error("git ls-files failed");
  return out.trim().split("\n").filter(Boolean);
}

async function resolves(path: string, dir: string): Promise<boolean> {
  for (const candidate of [...ROOTS.map((root) => root + path), join(dir, path)]) {
    if (await Bun.file(candidate).exists()) return true;
  }
  return false;
}

export async function documentMisses(
  files: readonly string[],
  scripts: Set<string>,
): Promise<readonly Miss[]> {
  const misses: Miss[] = [];
  for (const file of files) {
    if (LEDGERS.includes(file)) continue;
    const dir = file.includes("/") ? file.slice(0, file.lastIndexOf("/")) : ".";
    const lines = (await readFile(file, "utf8")).split("\n");
    for (const [index, line] of lines.entries()) {
      const at = index + 1;
      const named = new Set<string>();
      for (const match of line.matchAll(PATH_IN_TICKS)) named.add(match[1] ?? "");
      for (const match of line.matchAll(MARKDOWN_LINK)) named.add(match[1] ?? "");
      for (const path of named) {
        if (path === "" || !path.includes("/") || path.startsWith("/")) continue;
        if (FOREIGN.some((entry) => path.startsWith(entry.prefix))) continue;
        if (GENERATED.some((entry) => entry.path === path)) continue;
        if (!(await resolves(path, dir))) {
          misses.push({ file, line: at, what: path, why: "no such file" });
        }
      }
      for (const match of line.matchAll(BUN_SCRIPT)) {
        const name = match[1] ?? "";
        if (name !== "" && !scripts.has(name)) {
          misses.push({
            file,
            line: at,
            what: `bun run ${name}`,
            why: "package.json has no such script",
          });
        }
      }
    }
  }
  return misses;
}

if (import.meta.main) {
  const manifest = (await Bun.file("package.json").json()) as { scripts?: Record<string, string> };
  const misses = await documentMisses(
    await tracked(),
    new Set(Object.keys(manifest.scripts ?? {})),
  );
  for (const miss of misses) {
    process.stdout.write(`${miss.file}:${String(miss.line)}  ${miss.what}  (${miss.why})\n`);
  }
  if (misses.length > 0) {
    process.stdout.write(
      `\n${String(misses.length)} documentation reference(s) name something that does not exist.\n` +
        "A path resolves from the repository root, the document's own directory, or babel/.\n",
    );
    process.exit(1);
  }
  process.stdout.write("every documentation path and script name resolves\n");
}
