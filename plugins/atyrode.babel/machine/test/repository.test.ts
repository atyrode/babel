import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { realpathSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { REPOSITORY_REASONS, normalizeRemote, repositoryObserver } from "../repository.ts";

/*
  The repository observation against real git checkouts, because the only claim worth testing
  here is the one about git's own answers: a linked worktree and its checkout are two paths and
  one project, and the identity has to collapse them. A stubbed `git` would test the stub.
*/

let root = "";
let checkout = "";
let worktree = "";

async function git(cwd: string, ...args: string[]): Promise<void> {
  const child = Bun.spawn(["git", ...args], {
    cwd,
    env: {
      ...process.env,
      GIT_AUTHOR_NAME: "Babel Test",
      GIT_AUTHOR_EMAIL: "test@example.invalid",
      GIT_COMMITTER_NAME: "Babel Test",
      GIT_COMMITTER_EMAIL: "test@example.invalid",
      GIT_CONFIG_GLOBAL: "/dev/null",
      GIT_CONFIG_SYSTEM: "/dev/null",
    },
    stdout: "ignore",
    stderr: "pipe",
  });
  if ((await child.exited) !== 0) {
    throw new Error(`git ${args.join(" ")}: ${await new Response(child.stderr).text()}`);
  }
}

beforeAll(async () => {
  root = await mkdtemp(join(tmpdir(), "babel-repository-"));
  checkout = join(root, "checkout");
  worktree = join(root, "witty-sage-crab");
  await mkdir(checkout, { recursive: true });
  await git(checkout, "init", "--initial-branch=main");
  await git(checkout, "remote", "add", "origin", "git@github.com:atyrode/babel.git");
  await writeFile(join(checkout, "README.md"), "one commit, so a worktree can be added\n");
  await git(checkout, "add", "README.md");
  await git(checkout, "commit", "-m", "the first commit");
  await git(checkout, "worktree", "add", worktree, "-b", "side");
  await mkdir(join(root, "plain"), { recursive: true });
});

afterAll(async () => {
  await rm(root, { recursive: true, force: true });
});

describe("a workspace's repository identity", () => {
  test("a worktree and its checkout are one repository", async () => {
    const observer = repositoryObserver();
    const fromCheckout = await observer.observe(checkout);
    const fromWorktree = await observer.observe(worktree);
    const common = realpathSync(join(checkout, ".git"));

    expect(fromCheckout.identity).toBe(common);
    expect(fromWorktree.identity).toBe(common);
    expect(fromCheckout.reason).toBeNull();
    // The remote is the same project stated once, whatever URL grammar the checkout used.
    expect(fromCheckout.remote).toBe("github.com/atyrode/babel");
    expect(fromWorktree.remote).toBe("github.com/atyrode/babel");
  });

  test("a directory outside git has a reason, not an identity", async () => {
    const observed = await repositoryObserver().observe(join(root, "plain"));
    expect(observed.identity).toBeNull();
    expect(observed.remote).toBe("");
    expect(observed.reason).toBe(REPOSITORY_REASONS.notARepository);
  });

  test("a workspace this host does not hold is named as absent", async () => {
    const observed = await repositoryObserver().observe(join(root, "never-existed"));
    expect(observed.identity).toBeNull();
    expect(observed.reason).toBe(REPOSITORY_REASONS.workspaceAbsent);
  });

  test("a session that recorded no workspace has nothing to observe", async () => {
    const observer = repositoryObserver();
    expect((await observer.observe(null)).reason).toBe(REPOSITORY_REASONS.noWorkspace);
    expect((await observer.observe("   ")).reason).toBe(REPOSITORY_REASONS.noWorkspace);
  });

  test("one workspace is probed once, however many sessions name it", async () => {
    const observer = repositoryObserver();
    const [first, second, third] = await Promise.all([
      observer.observe(checkout),
      observer.observe(checkout),
      observer.observe(checkout),
    ]);
    // The same object: a scan of thousands of sessions over tens of workspaces runs two
    // subprocesses per workspace, not per session, and every row sees one state of the disk.
    expect(second).toBe(first);
    expect(third).toBe(first);
  });

  test("a repository with no origin is still one repository", async () => {
    const lonely = join(root, "unpublished");
    await mkdir(lonely, { recursive: true });
    await git(lonely, "init", "--initial-branch=main");
    const observed = await repositoryObserver().observe(lonely);
    expect(observed.identity).toBe(realpathSync(join(lonely, ".git")));
    expect(observed.remote).toBe("");
    expect(observed.reason).toBeNull();
  });
});

describe("normalizeRemote states one repository one way", () => {
  const same: Record<string, string> = {
    "git@github.com:atyrode/manifold.git": "github.com/atyrode/manifold",
    "https://github.com/atyrode/manifold": "github.com/atyrode/manifold",
    "https://token@github.com/atyrode/manifold.git/": "github.com/atyrode/manifold",
    "ssh://git@github.com/atyrode/manifold": "github.com/atyrode/manifold",
    "ssh://git@github.com:2222/atyrode/manifold.git": "github.com/atyrode/manifold",
    "https://user:password@gitlab.example.com/group/thing.git": "gitlab.example.com/group/thing",
  };
  for (const [url, expected] of Object.entries(same)) {
    test(url, () => {
      expect(normalizeRemote(url)).toBe(expected);
    });
  }

  const locators = ["/srv/git/thing", "../other", "./thing.git", "thing.git", "", "   "];
  for (const url of locators) {
    test(`a locator is not an identity: ${JSON.stringify(url)}`, () => {
      expect(normalizeRemote(url)).toBe("");
    });
  }
});
