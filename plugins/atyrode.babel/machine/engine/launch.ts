/*
  THE LAUNCH: `code engine --profile <id@revision> --runtime-info <path>` and the boundary Babel
  refuses it at. Ported from internal/worker (worker.go's launch, env, argv, ready/admit and
  runtime.go's `code.runtime/1` sidecar), which SPEC.md §2.6 makes Babel's own: Code owns the
  profile, the credential and the sandbox; Babel owns whether this engine may be prompted at all.

  Two facts carry the whole design.

  The sidecar is a file, not a frame. Code writes `code.runtime/1` before it forwards a byte of
  the engine's stdout, so the native stream stays native and the one Code-specific document is
  read by exactly the process that asked for it. It names the profile, the disclosure class, the
  cost per 1k and the containment; Watch states what will run from the same source, and the
  receipt records it (plan §5).

  Containment is a declaration Babel cannot verify and therefore refuses rather than believes.
  `containmentShortfall` names every gap at once, the platform gate refuses a host with no
  qualified backend whatever Code claims, and the caller closes stdin with nothing written when
  either fails — what a refused engine has seen is the profile it was launched under.
*/

import { spawn } from "node:child_process";
import type { ChildProcess } from "node:child_process";
import type { Readable } from "node:stream";
import { z } from "zod";

// ---------------------------------------------------------------------------- the sidecar

/** The schema of Code's runtime-info document. A launch declaring another is not this build's. */
export const RUNTIME_INFO_SCHEMA = "code.runtime/1";

const ENGINE_SUBCOMMAND = "engine";
const FLAG_PROFILE = "--profile";
const FLAG_RUNTIME_INFO = "--runtime-info";
const FLAG_DESCRIBE = "--describe";

/** One Code-owned analysis profile. Babel stores the reference and never what is behind it. */
export const ProfileRefSchema = z.strictObject({
  id: z.string().min(1),
  revision: z.number().int().min(0),
});
export type ProfileRef = z.infer<typeof ProfileRefSchema>;

/**
 * The sandbox Code declares it launched the engine into. Every field is a claim: Babel refuses a
 * launch that falls short before any prompt is written and records the claim in the receipt, so a
 * later reviewer sees which boundary the evidence was produced behind.
 */
export const ContainmentSchema = z.object({
  backend: z.string().default(""),
  filesystem_isolation: z.boolean().default(false),
  network_default_deny: z.boolean().default(false),
  resource_ceilings: z.boolean().default(false),
  disposable: z.boolean().default(false),
  escape: z.string().default(""),
});
export type Containment = z.infer<typeof ContainmentSchema>;

/**
 * `code.runtime/1`. Loose rather than strict: a newer Code's additions are visible as unknown
 * fields (`unknownRuntimeFields`) rather than failing a launch that is otherwise well formed.
 */
export const RuntimeReportSchema = z.looseObject({
  schema: z.string(),
  worker: z.object({ name: z.string().min(1), version: z.string().default("") }),
  profile: ProfileRefSchema,
  privacy: z
    .object({ disclosure: z.string().default(""), redaction_required: z.boolean().default(false) })
    .optional(),
  cost: z
    .object({
      currency: z.string().default(""),
      input_per_1k: z.number().default(0),
      output_per_1k: z.number().default(0),
      estimated_run: z.number().default(0),
    })
    .optional(),
  metadata: z.record(z.string(), z.string()).optional(),
  containment: ContainmentSchema.nullish(),
  /** Present only in the report Code rewrites after the engine exits; how a reader tells them apart. */
  finished: z.boolean().default(false),
  exit_code: z.number().int().nullish(),
  resources: z
    .object({
      cpu_seconds: z.number().optional(),
      max_rss_bytes: z.number().optional(),
      sandbox_bytes_written: z.number().optional(),
    })
    .nullish(),
  resources_provenance: z.string().default(""),
});
export type RuntimeReport = z.infer<typeof RuntimeReportSchema>;

