import { tmpdir } from "node:os";
import {
  CatalogInputSchema,
  MACHINE_OPERATIONS,
  PrepareInputSchema,
  RESTIC_CREDENTIAL_FILE,
  TranscriptMapCatalogJobInputSchema,
  TranscriptMapPrepareInputSchema,
  type OperationWord,
  type Receipt,
} from "../contract.ts";
import {
  type MaterialSink,
  type OutputSink,
  directorySink,
  materialSink,
  outputCapacity,
} from "./output.ts";
import { openProgress, type ProgressChannel } from "./progress.ts";
import { claim, existingRoots } from "./adapters/index.ts";
import type { ResticConfig } from "./restic.ts";
import {
  mapCatalog,
  mapCatalogWake,
  mapPrepare,
  openTranscriptMapClient,
} from "./transcript-map-jobs.ts";

/*
  THE MACHINE HALF'S ENTRY POINT (plan §2, §4).

  Finite operations read one JSON document, write JOB_OUTPUT_FILES and return a receipt.
  Recall is the owner-managed native service: it adopts the SDK channel, announces readiness
  and serves bounded requests until its owner stops it. It has no output lease or run receipt.
  This dispatcher holds no operation-specific retrieval or disclosure logic.

    babel-machine <operation> --input <file> --out <directory> [--material <directory>]

  WHAT IS NOT HERE: `explore` and `evaluate` (#279). A run that reaches a model is a Code
  session — the operator picks a saved Code profile or parametrizes one in Code's generator,
  and Code's `runSession` door posts the omp job. Babel neither composes a session nor launches
  omp, so the two lanes that did are not operations of this binary. What remains is the
  archive: what it holds, what a run may read out of it, and what is kept in it.

  `--material` IS `prepare`'S SECOND LEASE, and only `prepare` is given one: the evidence a
  session reads, sealed as its own output so ANOTHER plugin's job can bind it read-only at
  `/inputs/material` (`machine/output.ts` says the whole of it). A hand-run that passes none
  prepares a selection and seals no evidence, which its receipt says.

  A job supplies both paths through its bindings; the environment variables are the same two
  values for a hand-run on a machine, as `BABEL_RESTIC_BINDING` is for the operations that also
  read a materialized service binding. The operation name is argv's first non-flag word,
  which is what makes the file work identically under `bun machine/main.ts catalog …` (where
  argv[1] is this script) and as a compiled binary (where argv[1] is already the operation).

  Every operation module is imported on demand. That is not laziness: the operations have
  dependency trees of their own — the listing, the digesters, the store's shapes — and a catalog
  that runs on a schedule should not pay for the ones it is not.

  A crash still leaves a receipt. An operation that throws has its failure written as
  closure:"failed" with the reason, because the hub learns what a run did from the receipt and
  a run that produced no receipt at all is a run nobody can account for.
*/

export interface Invocation {
  operation: keyof typeof MACHINE_OPERATIONS;
  inputPath: string;
  outputDir: string;
  /** Where the material is sealed; empty for an invocation that bound no such lease. */
  materialDir: string;
}

const USAGE =
  `babel-machine <${Object.keys(MACHINE_OPERATIONS).join("|")}> --input <file> ` +
  `--out <directory> [--material <directory>]`;

/**
 * Where the engine put the restic service binding, or where a hand-run says it is. A path is
 * not a credential: the secret is behind the service, never in this variable.
 */
function resticBinding(): string {
  return process.env["BABEL_RESTIC_BINDING"]?.trim() || RESTIC_CREDENTIAL_FILE;
}

/**
 * Each operation parses its own input with its own schema; only the module knows the shape.
 *
 * The modules are loaded dynamically because the operation is selected from argv at runtime
 * and each one pulls in a dependency tree of its own — the listing for `catalog`, restic's
 * writes for `archive`, the digesters for `prepare`. A static import graph would make every
 * scheduled catalog load all of them.
 */
