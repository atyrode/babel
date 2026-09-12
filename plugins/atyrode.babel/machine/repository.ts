import { realpathSync } from "node:fs";
import { stat } from "node:fs/promises";
import { isAbsolute, resolve } from "node:path";

/*
  WHAT THE WORK WAS ABOUT, ported from internal/adapter/repository.go (§4.13).

  A workspace path is a locator. Two worktrees of one repository are two paths and one project,
  a path under /tmp names nothing durable, and a generated worktree name ("witty-sage-crab") is
  a directory rather than a subject. The repository's own identity survives all three, so it is
  what Babel observes and files under.

  Exactly one of `identity` and `reason` is set. An identity means the observation succeeded; a
  reason means it did not and says why — §3's rule that an absent value is explained rather
  than synthesized. A reason is never a guessed identity, and `remote` is empty whenever the
  checkout declares no origin: a repository nobody published is still one repository.

  Every probe is read-only. `rev-parse` and `remote get-url` read git's own metadata; nothing
  here runs a command that touches the index, the worktree or the network, because observing
  where a session's work happened must never modify it.
*/

export interface Repository {
  /**
   * The absolute path of the repository's git common directory. It is the identity rather
   * than the remote because every worktree of a repository shares exactly one common
   * directory, while a remote can be absent, renamed, or shared by a fork.
   */
  identity: string | null;
  /**
   * The origin URL normalized to host/owner/repo — no scheme, no credentials, no ".git", no
   * trailing slash — and empty when the checkout has no origin. It is what makes the same
   * repository cloned to two paths on two machines recognisable as one.
   */
  remote: string;
  reason: string | null;
}

/** The reasons an identity is absent: each names a state of this machine, none a fault of the session. */
export const REPOSITORY_REASONS = {
  notARepository: "not a git repository",
  workspaceAbsent: "workspace absent on this host",
  noWorkspace: "session records no workspace",
  gitUnavailable: "git unavailable",
} as const;

/**
 * Bounds one git invocation. Both probes read local metadata and answer in milliseconds; the
 * bound exists so a workspace on an unresponsive network mount costs a scan one second rather
 * than stalling it, and a timeout is an absent identity like any other failed observation.
 */
const PROBE_TIMEOUT_MS = 1000;

export interface RepositoryObserver {
  /** The repository identity of one workspace, observed once per distinct workspace. */
  observe(workspace: string | null): Promise<Repository>;
}

/**
 * An observer for one scan. The cache is the reason this is a type rather than a function: a
 * scan describes thousands of sessions and the operator's machine holds tens of workspaces, so
 * observing per session would run two subprocesses per session to re-derive an answer the scan
 * already has. One observer lives for one scan, which is also what keeps the answer internally
 * consistent — every row a scan writes saw the same state of the disk.
 */
export function repositoryObserver(): RepositoryObserver {
  const observed = new Map<string, Promise<Repository>>();
  // Resolved once: a machine without git answers every workspace with the same reason instead
  // of paying a PATH walk per workspace.
  let git: string | null | undefined;

  const probe = async (workspace: string): Promise<Repository> => {
    git ??= Bun.which("git");
    if (git === null) {
      return { identity: null, remote: "", reason: REPOSITORY_REASONS.gitUnavailable };
    }
    const info = await stat(workspace).catch(() => null);
    if (info?.isDirectory() !== true) {
      return { identity: null, remote: "", reason: REPOSITORY_REASONS.workspaceAbsent };
    }
    const common = await runGit(git, workspace, ["rev-parse", "--git-common-dir"]);
    if (common === "") {
      return { identity: null, remote: "", reason: REPOSITORY_REASONS.notARepository };
    }
    // git answers relative to the directory it ran in, so a plain checkout reports ".git" and
    // only a linked worktree reports an absolute path. Resolving against the workspace is what
    // collapses both to one identity.
    let identity = isAbsolute(common) ? resolve(common) : resolve(workspace, common);
    // Symlinks are resolved because two workspaces reached through different symlinked
    // prefixes are one repository, and comparing unresolved paths would file them as two. A
    // path that cannot be resolved is used as it stands: what git printed is still the
    // repository's.
    try {
      identity = realpathSync(identity);
    } catch {
      /* the unresolved path is the answer */
    }
    const origin = await runGit(git, workspace, ["remote", "get-url", "origin"]);
    return { identity, remote: normalizeRemote(origin), reason: null };
  };

  return {
    observe(workspace) {
      const path = workspace?.trim() ?? "";
      if (path === "") {
        return Promise.resolve({ identity: null, remote: "", reason: REPOSITORY_REASONS.noWorkspace });
      }
      let pending = observed.get(path);
      if (pending === undefined) {
        pending = probe(path);
        observed.set(path, pending);
      }
      return pending;
    },
  };
}

