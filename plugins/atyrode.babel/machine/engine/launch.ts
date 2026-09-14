/*
  THE LAUNCH: `omp --mode rpc` inside the job's sandbox, and the boundary Babel refuses it at.

  Until 2026-09-13 this file launched `code engine --profile <id@revision> --runtime-info <path>`
  and read the `code.runtime/1` sidecar Code wrote: Code owned the profile, the credential and
  the sandbox, and Babel owned whether that engine could be prompted at all. atyrode/code#153
  deleted that engine, so Babel is the launcher now (#279), and three things changed and one did
  not.

  BABEL WRITES ITS OWN LAUNCH REPORT; nobody writes one for it. `omp` is a binary, not a
  collaborator — it will not describe the boundary it was put inside — so the report is composed
  HERE from what this process can actually see: the session it was told to run, the binary it
  resolved, the sandbox facts it read out of its own job, and, after the run, the frames the
  engine sent. It is a record and never a claim: every field is something this process observed
  or was handed as a job input, which is exactly the opposite of the old sidecar, where the
  interesting fields were assertions by the process being judged.

  ADMISSION IS NOW OVER FACTS RATHER THAN OVER A DECLARATION. A run may reach a model only
  through the owner's metered proxy, and what makes that true is not a lane string in a sidecar:
  it is that the job's private home holds exactly the two files the OWNER materialized from the
  inference binding (`~/.omp/agent/models.yml`, whose `providers.*.apiKey` the owner spliced, and
  `~/.omp/agent/config.yml`), and that the launch environment carries no provider variable at
  all. Both are checkable, and both are checked before a byte of prompt is written.

  THE SANDBOX IS THE JOB'S, not something a child declares. A Manifold job runs inside the
  machine owner's own boundary — a tmpfs home, a default-deny network unless the operation
  declared otherwise, a delegated cgroup, a read-only root (`agent/src/job-linux.ts`) — and this
  process is INSIDE it. So containment is read from the environment the owner set rather than
  believed from a report, and a run whose surroundings do not look like a Manifold job sandbox is
  refused with the same force as the old missing-sandbox refusal.

  WHAT DID NOT CHANGE: the wire. `client.ts` speaks omp's own RPC (protocol 2, host tools,
  chunked frames), which is what `code engine` was forwarding all along — so the client is
  untouched by all of this, and this file hands it a process rather than a profile.
*/

import { spawn } from "node:child_process";
import type { ChildProcess } from "node:child_process";
import type { Readable } from "node:stream";
import { z } from "zod";
import { OMP_HOME, OMP_INPUT_FILES, THINKING_LEVELS } from "../../contract.ts";

// ---------------------------------------------------------------------------- the session

/**
 * WHAT A RUN WAS ASKED TO BE, as the machine half receives it in the launch document: the model
 * reference omp routes by, the thinking level, and the account the owner will spend.
 *
 * It replaces `ProfileRefSchema`. A profile was a name Code resolved three things behind; these
 * ARE the three things, and the two that identify the account are the non-secret halves of the
 * pool the owner already holds — the job never sees `scope` or `credentialId`, which are the
 * gateway's business, so they are not here.
 */
export const SessionRefSchema = z.strictObject({
  model: z.string().min(1).max(256),
  thinking: z.enum(THINKING_LEVELS).optional(),
  account: z.strictObject({
    provider: z.string().min(1).max(128),
    identityKey: z.string().max(1024).default(""),
  }),
});
export type SessionRef = z.infer<typeof SessionRefSchema>;

// ---------------------------------------------------------------------------- the boundary

/**
 * The sandbox this process is standing in, read from the job's own surroundings.
 *
 * Every field is an OBSERVATION and not a claim, which is the difference from the sidecar this
 * replaces. `backend` is `manifold-job` because that is what a Manifold machine owner built;
 * `escape` names the assumption a reviewer of the evidence needs — that the boundary is the
 * owner's bubblewrap sandbox and its escape story is the owner's, not Babel's.
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
 *
 * `networkDefaultDeny` is FALSE here and is the one honest difference from the Go worker's
 * demand. `explore` and `evaluate` declare `network: "host"` in the manifest, because the job
 * must reach the owner's loopback proxy — so demanding a default-deny network would be demanding
 * the opposite of what the operation is declared to need, and a requirement nothing can satisfy
 * is not a boundary, it is a run that never starts. What replaces it is stronger and is checked
 * separately: the job holds no credential with which to reach anything but that proxy
 * ({@link inferenceShortfall}).
 */