/** The top-level keys this build reads; anything else is reported rather than silently dropped. */
const KNOWN_RUNTIME_FIELDS: Record<string, true> = {
  schema: true,
  worker: true,
  profile: true,
  privacy: true,
  cost: true,
  metadata: true,
  containment: true,
  finished: true,
  exit_code: true,
  resources: true,
  resources_provenance: true,
};

/** Metadata key fragments that name a credential. A profile declaring one is refused whole. */
const SECRET_KEY_MARKERS = ["key", "token", "secret", "password", "credential", "auth"] as const;

// ---------------------------------------------------------------------------- failures

/**
 * Why a supervised run ended. They are codes rather than prose because a caller decides
 * differently between an engine that never spoke, one Babel refused, one that stalled, and one
 * that answered and then would not leave; the receipt records the code and the message.
 */
export const ENGINE_FAILURES = {
  handshake: "handshake",
  protocol: "protocol",
  runtimeInfo: "runtime-info",
  profileMismatch: "profile-mismatch",
  containment: "containment",
  platform: "platform",
  secretDeclared: "secret-declared",
  malformedFrame: "malformed-frame",
  oversizedFrame: "oversized-frame",
  commandFailed: "command-failed",
  resultSchema: "result-schema",
  noResult: "no-result",
  engineExited: "engine-exited",
  stalled: "stalled",
  lingered: "lingered",
  dirtyExit: "dirty-exit",
  eventBudget: "event-budget",
  toolBudget: "tool-budget",
  blinding: "blinding",
  launch: "launch",
} as const;
export type EngineFailureCode = (typeof ENGINE_FAILURES)[keyof typeof ENGINE_FAILURES];

/** One supervised run's failure, carrying the code a receipt records. */
export class EngineFailure extends Error {
  readonly code: EngineFailureCode;
  /** Whether the far side broke the boundary (`engine`) or Babel's own supervision did. */
  readonly origin: "engine" | "babel";

  constructor(code: EngineFailureCode, message: string, origin: "engine" | "babel" = "engine") {
    super(message);
    this.name = "EngineFailure";
    this.code = code;
    this.origin = origin;
  }
}

// ---------------------------------------------------------------------------- admission

/** The containment a run demands. */
export interface Requirement {
  filesystemIsolation: boolean;
  networkDefaultDeny: boolean;
  resourceCeilings: boolean;
  disposable: boolean;
}

/**
 * What every exploration and evaluation run demands. Strict deliberately: a weaker default would
 * silently become the norm, and the operator who wants to relax it should have to say so per run.
 */
export const SANDBOXED_RUN: Requirement = {
  filesystemIsolation: true,
  networkDefaultDeny: true,
  resourceCeilings: true,
  disposable: true,
};

/** The requirement of a run that genuinely needs no boundary; relaxing is a statement, not a zero. */
export const UNSANDBOXED: Requirement = {
  filesystemIsolation: false,
  networkDefaultDeny: false,
  resourceCeilings: false,
  disposable: false,
};

/**
 * Platforms with a sandbox backend that has passed its escape scenario (SPEC.md §10). It is a
 * constant rather than a check inline so that widening the set is one edit with one reason.
 */
const QUALIFIED_PLATFORMS: Record<string, true> = { linux: true };

/**
 * Names every way a declaration falls short of a requirement, or "" when it satisfies it. Every
 * shortfall at once rather than the first, so an operator sees the whole gap in one message
 * instead of fixing them one launch at a time.
 */
export function containmentShortfall(
  containment: Containment | null | undefined,
  requirement: Requirement,
  platform: string = process.platform,
): string {
  if (!containment) return "Code declared no containment for the launch";
  if (containment.backend.trim() === "") return "Code declared no sandbox backend";
  if (containment.escape.trim() === "") {
    return `Code declared no escape assumption for backend ${JSON.stringify(containment.backend)}`;
  }
  const demands =
    requirement.filesystemIsolation ||
    requirement.networkDefaultDeny ||
    requirement.resourceCeilings ||
    requirement.disposable;
  if (demands && QUALIFIED_PLATFORMS[platform] !== true) {
    return `no sandbox backend is qualified on ${platform}; ${containment.backend} has not passed an escape scenario there`;
  }
  const missing: string[] = [];
  if (requirement.filesystemIsolation && !containment.filesystem_isolation) missing.push("filesystem isolation");
  if (requirement.networkDefaultDeny && !containment.network_default_deny) missing.push("network default-deny");
  if (requirement.resourceCeilings && !containment.resource_ceilings) missing.push("resource ceilings");
  if (requirement.disposable && !containment.disposable) missing.push("disposable environment");
  if (missing.length > 0) {
    return `backend ${JSON.stringify(containment.backend)} does not provide ${missing.join(", ")}`;
  }
  return "";
}