const DISPATCH: Record<
  OperationWord,
  (
    raw: unknown,
    out: OutputSink,
    progress: ProgressChannel,
    material: MaterialSink | null,
    invocation: Invocation,
  ) => Promise<Receipt>
> = {
  mapCatalog: async (raw, out) => {
    try {
      const input = TranscriptMapCatalogJobInputSchema.parse(raw);
      if ("kind" in input) return mapCatalogWake(input);
      return await mapCatalog(input, out, await openTranscriptMapClient());
    } catch {
      throw new Error("Mapping catalog could not be collected.");
    }
  },
  mapPrepare: async (raw, out, _progress, material) => {
    try {
      if (!material) throw new Error("Missing material lease.");
      return await mapPrepare(
        TranscriptMapPrepareInputSchema.parse(raw),
        out,
        material,
        await openTranscriptMapClient(),
      );
    } catch {
      throw new Error("Mapping material could not be sealed.");
    }
  },
  catalog: async (raw, out, _progress, _material, invocation) => {
    const { CATALOG_ENV, catalog } = await import("./catalog.ts");
    const { openRepo, resticConfig } = await import("./restic.ts");
    return catalog(CatalogInputSchema.parse(raw), out, {
      archive: async () =>
        openRepo(await resticConfig({ credentialFile: resticBinding(), env: process.env })),
      // Inside a job this is a declared, managed, writable location; outside one — a hand-run,
      // the tests — nothing is remembered and every snapshot is listed on every run.
      cacheDir: process.env[CATALOG_ENV.cacheDir]?.trim() ?? "",
      capacity: () => outputCapacity(invocation.outputDir),
    });
  },
  archive: async (raw, out) => {
    const { ArchiveInputSchema, archive } = await import("./archive.ts");
    return archive(ArchiveInputSchema.parse(raw), out, {
      roots: existingRoots,
      claim,
      credentialFile: resticBinding(),
    });
  },
  verify: async (raw, out) => {
    const { VERIFY_ENV, VerifyInputSchema, verify } = await import("./verify.ts");
    return verify(VerifyInputSchema.parse(raw), out, {
      claim,
      credentialFile: resticBinding(),
      // Inside a job this is a declared, managed, writable location; outside one — a hand-run,
      // the tests — the system's own temporary directory is the honest default.
      scratchDir: process.env[VERIFY_ENV.scratchDir]?.trim() || tmpdir(),
    });
  },
  prepare: async (raw, out, progress, material, invocation) => {
    const { PREPARE_ENV, prepare } = await import("./prepare.ts");
    const { openRepo, resticConfig } = await import("./restic.ts");
    // ONE STORAGE DOCUMENT PER JOB: its locator files the kept readings, and the same document
    // opens the archive when a capture has to be fetched. It is asked for once, and only when
    // the preparation needs either.
    let config: Promise<ResticConfig> | null = null;
    const configured = (): Promise<ResticConfig> =>
      (config ??= resticConfig({ credentialFile: resticBinding(), env: process.env }));
    return prepare(PrepareInputSchema.parse(raw), out, {
      archive: async () => openRepo(await configured()),
      repository: async () => (await configured()).repository,
      // Every named output of a job is cut from one device, so either lease measures it; the
      // material's is the one the preparation fills.
      capacity: () => outputCapacity(invocation.materialDir || invocation.outputDir),
      // Inside a job this is a declared, managed, writable location every `prepare` on this
      // machine shares, which is what lets the second preparation over the same captures skip
      // the archive (#236); outside one — a hand-run, the tests — nothing is kept.
      cacheDir: process.env[PREPARE_ENV.cacheDir]?.trim() ?? "",
      material,
      progress,
    });
  },
};