export const SANDBOXED_RUN: Requirement = {
  filesystemIsolation: true,
  networkDefaultDeny: false,
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

/** The backend a Manifold job's own boundary is, and the assumption a reader of its evidence needs. */
export const JOB_BACKEND = "manifold-job";
const JOB_ESCAPE =
  "the machine owner's job sandbox: a private tmpfs home, a delegated cgroup and a read-only " +
  "root, established by the owner and not by this run";

/**
 * WHAT THE JOB'S SURROUNDINGS ARE, read rather than declared.
 *
 * A Manifold job sandbox sets `HOME` to a private tmpfs and points every XDG directory inside it
 * (`agent/src/job-linux.ts`), binds the operation's runtime tools read-only under
 * `/runtime/bin`, and runs the workload under a delegated cgroup with the operation's declared
 * ceilings. A process for which those are true is inside one; a process for which they are not
 * is a development shell, and a run that recorded `manifold-job` there would be recording a
 * boundary nobody built.
 */
export function observeContainment(
  home: string,
  environment: Readonly<Record<string, string | undefined>> = process.env,
): Containment {
  // THE HOME MUST BE THE JOB'S OWN, by its absolute path. A check that accepted "HOME with the
  // XDG directories under it" would be satisfied by any ordinary login shell, and a run that
  // recorded `manifold-job` there would be recording a boundary nobody built.
  const inside =
    home === OMP_HOME &&
    environment["HOME"] === home &&
    (environment["XDG_CACHE_HOME"] ?? "").startsWith(`${home}/`) &&
    (environment["XDG_RUNTIME_DIR"] ?? "").startsWith(`${home}/`);
  if (!inside) {
    return {
      backend: "",
      filesystem_isolation: false,
      network_default_deny: false,
      resource_ceilings: false,
      disposable: false,
      escape: "",
    };
  }
  return {
    backend: JOB_BACKEND,
    filesystem_isolation: true,
    // The two metered operations declare `network: "host"` so they can reach the owner's
    // loopback proxy; the boundary is the credential's absence and not the route's.
    network_default_deny: false,
    resource_ceilings: true,
    disposable: true,
    escape: JOB_ESCAPE,
  };
}

/**
 * Names every way the observed boundary falls short of a requirement, or "" when it satisfies
 * it. Every shortfall at once rather than the first, so an operator sees the whole gap in one
 * message instead of fixing them one launch at a time.
 */
export function containmentShortfall(
  containment: Containment | null | undefined,
  requirement: Requirement,
  platform: string = process.platform,
): string {
  const demands =
    requirement.filesystemIsolation ||
    requirement.networkDefaultDeny ||
    requirement.resourceCeilings ||
    requirement.disposable;
  if (!containment || containment.backend.trim() === "") {
    return demands
      ? "this run is not inside a Manifold job sandbox: HOME is not the job's private home"
      : "";
  }
  if (containment.escape.trim() === "") {
    return `no escape assumption is recorded for backend ${JSON.stringify(containment.backend)}`;
  }
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

/**
 * THE REFUSAL FOR A RUN THAT COULD REACH A MODEL SOME OTHER WAY, or "" when it could not.
 *
 * This is what replaces the sidecar's lane string, and it is a fact rather than a report. omp
 * resolves its providers from `~/.omp/agent/models.yml`, and under ADR 0038 that file is written
 * by the machine OWNER out of the job's `atyrode.babel.inference` binding: the `baseUrl` is the
 * owner's loopback proxy and the `apiKey` is a bearer minted for this job alone. So:
 *
 * - if the file is absent the binding was never materialized, which means the operation was
 *   posted without the service or the owner refused it, and there is no metered lane to run on;
 * - if `config.yml` is absent the model and thinking level the operator chose never reached omp,
 *   and the run would answer from whatever omp defaults to, on nobody's stated account;
 * - if the launch environment carries a provider credential variable, omp could answer from THAT
 *   instead, and every call would be unmetered and unattributed — which is precisely the state
 *   the whole lane exists to make impossible.
 *
 * The file is checked for EXISTENCE and never read. Its contents are a bearer this process must
 * not see, and a process that read it in order to check it would be the leak it was checking for.
 */
const PROVIDER_ENV_MARKERS = ["api_key", "apikey", "token", "secret", "credential", "auth"] as const;

export async function inferenceShortfall(
  home: string,
  environment: Readonly<Record<string, string | undefined>> = process.env,
): Promise<string> {
  for (const file of [OMP_INPUT_FILES.models, OMP_INPUT_FILES.config]) {
    const path = file.path.startsWith(OMP_HOME)
      ? `${home}${file.path.slice(OMP_HOME.length)}`
      : file.path;
    if (!(await Bun.file(path).exists())) {
      return (
        `${path} is not in this job's home, so the atyrode.babel.inference binding was never ` +
        `materialized and there is no metered lane to a model`
      );
    }
  }
  const offenders = Object.keys(environment)
    .filter((key) => {
      const lower = key.toLowerCase();
      return PROVIDER_ENV_MARKERS.some((marker) => lower.includes(marker));
    })
    .sort();
  if (offenders.length > 0) {
    return (
      `the launch environment carries ${offenders.join(", ")}, so the engine could reach a ` +
      `model outside the owner's metered proxy`
    );
  }
  return "";
}

// ---------------------------------------------------------------------------- the launch report

/**
 * BABEL'S OWN LAUNCH REPORT — what `code.runtime/1` used to be, written by the process that
 * actually launched something.
 *
 * The schema identifier is Babel's because the document is Babel's: a reader that finds
 * `code.runtime/1` is reading a run from before #279 and should say so rather than decode it.
 * `finished` marks the report rewritten after the engine exits, which is where the exit status
 * and the retry count live — the same split the sidecar had, for the same reason: a reader can
 * tell "this is what was launched" from "this is how it ended".
 */
export const LAUNCH_REPORT_SCHEMA = "babel.launch/1";

export const LaunchReportSchema = z.looseObject({
  schema: z.string(),
  /** The engine this run drove and the version it reported; "" before the handshake. */
  engine: z.object({ name: z.string().min(1), version: z.string().default("") }),
  session: SessionRefSchema,
  containment: ContainmentSchema.nullish(),
  /** The models that actually answered, first heard from first (#261). */
  models: z.array(z.string()).default([]),
  /**
   * A NAMED reason this run produced nothing, or "": `broker_unavailable`, `rate_limited`,
   * `inference_unbound`. #277's whole subject — a handshake EOF whose real cause was one line
   * earlier on stderr and nowhere in the receipt.
   */
  failure: z.string().default(""),
  reason: z.string().default(""),
  /** How many provider retries the engine reported; the bound is {@link MAX_RETRIES}. */
  retries: z.number().int().min(0).default(0),
  /** Present only in the report rewritten after the engine exits; how a reader tells them apart. */
  finished: z.boolean().default(false),
  exit_code: z.number().int().nullish(),
});
export type LaunchReport = z.infer<typeof LaunchReportSchema>;

/** The top-level keys this build reads; anything else is reported rather than silently dropped. */
const KNOWN_REPORT_FIELDS: Record<string, true> = {
  schema: true,
  engine: true,
  session: true,
  containment: true,
  models: true,
  failure: true,
  reason: true,
  retries: true,
  finished: true,
  exit_code: true,
};

/**
 * THE NAMED FAILURES a receipt carries instead of a sentence about a missing frame (#277).
 *
 * On 2026-09-13 every run of an hour failed as "the engine closed its stdout before a ready
 * frame, exit status -1" while the real reason — the account snapshot was unavailable because
 * the auth broker was down — was one line earlier on stderr and nowhere in any receipt. These
 * are the names that sentence is replaced by, and each is decided from something this process
 * observed: the engine's own stderr, the job's own home, or a counted number of retries.
 */
export const LAUNCH_FAILURES = {
  /** The engine could not reach the account broker behind the owner's proxy. */
  brokerUnavailable: "broker_unavailable",
  /** The job holds no materialized inference binding, so there is no metered lane. */
  inferenceUnbound: "inference_unbound",
  /** The provider refused with 429 more times than {@link MAX_RETRIES} allows. */
  rateLimited: "rate_limited",
} as const;
export type LaunchFailureName = (typeof LAUNCH_FAILURES)[keyof typeof LAUNCH_FAILURES];

/**
 * HOW MANY CONSECUTIVE PROVIDER REFUSALS A RUN SURVIVES before it ends `rate_limited` (K-5,
 * relocated to Babel's own code by #279 because this is the only place it was ever consumed).
 *
 * Five, because the failure this bounds is a window that has run out, and a window that has run
 * out does not refill inside one run: on 2026-09-13, 152.7 MB was sent and 2.0 MB received
 * across 21 sockets with no way to tell a retry from an answer. A run that ends by name after
 * five is a run whose receipt an operator can act on.
 */
export const MAX_RETRIES = 5;

/**
 * The stderr lines that name a failure, in the order a reader should prefer them. They are
 * patterns over the engine's own diagnostics because that is where the cause appears: the
 * gateway answers a job `{"error":{"type":"gateway_unavailable"}}` and omp says so on stderr,
 * and a handshake that never happened has nothing else to offer.
 */
const STDERR_FAILURES: readonly { pattern: RegExp; name: LaunchFailureName }[] = [
  { pattern: /gateway_unavailable|account_unavailable|account snapshot is unavailable/i, name: LAUNCH_FAILURES.brokerUnavailable },
  { pattern: /\b429\b|rate.?limit/i, name: LAUNCH_FAILURES.rateLimited },
];

/**
 * The named failure the engine's diagnostics carry, or null. It is read only when a run produced
 * nothing: a run that answered is explained by its answer, and a 429 it recovered from is a
 * retry rather than a cause.
 */
export function diagnoseFailure(stderr: string): { name: LaunchFailureName; reason: string } | null {
  const lines = stderr.split("\n").filter((line) => line.trim() !== "");
  for (const { pattern, name } of STDERR_FAILURES) {
    const line = lines.findLast((candidate) => pattern.test(candidate));
    if (line !== undefined) return { name, reason: line.trim().slice(0, 512) };
  }
  return null;
}

/** Decodes a launch report, validating the schema identifier and the shape it promises. */
export function decodeLaunchReport(text: string): { report: LaunchReport; unknown: string[] } {
  let raw: unknown;
  try {
    raw = JSON.parse(text);
  } catch (error) {
    throw new EngineFailure(ENGINE_FAILURES.launchReport, `the launch report is not JSON: ${String(error)}`);
  }
  const parsed = LaunchReportSchema.safeParse(raw);
  if (!parsed.success) {
    throw new EngineFailure(
      ENGINE_FAILURES.launchReport,
      `the launch report does not decode: ${parsed.error.issues.map((i) => `${i.path.join(".")}: ${i.message}`).join("; ")}`,
    );
  }
  const report = parsed.data;
  if (report.schema !== LAUNCH_REPORT_SCHEMA) {
    throw new EngineFailure(
      ENGINE_FAILURES.launchReport,
      `the launch report declares schema ${JSON.stringify(report.schema)}, this build writes ${LAUNCH_REPORT_SCHEMA}`,
    );
  }
  const unknown = Object.keys(raw as Record<string, unknown>)
    .filter((key) => KNOWN_REPORT_FIELDS[key] !== true)
    .sort();
  return { report, unknown };
}

// ---------------------------------------------------------------------------- failures

/**
 * Why a supervised run ended. They are codes rather than prose because a caller decides
 * differently between an engine that never spoke, one Babel refused, one that stalled, and one
 * that answered and then would not leave; the receipt records the code and the message.
 */
export const ENGINE_FAILURES = {
  handshake: "handshake",
  protocol: "protocol",
  launchReport: "launch-report",
  containment: "containment",
  inference: "inference",
  platform: "platform",
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
  rateLimited: "rate-limited",
  launch: "launch",
} as const;
export type EngineFailureCode = (typeof ENGINE_FAILURES)[keyof typeof ENGINE_FAILURES];

/** One supervised run's failure, carrying the code a receipt records. */
export class EngineFailure extends Error {
  readonly code: EngineFailureCode;
  /** Whether the far side broke the boundary (`engine`) or Babel's own supervision did. */
  readonly origin: "engine" | "babel";
  /** The named cause a receipt carries, when one was diagnosed (#277); "" otherwise. */
  readonly named: string;

  constructor(
    code: EngineFailureCode,
    message: string,
    origin: "engine" | "babel" = "engine",
    named = "",
  ) {
    super(message);
    this.name = "EngineFailure";
    this.code = code;
    this.origin = origin;
    this.named = named;
  }
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
  maxRetries: number;
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
  maxRetries: MAX_RETRIES,
};

// ---------------------------------------------------------------------------- the process

/** How to launch one engine. `args` are the operator's own, placed before Babel's own flags. */
export interface LaunchSpec {
  binary: string;
  args?: readonly string[];
  session: SessionRef;
  /**
   * The job's private home: where the owner materialized `models.yml` and `config.yml`, and what
   * `HOME` is set to for the child. It is a parameter rather than `process.env.HOME` so a test
   * can stand up the same two files in a temporary directory and prove the launch reads them.
   */
  home: string;
  /** Where the run works. A directory the job may write, created by the caller. */
  cwd: string;
  /** Appended to the derived launch environment. It carries no credentials, as argv must not. */
  env?: Readonly<Record<string, string>>;
}

/**
 * THE CHILD'S COMMAND LINE, and a fixed list on purpose.
 *
 * It carries nothing about the session and nothing about a provider: the model, the thinking
 * level and the account are in `config.yml` and `models.yml`, which is where the owner put the
 * bearer too, so argv — visible in any process listing on the host — says only which mode omp
 * runs in and which files to read. `atyrode.omp.launch` composes its own argv exactly this way,
 * and `code engine` did (`omprpc.go` `ompArgv`), which is why the flags below are the same set:
 *
 * - `--mode rpc` is the pipe `client.ts` speaks (protocol 2, host tools, chunked frames).
 * - `--no-tools`, `--no-lsp`, `--no-extensions`, `--no-rules`, `--no-skills` empty the registry
 *   so the only tools in the session are the host tools Babel registers. A private HOME is what
 *   makes that true rather than nominal: `--no-tools` alone leaves whatever the discovered
 *   configuration brings, and a job's home is a tmpfs holding two files.
 * - `--no-session` and `--no-title` keep the run from writing a transcript or asking a model for
 *   a name: the record of a Babel run is its receipt.
 * - `--auto-approve` is safe here and nowhere else — every tool in the registry is one Babel
 *   registered and authorizes per call, and an approval prompt would ask a question no RPC host
 *   in this design can answer.
 */
export const OMP_FLAGS = [
  "--mode",
  "rpc",
  "--no-tools",
  "--no-lsp",
  "--no-session",
  "--no-extensions",
  "--no-rules",
  "--no-skills",
  "--no-title",
  "--auto-approve",
] as const;

export function engineArgv(spec: LaunchSpec): string[] {
  return [
    ...(spec.args ?? []),
    ...OMP_FLAGS,
    "--config",
    `${spec.home}${OMP_INPUT_FILES.config.path.slice(OMP_HOME.length)}`,
    "--cwd",
    spec.cwd,
  ];
}

/**
 * THE CHILD'S ENVIRONMENT: the job's own home and the CA bundle, and nothing else.
 *
 * It is an allow-list and not a filter of `process.env`, because the thing being kept out is a
 * provider credential and a filter is a list of the ones somebody thought of. `HOME` is the
 * job's private home so omp discovers `models.yml` and `config.yml` there and nothing else;
 * `SSL_CERT_FILE` is the bundle the operation's manifest binds (omp's own operations set exactly
 * this variable to exactly this path); `PATH` is what lets omp find a shell for the tools it has
 * none of. No `XDG_*`, because omp would write caches Babel does not want kept, and the job's
 * home is disposable anyway.
 */
const CA_BUNDLE = "/runtime/bin/ca-certificates";

function launchEnv(
  spec: LaunchSpec,
  environment: Readonly<Record<string, string | undefined>> = process.env,
): Record<string, string> {
  const env: Record<string, string> = {
    HOME: spec.home,
    PATH: environment["PATH"] ?? "/runtime/bin",
    LANG: environment["LANG"] ?? "C",
    SSL_CERT_FILE: environment["SSL_CERT_FILE"] ?? CA_BUNDLE,
    TMPDIR: environment["TMPDIR"] ?? `${spec.home}/.run`,
    // The two the boundary is READ from: the owner's sandbox points them inside the private
    // home, and `observeContainment` is what notices when they are somewhere else.
    XDG_CACHE_HOME: `${spec.home}/.cache`,
    XDG_RUNTIME_DIR: `${spec.home}/.run`,
  };
  return Object.assign(env, spec.env ?? {});
}

/** One launched engine and the lifetime Babel owns: nothing it started outlives `killTree`. */
export interface EngineProcess {
  readonly argv: readonly string[];
  readonly env: Readonly<Record<string, string>>;
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

export function launchEngine(spec: LaunchSpec): EngineProcess {
  const argv = engineArgv(spec);
  const env = launchEnv(spec);
  let child: ChildProcess;
  try {
    child = spawn(spec.binary, argv, {
      cwd: spec.cwd,
      env,
      stdio: ["pipe", "pipe", "pipe"],
      // The child leads its own process group, so a kill reaches every process it spawned —
      // omp runs shells and language servers of its own, and killing only the direct child
      // would leave them running.
      detached: true,
    });
  } catch (error) {
    throw new EngineFailure(ENGINE_FAILURES.launch, `could not launch ${spec.binary}: ${String(error)}`, "babel");
  }
  return processHandle(child, argv, env);
}

function processHandle(
  child: ChildProcess,
  argv: readonly string[],
  env: Readonly<Record<string, string>>,
): EngineProcess {
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
    env,
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