/** The refusal for metadata whose keys name a credential, or "" when none do. */
export function metadataShortfall(metadata: Readonly<Record<string, string>> | undefined): string {
  for (const key of Object.keys(metadata ?? {})) {
    const lower = key.toLowerCase();
    for (const marker of SECRET_KEY_MARKERS) {
      if (lower.includes(marker)) return `profile metadata declares a credential under ${JSON.stringify(key)}`;
    }
  }
  return "";
}

/** Decodes a runtime-info document, validating the schema identifier and the shape it promises. */
export function decodeRuntimeReport(text: string): { report: RuntimeReport; unknown: string[] } {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch (error) {
    throw new EngineFailure(ENGINE_FAILURES.runtimeInfo, `runtime-info is not JSON: ${String(error)}`);
  }
  const parsed = RuntimeReportSchema.safeParse(raw);
  if (!parsed.success) {
    throw new EngineFailure(
      ENGINE_FAILURES.runtimeInfo,
      `runtime-info does not decode: ${parsed.error.issues.map((i) => `${i.path.join(".")}: ${i.message}`).join("; ")}`,
    );
  }
  const report = parsed.data;
  if (report.schema !== RUNTIME_INFO_SCHEMA) {
    throw new EngineFailure(
      ENGINE_FAILURES.runtimeInfo,
      `runtime-info declares schema ${JSON.stringify(report.schema)}, this build reads ${RUNTIME_INFO_SCHEMA}`,
    );
  }
  const unknown = Object.keys(raw as Record<string, unknown>)
    .filter((key) => KNOWN_RUNTIME_FIELDS[key] !== true)
    .sort();
  return { report, unknown };
}

/** Reads the sidecar. A missing file after the ready frame means the process on the pipe is not Code. */
export async function readRuntimeReport(path: string): Promise<{ report: RuntimeReport; unknown: string[] }> {
  let text: string;
  try {
    text = await Bun.file(path).text();
  } catch {
    throw new EngineFailure(
      ENGINE_FAILURES.runtimeInfo,
      "Code wrote no runtime-info before the engine became ready",
    );
  }
  return decodeRuntimeReport(text);
}

// ---------------------------------------------------------------------------- limits

/**
 * What bounds the transport and the shutdown, never the analysis: a run is minutes of model work,
 * so the budgets that must be small are the ones bounding silence and departure.
 */
export interface EngineLimits {
  handshakeMs: number;
  idleMs: number;
  exitGraceMs: number;
  terminateGraceMs: number;
  drainGraceMs: number;
  maxFrameBytes: number;
  maxReassembledBytes: number;
  maxEvents: number;
  maxToolCalls: number;
  maxProgress: number;
  stderrTailBytes: number;
}

export const DEFAULT_LIMITS: EngineLimits = {
  handshakeMs: 30_000,
  idleMs: 300_000,
  exitGraceMs: 10_000,
  terminateGraceMs: 2_000,
  drainGraceMs: 2_000,
  maxFrameBytes: 1 << 20,
  maxReassembledBytes: 64 << 20,
  maxEvents: 100_000,
  maxToolCalls: 1024,
  maxProgress: 256,
  stderrTailBytes: 4 << 10,
};

// ---------------------------------------------------------------------------- the process

