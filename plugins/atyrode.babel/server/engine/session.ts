import { ActionCallError } from "@manifold/plugin-kit/errors";
import {
  CODE_PLUGIN_ID,
  RefusalSchema,
  actionSchemas,
  type ActionInput,
  type ActionResult,
  type CodeAction,
} from "@atyrode/manifold-code";
import {
  ENGINE_REFUSALS,
  MATERIAL_INPUT_PENDING,
  MATERIAL_INPUT_PENDING_CODE,
  type CodeProfile,
  type EngineRefusalCode,
  type ProfileRow,
} from "../../contract.ts";

/*
  THE ENGINE, WHICH IS CODE (#279).

  `atyrode.babel` depends on `atyrode.code`, which depends on `atyrode.omp`. Code owns the
  profiles — the model, the thinking level, the account — and Code posts the omp job. This file
  is the whole of Babel's side of that: three doors called through `ctx.actions.call` on the
  hardened GuestCtx (ADR 0041, atyrode/manifold#576), and one translation of the refusals that
  come back. Babel launches nothing and composes no session; what it supplies is a profile, a
  destination, a prompt and — once Manifold can bind one — the material.

  THE SCHEMAS ARE CODE'S OWN, IMPORTED AND NEVER MIRRORED. `@atyrode/manifold-code` publishes
  `actionSchemas` with an input and a result apiece, which is the same arrangement Code itself
  consumes omp through. A mirror here would be a second statement of another plugin's contract,
  and the day Code changed one the mirror would be the copy nobody checked. So the input is
  parsed with Code's schema before the call and the reply with Code's schema after it: a door
  whose answer does not match its own published result is a fault, not a value to pass on.

  REFUSALS ARRIVE BY TWO ROADS AND THIS IS THE ONE PLACE THAT KNOWS IT.

  - THE HOST refuses the EDGE, as a REJECTION whose message is the class and then the plugins it
    names, caller first: `undeclared_dependency: atyrode.babel -> atyrode.code`, or
    `refused: atyrode.babel -> atyrode.code.runSession (code_catalog_missing)`. A hardened row
    catches {@link ActionCallError} and an in-realm row the engine's own `ActionCallRefused`, and
    the kit's contract is that both carry the SAME sentence — so the class is read off the
    message and one function answers both boundaries.
  - CODE refuses the REQUEST, and that is a RESOLVED value: `{ refused: "code_…" }`, published on
    `RefusalSchema`, the way an omp refusal reaches Code as `omp_…`. So the reply is checked for a
    refusal BEFORE it is parsed as a result, exactly as Code's own client does it.

  Both are folded onto {@link ENGINE_REFUSALS}'s four names, because those are the four things an
  operator does differently: install or upgrade Code, consent to what its door demands, re-read a
  profile that moved, or read the word Code said no with.
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

/** Every name a refusal from this file carries: the four engine ones, and the missing primitive. */
export type EngineCode = EngineRefusalCode | typeof MATERIAL_INPUT_PENDING_CODE;

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

/** What `readSession` answered: where the job is, and the receipt its transcript yielded. */
export interface SessionRead {
  readonly job: CodeJob;
  readonly session: SessionReceipt;
}

/** One sealed output bound into a session's sandbox, as a job request will carry it. */
export interface MaterialBinding {
  readonly name: string;
  readonly from: { readonly jobId: string; readonly output: string };
}

/** The material as a request carries it, or the sentence saying it cannot be carried yet. */
export type MaterialInput =
  | { readonly inputs: readonly MaterialBinding[] }
  | { readonly refused: string };