/**
 * One read-only git probe's first line of output. A failure of any kind — git refusing, the
 * directory vanishing, the timeout expiring — is reported as no answer, because every caller
 * here has a reason to record and nothing to retry.
 */
async function runGit(git: string, workspace: string, args: readonly string[]): Promise<string> {
  const child = Bun.spawn([git, "-C", workspace, ...args], {
    // The probe never prompts and never takes a lock: GIT_OPTIONAL_LOCKS=0 is what makes this
    // read-only in git's own terms, and a terminal prompt in a background scan would hang it.
    // The operator's own git configuration is left in place deliberately — safe.directory and
    // url.insteadOf are how this machine's git reads this machine's checkouts, and observing
    // through a different configuration would report a repository he does not have.
    env: { ...process.env, GIT_TERMINAL_PROMPT: "0", GIT_OPTIONAL_LOCKS: "0" },
    stdin: "ignore",
    stderr: "ignore",
    stdout: "pipe",
    timeout: PROBE_TIMEOUT_MS,
  });
  const out = await new Response(child.stdout).text().catch(() => "");
  if ((await child.exited) !== 0) return "";
  const line = out.trim();
  const end = line.search(/[\r\n]/u);
  return end < 0 ? line : line.slice(0, end).trim();
}

/**
 * States a git remote URL as host/owner/repo, and returns "" for a URL it cannot read that
 * way.
 *
 * The normalization is what makes one repository one topic. git's own URL grammar writes the
 * same GitHub repository as git@github.com:atyrode/manifold.git,
 * https://github.com/atyrode/manifold, https://token@github.com/atyrode/manifold.git/ and
 * ssh://git@github.com/atyrode/manifold — four strings, one project — so the scheme, the
 * credentials, the ".git" suffix and the trailing slash are removed and the ssh short form's
 * colon becomes the separator it means.
 *
 * A local path remote ("/srv/git/thing", "../other") normalizes to nothing: it names a
 * directory on one machine, which is a locator and not an identity, and the common directory
 * is already the better answer for it.
 */
export function normalizeRemote(url: string): string {
  let remote = url.trim();
  if (remote === "") return "";
  const scheme = remote.indexOf("://");
  if (scheme >= 0) {
    remote = remote.slice(scheme + 3);
  } else {
    const colon = remote.indexOf(":");
    // The scp-like short form, [user@]host:owner/repo. Its colon is a separator rather than a
    // port, which is why it is rewritten here and not for a URL that carried a scheme.
    if (colon >= 0 && !remote.slice(0, colon).includes("/")) {
      remote = remote.slice(0, colon) + "/" + remote.slice(colon + 1);
    }
  }
  // Credentials in a URL that had a scheme: user[:password]@host.
  const at = remote.indexOf("@");
  if (at >= 0) remote = remote.slice(at + 1);
  remote = remote.replace(/^\/+|\/+$/gu, "");
  if (remote === "" || remote.startsWith(".")) return "";
  const parts: string[] = [];
  for (const part of remote.split("/")) {
    if (part === "" || part === ".") continue;
    parts.push(part);
  }
  if (parts.length < 2) return "";
  const last = parts.length - 1;
  const tail = parts[last];
  if (tail === undefined) return "";
  parts[last] = tail.endsWith(".git") ? tail.slice(0, -".git".length) : tail;
  if (parts[last] === "") return "";
  // A host element carries a dot or is localhost; anything else is a path, and a path remote
  // is a locator rather than a repository identity.
  const first = parts[0] ?? "";
  const host = first.includes(":") ? first.slice(0, first.indexOf(":")) : first;
  if (!host.includes(".") && host !== "localhost") return "";
  parts[0] = host;
  return parts.join("/");
}