/** How to launch one engine. `args` are the operator's own, placed before the `engine` subcommand. */
export interface LaunchSpec {
  binary: string;
  args?: readonly string[];
  profile: ProfileRef;
  runtimeInfoPath: string;
  cwd?: string;
  /** Appended to the derived launch environment. It carries no credentials, as argv must not. */
  env?: Readonly<Record<string, string>>;
}

/** Composes the executable's arguments: the operator's own, then the subcommand and Babel's flags. */
export function engineArgv(spec: LaunchSpec, describe = false): string[] {
  const argv = [...(spec.args ?? []), ENGINE_SUBCOMMAND];
  if (describe) argv.push(FLAG_DESCRIBE);
  argv.push(FLAG_PROFILE, `${spec.profile.id}@${spec.profile.revision}`);
  if (!describe) argv.push(FLAG_RUNTIME_INFO, spec.runtimeInfoPath);
  return argv;
}

/**
 * The launch configuration Code needs to resolve the same profile its configuration ceremony
 * saved and to reach the user's systemd manager. Provider credentials and model-selection
 * variables are not inherited.
 */
const INHERITED_ENV = [
  "HOME",
  "PATH",
  "TMPDIR",
  "LANG",
  "XDG_CONFIG_HOME",
  "XDG_DATA_HOME",
  "XDG_STATE_HOME",
  "XDG_CACHE_HOME",
  "XDG_RUNTIME_DIR",
  "DBUS_SESSION_BUS_ADDRESS",
  "CODE_PROFILE_STATE",
  "CODE_OMP",
] as const;

function launchEnv(extra: Readonly<Record<string, string>> | undefined): Record<string, string> {
  const env: Record<string, string> = {};
  for (const key of INHERITED_ENV) {
    const value = process.env[key];
    if (value !== undefined) env[key] = value;
  }
  return Object.assign(env, extra ?? {});
}

/** One launched engine and the lifetime Babel owns: nothing it started outlives `killTree`. */
export interface EngineProcess {
  readonly argv: readonly string[];
  readonly stdout: Readable;
  readonly stderr: Readable;
  /** Writes one newline-terminated command. A closed pipe is not fatal: the stream says why. */
  write(message: unknown): void;
  closeStdin(): void;
  hasExited(): boolean;
  /** The child's status, or -1 when it was signalled or has not exited. */
  exitCode(): number;
  /** Resolves true when the tree left on its own within the grace. */
  awaitExit(graceMs: number): Promise<boolean>;
  /** SIGTERM the whole group, then SIGKILL after the terminate grace. */
  killTree(terminateGraceMs: number): Promise<void>;
}

export function launchEngine(spec: LaunchSpec, describe = false): EngineProcess {
  const argv = engineArgv(spec, describe);
  let child: ChildProcess;
  try {
    child = spawn(spec.binary, argv, {
      cwd: spec.cwd ?? process.cwd(),
      env: launchEnv(spec.env),
      stdio: ["pipe", "pipe", "pipe"],
      // The child leads its own process group, so a kill reaches every process it spawned —
      // the sandbox Code established is in that group, and killing only the direct child
      // would leave it running.
      detached: true,
    });
  } catch (error) {
    throw new EngineFailure(ENGINE_FAILURES.launch, `could not launch ${spec.binary}: ${String(error)}`, "babel");
  }
  return processHandle(child, argv);
}

