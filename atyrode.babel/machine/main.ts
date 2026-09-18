import {
  MACHINE_OPERATIONS,
  RESTIC_CREDENTIAL_FILE,
  type OperationWord,
  type Receipt,
} from "../contract.ts";
import { type MaterialSink, type OutputSink, directorySink, materialSink } from "./output.ts";
import { openProgress, type ProgressChannel } from "./progress.ts";
import { claim, discover, existingRoots } from "./adapters/index.ts";

/*
  THE MACHINE HALF'S ENTRY POINT (plan §2, §4).

  One binary, three operations, one shape each: read a JSON input document, write the
  JOB_OUTPUT_FILES of what you produced into an output directory, hand the receipt to the sink
  last and return it. This file is the dispatcher and nothing else — it holds no knowledge of
  what an operation does, only where its input and its outputs are and which module owns it.

    babel-machine <operation> --input <file> --out <directory> [--material <directory>]

  WHAT IS NOT HERE: `explore` and `evaluate` (#279). A run that reaches a model is a Code
  session — the operator picks a saved Code profile or parametrizes one in Code's generator,
  and Code's `runSession` door posts the omp job. Babel neither composes a session nor launches
  omp, so the two lanes that did are not operations of this binary. What remains is the catalog:
  what is on the machine, what a run may read, and what is kept.

  `--material` IS `prepare`'S SECOND LEASE, and only `prepare` is given one: the evidence a
  session reads, sealed as its own output so ANOTHER plugin's job can bind it read-only at
  `/inputs/material` (`machine/output.ts` says the whole of it). A hand-run that passes none
  prepares a selection and seals no evidence, which its receipt says.

  A job supplies both paths through its bindings; the environment variables are the same two
  values for a hand-run on a machine, as `BABEL_RESTIC_BINDING` is for the one operation that
  also reads a materialized service binding. The operation name is argv's first non-flag word,
  which is what makes the file work identically under `bun machine/main.ts scan …` (where
  argv[1] is this script) and as a compiled binary (where argv[1] is already the operation).

  Every operation module is imported on demand. That is not laziness: three operations mean
  three dependency trees — restic, the digesters, the store's shapes — and a scan that ran on a
  schedule should not pay for the two it is not.

  A crash still leaves a receipt. An operation that throws has its failure written as
  closure:"failed" with the reason, because the hub learns what a run did from the receipt and
  a run that produced no receipt at all is a run nobody can account for.
*/

export interface Invocation {
  operation: OperationWord;
  inputPath: string;
  outputDir: string;
  /** Where the material is sealed; empty for an invocation that bound no such lease. */
  materialDir: string;
}

const USAGE =
  `babel-machine <${Object.keys(MACHINE_OPERATIONS).join("|")}> --input <file> ` +
  `--out <directory> [--material <directory>]`;

/**
 * Each operation parses its own input with its own schema; only the module knows the shape.
 *
 * The modules are loaded dynamically because the operation is selected from argv at runtime
 * and each one pulls in a dependency tree of its own — restic for `archive`, the digesters for
 * `prepare`. A static import graph would make every scheduled scan load all three.
 */
const DISPATCH: Record<
  OperationWord,
  (
    raw: unknown,
    out: OutputSink,
    progress: ProgressChannel,
    material: MaterialSink | null,
  ) => Promise<Receipt>
> = {
  scan: async (raw, out) => {
    const { ScanInputSchema, scan } = await import("./scan.ts");
    return scan(ScanInputSchema.parse(raw), out);
  },
  archive: async (raw, out) => {
    const { ArchiveInputSchema, archive } = await import("./archive.ts");
    return archive(ArchiveInputSchema.parse(raw), out, {
      roots: existingRoots,
      claim,
      // Where the engine put the service binding, or where a hand-run says it is. A path is
      // not a credential: the secret is behind the service, never in this variable.
      credentialFile: process.env["BABEL_RESTIC_BINDING"]?.trim() || RESTIC_CREDENTIAL_FILE,
    });
  },
  prepare: async (raw, out, progress, material) => {
    const { PrepareInputSchema, prepare, digests, modifiedAt } = await import("./prepare.ts");
    return prepare(PrepareInputSchema.parse(raw), out, {
      discover,
      digests,
      modifiedAt,
      material,
      progress,
    });
  },
};

export function parseArgv(argv: readonly string[]): Invocation {
  let operation: OperationWord | null = null;
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
        if (value === undefined || value === "") throw new Error(`${argument} needs a path\n${USAGE}`);
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
    if (!(argument in MACHINE_OPERATIONS)) throw new Error(`unknown operation ${argument}\n${USAGE}`);
    operation = argument as OperationWord;
  }
  if (operation === null) throw new Error(`no operation named\n${USAGE}`);
  if (inputPath === "") throw new Error(`${operation} needs --input <file>\n${USAGE}`);
  if (outputDir === "") throw new Error(`${operation} needs --out <directory>\n${USAGE}`);
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
export async function run(invocation: Invocation, progress: ProgressChannel = openProgress()): Promise<Receipt> {
  const sink = directorySink(invocation.outputDir);
  const material = invocation.materialDir === "" ? null : materialSink(invocation.materialDir);
  const startedAt = new Date().toISOString();
  let raw: unknown = null;
  try {
    raw = await Bun.file(invocation.inputPath).json();
    return await DISPATCH[invocation.operation](raw, sink, progress, material);
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
    process.stdout.write(JSON.stringify(receipt) + "\n");
  } catch (cause) {
    process.stderr.write((cause instanceof Error ? cause.message : String(cause)) + "\n");
    process.exit(1);
  }
}