export function parseArgv(argv: readonly string[]): Invocation {
  let operation: keyof typeof MACHINE_OPERATIONS | null = null;
  let inputPath = process.env["BABEL_JOB_INPUT"]?.trim() ?? "";
  let outputDir = process.env["BABEL_JOB_OUTPUT_DIR"]?.trim() ?? "";
  let materialDir = process.env["BABEL_JOB_MATERIAL_DIR"]?.trim() ?? "";
  for (let i = 0; i < argv.length; i++) {
    const argument = argv[i] ?? "";
    switch (argument) {
      case "--input":
      case "--out":
      case "--material": {
        const value = argv[i + 1];
        if (value === undefined || value === "")
          throw new Error(`${argument} needs a path\n${USAGE}`);
        if (argument === "--input") inputPath = value;
        else if (argument === "--out") outputDir = value;
        else materialDir = value;
        i++;
        continue;
      }
      default:
        break;
    }
    if (argument.startsWith("-")) throw new Error(`unknown flag ${argument}\n${USAGE}`);
    if (operation !== null) throw new Error(`unexpected argument ${argument}\n${USAGE}`);
    if (!Object.hasOwn(MACHINE_OPERATIONS, argument))
      throw new Error(`unknown operation ${argument}\n${USAGE}`);
    operation = argument as keyof typeof MACHINE_OPERATIONS;
  }
  if (operation === null) throw new Error(`no operation named\n${USAGE}`);
  if (inputPath === "") throw new Error(`${operation} needs --input <file>\n${USAGE}`);
  if (operation !== "recall" && outputDir === "")
    throw new Error(`${operation} needs --out <directory>\n${USAGE}`);
  return { operation, inputPath, outputDir, materialDir };
}

/**
 * Runs one operation and returns its receipt, or writes a failed one and rethrows. Reading the
 * input is inside the attempt on purpose: a job whose input document is missing or malformed
 * is a run that failed, and it must leave the same receipt as one that failed later.
 *
 * The progress channel is opened here and handed down: it is the JOB's, one per process, and an
 * operation that opened its own would be a second writer on a descriptor the owner made for one
 * (`machine/progress.ts`). A caller that hands one — a test, a hand-run — keeps it.
 */
export async function run(
  invocation: Invocation,
  progress: ProgressChannel = openProgress(),
): Promise<Receipt | void> {
  if (invocation.operation === "recall") {
    try {
      const { runRecallService } = await import("./recall-service.ts");
      await runRecallService(await Bun.file(invocation.inputPath).json());
    } catch {
      // Service configuration and native bindings are private, including parser diagnostics.
      throw new Error("Recall service could not start or lost its native owner.");
    }
    return;
  }
  const sink = directorySink(invocation.outputDir);
  const material = invocation.materialDir === "" ? null : materialSink(invocation.materialDir);
  const startedAt = new Date().toISOString();
  let raw: unknown = null;
  try {
    raw = await Bun.file(invocation.inputPath).json();
    return await DISPATCH[invocation.operation](raw, sink, progress, material, invocation);
  } catch (cause) {
    const fields = typeof raw === "object" && raw !== null ? (raw as Record<string, unknown>) : {};
    const named = typeof fields["runId"] === "string" ? fields["runId"].trim() : "";
    await sink.receipt({
      // A failed run still has to be one run in the ledger. The input's id when it carried
      // one, a minted id otherwise: the hub correlates the job to the receipt's own id, and
      // nothing this process can see names the job.
      runId: named !== "" ? named : "run_" + crypto.randomUUID().replaceAll("-", ""),
      kind: invocation.operation,
      machineId: typeof fields["machineId"] === "string" ? fields["machineId"] : "",
      startedAt,
      finishedAt: new Date().toISOString(),
      closure: "failed",
      reason: cause instanceof Error ? cause.message : String(cause),
      counts: {},
    });
    throw cause;
  }
}

if (import.meta.main) {
  const argv = process.argv[1] === import.meta.path ? process.argv.slice(2) : process.argv.slice(1);
  try {
    const receipt = await run(parseArgv(argv));
    if (receipt !== undefined) process.stdout.write(JSON.stringify(receipt) + "\n");
  } catch (cause) {
    process.stderr.write((cause instanceof Error ? cause.message : String(cause)) + "\n");
    process.exit(1);
  }
}