function processHandle(child: ChildProcess, argv: readonly string[]): EngineProcess {
  let status = -1;
  let exited = false;
  const stdin = child.stdin;
  const stdout = child.stdout;
  const stderr = child.stderr;
  if (!stdin || !stdout || !stderr) {
    throw new EngineFailure(ENGINE_FAILURES.launch, "the engine was launched without its three pipes", "babel");
  }
  // A child whose stdin nobody is reading must not take Babel down with it.
  stdin.on("error", () => {});
  const reaped = Promise.withResolvers<void>();
  child.once("exit", (code) => {
    exited = true;
    status = code ?? -1;
    reaped.resolve();
  });
  child.once("error", () => {
    exited = true;
    reaped.resolve();
  });
  const pid = child.pid;

  const signalTree = (signal: NodeJS.Signals): void => {
    if (pid === undefined) return;
    try {
      // The negative pid signals the group; it is reserved while any member lives, so this
      // cannot reach a recycled group as long as it runs before the child is reaped.
      process.kill(-pid, signal);
    } catch {
      try {
        child.kill(signal);
      } catch {
        /* already gone */
      }
    }
  };

  return {
    argv,
    stdout,
    stderr,
    write(message: unknown): void {
      if (!stdin.destroyed && stdin.writable) stdin.write(`${JSON.stringify(message)}\n`);
    },
    closeStdin(): void {
      if (!stdin.destroyed) stdin.end();
    },
    hasExited: () => exited,
    exitCode: () => status,
    async awaitExit(graceMs: number): Promise<boolean> {
      if (exited) return true;
      const elapsed = Promise.withResolvers<boolean>();
      const timer = setTimeout(() => elapsed.resolve(false), graceMs);
      const left = await Promise.race([reaped.promise.then(() => true), elapsed.promise]);
      clearTimeout(timer);
      return left;
    },
    async killTree(terminateGraceMs: number): Promise<void> {
      if (!exited) {
        signalTree("SIGTERM");
        const grace = Promise.withResolvers<void>();
        const timer = setTimeout(grace.resolve, terminateGraceMs);
        await Promise.race([reaped.promise, grace.promise]);
        clearTimeout(timer);
        if (!exited) signalTree("SIGKILL");
      }
      await reaped.promise;
      stdout.destroy();
      stderr.destroy();
    },
  };
}

// ---------------------------------------------------------------------------- describe

/** A resolved profile as `code engine --describe` reports it: what Watch states before a run. */
export interface Configuration {
  profile: ProfileRef;
  disclosure: string;
  redactionRequired: boolean;
  costPer1k: { input: number; output: number };
  estimatedRun: number;
  currency: string;
  metadata: Readonly<Record<string, string>>;
  worker: { name: string; version: string };
  unknown: readonly string[];
}

/**
 * Asks Code to describe a profile without launching anything: `engine --describe` resolves the
 * profile and reports its reference plus non-secret privacy, cost and provider metadata, and
 * never opens an interface or reaches a provider. Nothing executes to produce it.
 */
export async function describeProfile(spec: LaunchSpec, limits: EngineLimits = DEFAULT_LIMITS): Promise<Configuration> {
  const argv = engineArgv(spec, true);
  const child = Bun.spawn([spec.binary, ...argv], {
    cwd: spec.cwd ?? process.cwd(),
    env: launchEnv(spec.env),
    stdout: "pipe",
    stderr: "pipe",
    stdin: "ignore",
  });
  const deadline = setTimeout(() => child.kill("SIGKILL"), limits.handshakeMs);
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  clearTimeout(deadline);
  if (code !== 0) {
    throw new EngineFailure(
      ENGINE_FAILURES.dirtyExit,
      `describe exited ${code}: ${stderr.trim().slice(-limits.stderrTailBytes)}`,
    );
  }
  const { report, unknown } = decodeRuntimeReport(stdout.trim());
  const secret = metadataShortfall(report.metadata);
  if (secret !== "") throw new EngineFailure(ENGINE_FAILURES.secretDeclared, secret);
  if (report.profile.id !== spec.profile.id || report.profile.revision !== spec.profile.revision) {
    throw new EngineFailure(
      ENGINE_FAILURES.profileMismatch,
      `asked for ${spec.profile.id}@${spec.profile.revision}, Code described ${report.profile.id}@${report.profile.revision}`,
    );
  }
  return {
    profile: report.profile,
    disclosure: report.privacy?.disclosure ?? "",
    redactionRequired: report.privacy?.redaction_required ?? false,
    costPer1k: { input: report.cost?.input_per_1k ?? 0, output: report.cost?.output_per_1k ?? 0 },
    estimatedRun: report.cost?.estimated_run ?? 0,
    currency: report.cost?.currency ?? "",
    metadata: report.metadata ?? {},
    worker: report.worker,
    unknown,
  };
}
