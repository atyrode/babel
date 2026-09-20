import { ActionCallError } from "@manifold/plugin-kit/errors";
import {
  CODE_PLUGIN_ID,
  PROMPT_MAX_BYTES,
  actionSchemas,
  type ActionInput,
  type ActionResult,
  type CodeAction,
} from "@atyrode/manifold-code";
import {
  ENGINE_REFUSALS,
  MATERIAL_OUTPUT,
  type CodeProfile,
  type EngineRefusalCode,
  type ProfileRow,
} from "../../contract.ts";

/*
  THE ENGINE, WHICH IS CODE (#279).

  `atyrode.babel` depends on `atyrode.code`, which depends on `atyrode.omp`. Code owns the
  profiles — the model, the thinking level, the account — and Code posts the omp job. This file
  is the whole of Babel's side of that: Code's doors called through `ctx.actions.call` on the
  hardened GuestCtx (ADR 0041, atyrode/manifold#576), and one translation of the refusals that
  come back. Babel launches nothing and composes no session; what it supplies is a profile, a
  destination, a prompt and a binding to the sealed material.

  THE SCHEMAS ARE CODE'S OWN, IMPORTED AND NEVER MIRRORED. `@atyrode/manifold-code` publishes
  `actionSchemas` with an input and a result apiece, which is the same arrangement Code itself
  consumes omp through. A mirror here would be a second statement of another plugin's contract,
  and the day Code changed one the mirror would be the copy nobody checked. So the input is
  parsed with Code's schema before the call and the reply with Code's schema after it: a door
  whose answer does not match its own published result is a fault, not a value to pass on.

  Refusals reach this adapter as host rejections, including Code's `code_…` token in the
  rejection's detail. Hardened and in-realm callers receive different error classes carrying
  the same sentence; successful replies must match Code's published result schema.

  The profile preflight (#255) rejects a positively resolved empty account selection before
  preparation or posting. An unresolved observation is not evidence of absence, and success
  is not spend authority: Code still validates the current profile and accounts when posting.
  Babel receives account references, never the credentials the machine broker resolves.
*/

/**
 * The one verb onto a sibling, narrowed to what this plugin calls. It is a slice for the reason
 * every other slice in this tree is one: a hook whose installer's credential could not be
 * restored is served NO `actions` member at all (`GuestLifecycleCtx.actions?`), and "nobody
 * could be asked" is an answer this file has to be able to give rather than a `TypeError`.
 */
export interface ActionsSlice {
  call(args: { plugin: string; action: string; input: unknown }): Promise<unknown>;
}

/** Every name a refusal from this file carries, which is {@link ENGINE_REFUSALS}'s four. */
export type EngineCode = EngineRefusalCode;

/** What the engine answered, or the named refusal — never an exception across this boundary. */
export type EngineAnswer<T> =
  | { readonly ok: true; readonly value: T }
  | { readonly ok: false; readonly code: EngineCode; readonly refused: string };

/** The Code job a posted session runs as: omp's own one-shot, under omp's plugin id. */
export interface CodeJob {
  readonly jobId: string;
  readonly machineId: string;
  readonly operationId: string;
  readonly pluginId: string;
  readonly state: string;
}

/** What one session's transcript yielded, as omp's receipt records it. */
export interface SessionUsage {
  readonly input: number;
  readonly output: number;
  readonly cacheRead: number;
  readonly cacheWrite: number;
  readonly cost?: number | undefined;
}

export interface SessionReceipt {
  readonly sessionId: string;
  readonly sessionPath: string;
  readonly model: string;
  readonly finalMessage: string;
  readonly usage: SessionUsage | null;
  readonly exitCode: number;
}
/**
 * WHAT `readSession` ANSWERED: where the job is, and the receipt its transcript yielded.
 *
 * `job` is ALWAYS there for a job Code's door posted, whatever state it is in; `session` is
 * there only when that job exited 0 and its transcript was sealed. So a RUNNING, cancelled,
 * interrupted or non-zero-exit job is a successful read with a null receipt, and the consumer
 * decides from `job.state` and `job.result.exitCode` — a refusal is reserved for a job Code
 * never posted. The distinction is the whole reason a live session no longer reads as a fault.
 */
export interface SessionRead {
  readonly job: CodeJob;
  readonly session: SessionReceipt | null;
}

/** One sealed output bound into a session's sandbox, as the job request carries it. */
export interface MaterialBinding {
  readonly name: string;
  readonly from: { readonly jobId: string; readonly output: string };
}

