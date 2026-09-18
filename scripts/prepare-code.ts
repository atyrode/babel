#!/usr/bin/env bun
/*
  THE CODE BUNDLES BABEL VERIFIES AGAINST, built from a sibling checkout.

  `atyrode.babel` declares `atyrode.code` a REQUIRED dependency (manifest.json): a hub that
  enabled Babel without Code would offer a Start section whose every press the host itself
  refuses, and assembly refusing the install is the earlier and better answer. But a required
  dependency is a dependency ASSEMBLY CHECKS — `bun run verify` installs Babel on a disposable
  engine and gets `artifact_invalid: … requires plugin "atyrode.code", which is not composed`
  unless Code is composed there first. So verification has to install omp, then Code, then
  Babel, and this is what puts the first two on disk.

  IT IS CODE'S OWN MACHINERY, NOT A SECOND ONE. Code's `prepare:integration` already builds
  omp's bundles from ITS sibling checkout, and Code's `pack` builds Code's; this script does no
  packing of its own. It fetches atyrode/code at `CODE_REV`, arranges the sibling layout that
  script expects (`<snapshot>/code` beside `<snapshot>/manifold`), and runs Code's own scripts
  inside it. A second packer here would be a second answer to what a Code bundle is, and the
  day Code changed its own it would be the copy nobody updated.

  THE PIN IS ONE NUMBER IN TWO PLACES AND A TEST HOLDS THEM TOGETHER. `CODE_REV` is what this
  fetches; `package.json`'s `@atyrode/manifold-code` is what the TYPES come from.
  `test/contract.test.ts` refuses a tree where they disagree, because verifying against one
  revision while compiling against another proves nothing about either.

  Nothing here installs a daemon, a credential or a native service configuration: it produces
  bundle files, and the disposable engine `verify` starts is what installs them.
*/
import { lstat, mkdir, readFile, realpath, rm, symlink } from "node:fs/promises";
import { join, resolve } from "node:path";

/** This repository root: the plugin family is the repository, so `scripts/` sits beside it. */
const repo = resolve(import.meta.dir, "..");
const repository = "https://github.com/atyrode/code.git";

const revision = (await readFile(join(repo, "CODE_REV"), "utf8")).trim();
if (!/^[a-f0-9]{40}$/.test(revision)) {
  throw new Error("CODE_REV must name one published Git commit of atyrode/code");
}
const manifoldRevision = (await readFile(join(repo, "MANIFOLD_REV"), "utf8")).trim();

/** The SDK checkout every script in this tree builds against (`manifold-dir.sh`). */
const manifold = await realpath((await run([join(repo, "manifold-dir.sh")], repo)).trim());

const root = join(repo, ".integration");
const snapshot = join(root, revision);
const source = join(snapshot, "code");

async function run(command: string[], cwd: string): Promise<string> {
  const child = Bun.spawn(command, {
    cwd,
    env: { ...process.env, GIT_TERMINAL_PROMPT: "0" },
    stdin: "ignore",
    stdout: "pipe",
    stderr: "inherit",
  });
  const [status, output] = await Promise.all([child.exited, new Response(child.stdout).text()]);
  if (status !== 0) {
    throw new Error(`preparing Code failed: ${command.join(" ")} (in ${cwd})`);
  }
  return output.trim();
}

async function exists(path: string): Promise<boolean> {
  try {
    await lstat(path);
    return true;
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return false;
    throw error;
  }
}

await mkdir(snapshot, { recursive: true });
if (!(await exists(source))) {
  await run(["git", "init", "--quiet", source], snapshot);
  await run(["git", "fetch", "--quiet", "--depth=1", repository, revision], source);
  await run(["git", "checkout", "--quiet", "--detach", "FETCH_HEAD"], source);
}
if ((await run(["git", "rev-parse", "HEAD"], source)) !== revision) {
  throw new Error("the prepared Code source does not match CODE_REV");
}

/*
  ONE SDK FOR ALL THREE. Code's `prepare:integration` refuses a checkout whose `MANIFOLD_REV`
  is not its own, and omp's is checked against Code's there too — so a Babel that verified
  against a Code built on another Manifold would be proving nothing about the hub it installs
  on. The link is what makes Code's `../../manifold` this tree's own checkout.

  THE `plugins/` BELOW IS CODE'S, NOT THIS REPOSITORY'S. Babel's own pin sits at its root
  because Babel is nothing but its plugin family; Code's sits under `plugins/` because a Go
  product sits beside it. A rewrite that flattened this path with Babel's own would read a file
  Code does not have, which is exactly what it did once.
*/
if ((await readFile(join(source, "plugins/MANIFOLD_REV"), "utf8")).trim() !== manifoldRevision) {
  throw new Error(
    `Code at ${revision} builds against another Manifold than MANIFOLD_REV; move one pin`,
  );
}
const sdkLink = join(snapshot, "manifold");
if (await exists(sdkLink)) {
  if ((await realpath(sdkLink)) !== manifold) {
    throw new Error("the prepared SDK link belongs to another checkout");
  }
} else {
  await symlink(manifold, sdkLink, "dir");
}

const upstream = join(source, "plugins");
await run([process.execPath, "install", "--frozen-lockfile"], upstream);
await run([process.execPath, "--no-env-file", "run", "prepare:integration"], upstream);
await run([process.execPath, "--no-env-file", "run", "pack"], upstream);

/*
  The pointer is ours and is replaced atomically, so a consumer only ever sees a snapshot the
  upstream packers have already succeeded on.
*/
const current = join(root, "code");
await rm(current, { force: true, recursive: false }).catch(() => undefined);
await symlink(`${revision}/code`, current, "dir");

console.log(
  `Prepared Code ${revision.slice(0, 12)} and its OMP bundles; no daemon, credential or ` +
    `service configuration was installed.`,
);