/**
 * THE MATERIAL AS ONE JOB INPUT — and THE ONE PLACE IN THIS TREE THAT MOVES when Manifold's
 * job-inputs primitive lands.
 *
 * What it becomes, in one statement:
 *
 *     return { inputs: [{ name: "material",
 *                         from: { jobId: prepareJobId, output: "material" } }] };
 *
 * …and {@link codeEngine}'s `runSession` already spreads it into the request. TWO lines change in
 * the whole repository: this one, and `exports: ["material"]` beside `outputs` on
 * `atyrode.babel.prepare` in `manifest.json` — the declaration that lets ANOTHER plugin's job
 * bind Babel's sealed output at all (a same-plugin binding needs no export; Code's job is not
 * Babel's). Code's own `runSession` gains the matching pass-through in its own PR.
 *
 * WHY IT REFUSES RATHER THAN POSTING WITHOUT IT. The prompt tells the model to read
 * `/inputs/material`; a session posted with no binding would find nothing there and answer out
 * of the prompt alone, and Babel would record that answer as evidence-backed analysis. A refusal
 * that names the missing primitive costs an operator a sentence; the alternative costs him the
 * corpus. `SessionRunInputSchema` has no `inputs` key at this pin either, so the field cannot be
 * smuggled past Code's own parse: the refusal is the honest shape of that, not a placeholder.
 */
export function materialInput(prepareJobId: string): MaterialInput {
  return {
    refused: `${MATERIAL_INPUT_PENDING} The material is prepare job ${prepareJobId}.`,
  };
}

/** What a session is posted with: the profile, the destination, the prompt, and the material. */
export interface SessionRequest {
  readonly profile: CodeProfile;
  readonly machineId: string;
  readonly prompt: string;
  /** The `prepare` job whose sealed `material` output this run reads. */
  readonly prepareJobId: string;
}

export interface CodeEngine {
  /** Every saved Code profile, as Watch's Start section offers them. */
  profiles(): Promise<EngineAnswer<readonly ProfileRow[]>>;
  /** One session, posted by Code as an omp job. */
  runSession(request: SessionRequest): Promise<EngineAnswer<CodeJob>>;
  /** Where a posted session is, and what its transcript yielded. */
  readSession(args: { containerId: string; jobId: string }): Promise<EngineAnswer<SessionRead>>;
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
 * Every call is one shape: parse the input with Code's schema, ask, check for Code's own refusal,
 * parse the result with Code's schema. A slice that is absent refuses every call by name rather
 * than being asked.
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
      // A refusal AT the callee carries Code's own word inside the host's detail, so
      // `code_stale_preferences` is read here as well as on the resolved path below.
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
    const refusal = RefusalSchema.safeParse(reply);
    if (refusal.success) {
      const token = refusal.data.refused;
      return refuse(
        CODE_TOKENS[token] ?? ENGINE_REFUSALS.refused,
        `${CODE_PLUGIN_ID}.${action} (${token})`,
      );
    }
    const parsed = schemas.result.safeParse(reply);
    if (!parsed.success) {
      return refuse(
        ENGINE_REFUSALS.refused,
        `${CODE_PLUGIN_ID}.${action} answered outside its own published result`,
      );
    }
    return { ok: true, value: parsed.data as ActionResult<K> };
  }

  return {
    profiles: async (): Promise<EngineAnswer<readonly ProfileRow[]>> => {
      const answered = await call("listProfiles", {});
      if (!answered.ok) return answered;
      return {
        ok: true,
        // A profile whose saved selection no longer reviews against its catalog answers
        // `selected: null` — a profile to open in Code, which the panel says rather than hides.
        value: answered.value.profiles.map((profile) => ({
          containerId: profile.containerId,
          revision: profile.revision,
          model: profile.selected?.model ?? "",
          thinking: profile.selected?.thinking ?? "",
          lastMachineId: profile.machineId ?? "",
        })),
      };
    },

    runSession: async (request: SessionRequest): Promise<EngineAnswer<CodeJob>> => {
      const material = materialInput(request.prepareJobId);
      if ("refused" in material) {
        return { ok: false, code: MATERIAL_INPUT_PENDING_CODE, refused: material.refused };
      }
      return await call("runSession", {
        containerId: request.profile.containerId,
        machineId: request.machineId,
        expectedRevision: request.profile.expectedRevision,
        prompt: request.prompt,
        // The material, bound from `prepare`'s own sealed output; see {@link materialInput}.
        ...material,
      });
    },

    readSession: async (args: {
      containerId: string;
      jobId: string;
    }): Promise<EngineAnswer<SessionRead>> => await call("readSession", args),
  };
}