/**
 * THE MATERIAL AS ONE JOB INPUT (ADR 0044, atyrode/manifold#592).
 *
 * A run's evidence is a sealed output of Babel's OWN `prepare` job, bound read-only into the
 * session's sandbox at `/inputs/material`. Babel holds no tools in the session — a Code
 * session is omp's job with omp's own tools — so this is how evidence is served: the way a
 * job serves any input, and the model reads it with the tools it already has.
 *
 * THE BINDING NAMES A SETTLED JOB, which is why nothing calls this at the press. The hub
 * refuses a binding whose source is still active, and `prepare` is running the moment it is
 * posted; `launchMachinery.postPrepared` is the wake that reaches here, after that
 * preparation has settled and written the index the prompt was composed from.
 *
 * IT IS A CROSS-PLUGIN BINDING, and that is what `exports: ["material"]` on
 * `atyrode.babel.prepare` is for: a same-plugin binding needs no export, but Code's job is
 * `atyrode.omp`'s and admission refuses `input_not_exported:material` without the
 * declaration. The two halves are one statement made twice, here and in `manifest.json`, and
 * `test/contract.test.ts` refuses a tree where the operation exports less than it outputs.
 */
export function materialInput(prepareJobId: string): { readonly inputs: MaterialBinding[] } {
  return {
    inputs: [{ name: MATERIAL_OUTPUT, from: { jobId: prepareJobId, output: MATERIAL_OUTPUT } }],
  };
}

/**
 * CODE'S OWN BOUND ON ONE SESSION'S PROMPT, IN BYTES, imported rather than mirrored — a
 * number copied here would be the thing nobody updated the day it moved.
 *
 * It is BYTES and not characters because the real ceiling is the hub's 64 KiB job-input map,
 * which counts encoded bytes: a prompt of legal length whose selectors and digests are
 * multi-byte would pass a character check and be refused at admission. `PROMPT_MAX_BYTES` is
 * omp's own constant, re-exported by Code, and `SessionRunInputSchema.prompt` is omp's schema
 * by import — so there is one number and this reads it.
 *
 * `postPrepared` measures against it and refuses by name. Babel's composed prompt fits today
 * with room to spare; the guard stays because a longer contract, a bigger selection or a
 * corpus of non-ASCII selectors is how it would stop fitting, and a run that discovered that
 * inside Code's parse would report a Zod issue instead of the two figures.
 */
export const PROMPT_LIMIT: number = PROMPT_MAX_BYTES;

/** What the bound is measured over: the encoded bytes the hub's input map will hold. */
export function promptBytes(prompt: string): number {
  return new TextEncoder().encode(prompt).byteLength;
}

/** What a session is posted with. Explorations bind prepared material; reviews need only the
 * immutable record already carried in their prompt and therefore omit the binding entirely. */
export interface SessionRequest {
  readonly profile: CodeProfile;
  readonly machineId: string;
  readonly prompt: string;
  /** The `prepare` job whose sealed `material` output this run reads, when one is needed. */
  readonly prepareJobId?: string | undefined;
}

export interface CodeEngine {
  /** Every saved Code profile, as Watch's Start section offers them. */
  profiles(): Promise<EngineAnswer<readonly ProfileRow[]>>;
  /** One session, posted by Code as an omp job. */
  runSession(request: SessionRequest): Promise<EngineAnswer<CodeJob>>;
  /** Where a posted session is, and what its transcript yielded. */
  readSession(args: { containerId: string; jobId: string }): Promise<EngineAnswer<SessionRead>>;
  /**
   * STOP ONE POSTED SESSION. It is Code's to cancel and not Babel's: the job belongs to
   * `atyrode.omp` and `ctx.jobs.cancel` is bound to the calling plugin's id, so an operator's
   * Stop, a `drain.stop` and a drain's own deadline all reach it through this door. It is
   * idempotent on a settled job — the answer is the job — and refuses only a job Code never
   * posted, so a stop that raced a settlement is not an error to report.
   */
  cancelSession(args: { containerId: string; jobId: string }): Promise<EngineAnswer<CodeJob>>;
  /**
   * Reject a stale profile or a positively resolved empty account selection before preparation.
   * Unresolved observations pass through; Code remains authoritative when posting the session.
   */
  checkProfile(profile: CodeProfile): Promise<EngineAnswer<null>>;
}

/**
 * WHAT A CALLER WITH NO `actions` SLICE IS TOLD, in the truthful shape rather than a pessimistic
 * one: nobody was asked. It is `engine_unavailable` because the operator's remedy is the same as
 * for a Code that is not installed — this row's installer credential is gone, and the install is
 * what restores it.
 */
export const ENGINE_WITHOUT_ACTIONS =
  "this hook is served no actions slice: GuestLifecycleCtx carries it only while the " +
  "installer's credential can be restored, and this one's could not";

/** The host's own refusal classes, folded onto the four names an operator acts on. */
const HOST_CLASSES: Readonly<Record<string, EngineRefusalCode>> = {
  // There is no Code to ask: Babel's manifest does not declare the edge, the roster has no
  // enabled row for it, or the Code that is installed publishes no such door.
  undeclared_dependency: ENGINE_REFUSALS.unavailable,
  dependency_unavailable: ENGINE_REFUSALS.unavailable,
  unknown_action: ENGINE_REFUSALS.unavailable,
  // Authority: what the principal this dispatch serves holds, or Babel's own installed ceiling.
  capability: ENGINE_REFUSALS.forbidden,
  caller_ceiling: ENGINE_REFUSALS.forbidden,
  // Code answered no, or the composition graph itself is wrong. Neither is something an
  // operator installs his way out of, and both carry the sentence that says which.
  refused: ENGINE_REFUSALS.refused,
  dispatch_cycle: ENGINE_REFUSALS.refused,
  dispatch_depth: ENGINE_REFUSALS.refused,
};

/**
 * CODE'S OWN WORDS THAT BABEL ACTS ON. Exactly one of them is not `engine_refused`: a profile
 * that moved between the read and the press is what the panel re-reads and presses again for,
 * and folding it into the generic refusal would send an operator looking for a fault that is a
 * stale list. Every other `code_…` — a missing catalog, an unavailable account, omp's own word
 * carried through as `code_omp_…` — is Code saying no, and the word rides the detail.
 */
const CODE_TOKENS: Readonly<Record<string, EngineRefusalCode>> = {
  code_stale_preferences: ENGINE_REFUSALS.staleProfile,
};

/** `<class>: <offenders>`, which is the message BOTH boundaries carry (ADR 0041). */
const REFUSAL_SENTENCE = /^([a-z_]+): ([\s\S]*)$/;
/** Code's own token, wherever the host's detail carried it through. */
const CODE_TOKEN = /\bcode_[a-z0-9_]+\b/;

/**
 * Babel's side of Code's doors.
 *
 * Every call is one shape: parse the input with Code's schema, ask, and parse the reply with
 * Code's schema. A refusal is a REJECTION the host raised, folded by {@link translate}; a
 * slice that is absent refuses every call by name rather than being asked.
 */
export function codeEngine(actions: ActionsSlice | undefined): CodeEngine {
  /** One refusal of this file's own, in the sentence every caller reports verbatim. */
  function refuse<T>(code: EngineCode, detail: string): EngineAnswer<T> {
    return { ok: false, code, refused: `${code}: ${detail}` };
  }

  /**
   * One thrown thing, as this plugin's refusal. The class is read off the SENTENCE because the
   * two boundaries raise two classes carrying one message, and because a bundle must not import
   * the engine's own package to recognise its errors — but only for something that IS one of
   * those classes. A `TypeError` out of this plugin's own code is reported as itself rather than
   * folded into a vocabulary it does not belong to.
   */
  function translate<T>(door: string, error: unknown): EngineAnswer<T> {
    const text = error instanceof Error ? error.message : String(error);
    const isRefusal =
      error instanceof ActionCallError ||
      (error instanceof Error && error.name === "ActionCallRefused");
    const matched = isRefusal ? REFUSAL_SENTENCE.exec(text) : null;
    const host = matched === null ? undefined : HOST_CLASSES[matched[1] ?? ""];
    if (matched !== null && host !== undefined) {
      // Code's own refusal token travels inside the host rejection's detail.
      const detail = matched[2] ?? "";
      const token = CODE_TOKEN.exec(detail)?.[0] ?? "";
      return refuse(CODE_TOKENS[token] ?? host, detail);
    }
    return refuse(
      ENGINE_REFUSALS.refused,
      `${CODE_PLUGIN_ID}.${door} raised something that is not a refusal: ${text}`,
    );
  }

  async function call<K extends CodeAction>(
    action: K,
    input: ActionInput<K>,
  ): Promise<EngineAnswer<ActionResult<K>>> {
    if (actions === undefined) {
      return refuse(ENGINE_REFUSALS.unavailable, ENGINE_WITHOUT_ACTIONS);
    }
    const schemas = actionSchemas[action];
    const args = schemas.input.safeParse(input);
    if (!args.success) {
      // Babel built a request Code's own schema refuses. That is this plugin's bug and is
      // reported as one rather than as Code saying no.
      return refuse(
        ENGINE_REFUSALS.refused,
        `${CODE_PLUGIN_ID}.${action} was asked for something it does not take: ` +
          args.error.issues.map((issue) => issue.message).join("; "),
      );
    }
    let reply: unknown;
    try {
      reply = await actions.call({ plugin: CODE_PLUGIN_ID, action, input: args.data });
    } catch (error) {
      return translate(action, error);
    }
    /*
      THERE IS NO RESOLVED REFUSAL. A Code refusal reaches this plugin as a REJECTION raised
      by the host, whose sentence names the class and then Code's own `code_…` word inside its
      detail ({@link translate}); `{ refused: "code_…" }` is what Code's ORDINARY-CLIENT
      adapter answers a session dispatch with, not what an in-process `ctx.actions.call`
      resolves. Checking for it here made a second road that never carried anything and two
      tests that proved a shape nobody produces.
    */
    const parsed = schemas.result.safeParse(reply);
    if (!parsed.success) {
      return refuse(
        ENGINE_REFUSALS.refused,
        `${CODE_PLUGIN_ID}.${action} answered outside its own published result`,
      );
    }
    return { ok: true, value: parsed.data as ActionResult<K> };
  }

  // Cache only within this adapter instance, not across wakes. Code revalidates on posting.
  let roster: Promise<EngineAnswer<ActionResult<"listProfiles">>> | undefined;

  /** Every saved profile, which is both what the door offers and what the authority read is. */
  async function readProfiles(): Promise<EngineAnswer<readonly ProfileRow[]>> {
    const answered = await (roster ??= call("listProfiles", {}));
    if (!answered.ok) return answered;
    return {
      ok: true,
      // A profile whose saved selection no longer reviews against its catalog answers
      // `selected: null` — a profile to open in Code, which the panel says rather than hides.
      //
      // `accounts` AND `resolved` ARE CODE'S FACTS AND BABEL HAS NO OTHER SOURCE FOR THEM:
      // the accounts belong to the profile, Code resolves the container's saved choices
      // against the live observation, and this plugin has no broker to ask. Code's choices
      // are stored as EXCLUSIONS, so without an observation there is no list to give —
      // `resolved: false` with an empty list means ASK AGAIN and never "spends nothing",
      // and every reader of these rows says which of the two it is looking at.
      value: answered.value.profiles.map((profile) => ({
        containerId: profile.containerId,
        revision: profile.revision,
        model: profile.selected?.model ?? "",
        thinking: profile.selected?.thinking ?? "",
        lastMachineId: profile.machineId ?? "",
        // `identityKey` is null for an API-key slot, which has a credential and no login.
        accounts: profile.accounts.map((account) => ({
          provider: account.provider,
          identityKey: account.identityKey ?? "",
          label: account.label ?? "",
        })),
        resolved: profile.resolved,
      })),
    };
  }

  async function checkProfile(profile: CodeProfile): Promise<EngineAnswer<null>> {
    roster ??= call("listProfiles", {});
    const listed = await roster;
    if (!listed.ok) return listed;
    const held = listed.value.profiles.find((row) => row.containerId === profile.containerId);
    if (held === undefined) {
      return refuse(
        ENGINE_REFUSALS.staleProfile,
        `the Code profile ${profile.containerId} is absent from this caller's readable, ` +
          `configured profiles. It may have been removed, need configuration in Code, or be ` +
          `outside the caller's access. Check the workspace and access, then re-read the profiles.`,
      );
    }
    if (held.revision !== profile.expectedRevision) {
      return refuse(
        ENGINE_REFUSALS.staleProfile,
        `the Code profile ${profile.containerId} moved from revision ` +
          `${String(profile.expectedRevision)} to ${String(held.revision)}. ` +
          `Re-read the profiles and start it again.`,
      );
    }
    // Account choices are exclusions: without a live observation, empty means unknown.
    if (held.resolved && held.accounts.length === 0) {
      return refuse(
        ENGINE_REFUSALS.noAccount,
        `the Code profile ${profile.containerId} spends no account: Code resolved its saved ` +
          `choices against the live observation and found none. Open that workspace in Code ` +
          `and choose an account. Babel holds no provider credential of its own.`,
      );
    }
    return { ok: true, value: null };
  }

  return {
    profiles: readProfiles,

    checkProfile,

    // Guard every posting path, including conductor reviews and prepared explorations.
    runSession: async (request: SessionRequest): Promise<EngineAnswer<CodeJob>> => {
      const may = await checkProfile(request.profile);
      if (!may.ok) return may;
      return await call("runSession", {
        containerId: request.profile.containerId,
        machineId: request.machineId,
        expectedRevision: request.profile.expectedRevision,
        prompt: request.prompt,
        ...(request.prepareJobId === undefined ? {} : materialInput(request.prepareJobId)),
      });
    },

    readSession: async (args: {
      containerId: string;
      jobId: string;
    }): Promise<EngineAnswer<SessionRead>> => await call("readSession", args),

    cancelSession: async (args: {
      containerId: string;
      jobId: string;
    }): Promise<EngineAnswer<CodeJob>> =>
      (await call("cancelSession", args)) as EngineAnswer<CodeJob>,
  };
}
