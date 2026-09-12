import { OPERATIONS, type OperationWord, type Receipt } from "../contract.ts";
import { type OutputSink, directorySink } from "./output.ts";
import { claim, discover, existingRoots } from "./adapters/index.ts";

/*
  THE MACHINE HALF'S ENTRY POINT (plan §2, §4).

  One binary, five operations, one shape each: read a JSON input document, write the
  JOB_OUTPUT_FILES of what you produced into an output directory, hand the receipt to the sink
  last and return it. This file is the dispatcher and nothing else — it holds no knowledge of
  what an operation does, only where its input and its outputs are and which module owns it.

    babel-machine <operation> --input <file> --out <directory>

  A job supplies both paths through its bindings; the environment variables are the same two
  values for a hand-run on a machine. The operation name is argv's first non-flag word, which
  is what makes the file work identically under `bun machine/main.ts scan …` (where argv[1] is
  this script) and as a compiled binary (where argv[1] is already the operation).

  Every operation module is imported on demand. That is not laziness: five operations mean five
  dependency trees — restic, the engine client, the store's shapes — and a scan that ran on a
  schedule should not pay for the four it is not.

  A crash still leaves a receipt. An operation that throws has its failure written as
  closure:"failed" with the reason, because the hub learns what a run did from the receipt and
  a run that produced no receipt at all is a run nobody can account for.
*/

export interface Invocation {
  operation: OperationWord;
  inputPath: string;
  outputDir: string;
}

const USAGE = `babel-machine <${Object.keys(OPERATIONS).join("|")}> --input <file> --out <directory>`;

/**
 * Each operation parses its own input with its own schema; only the module knows the shape.
 *
 * The modules are loaded dynamically because the operation is selected from argv at runtime
 * and each one pulls in a dependency tree of its own — restic for `archive`, the engine's RPC
 * client for `explore` and `evaluate`. A static import graph would make every scheduled scan
 * load all five.
 */
const DISPATCH: Record<OperationWord, (raw: unknown, out: OutputSink) => Promise<Receipt>> = {
  scan: async (raw, out) => {
    const { ScanInputSchema, scan } = await import("./scan.ts");
    return scan(ScanInputSchema.parse(raw), out);
  },
  archive: async (raw, out) => {
    const { ArchiveInputSchema, archive } = await import("./archive.ts");
    return archive(ArchiveInputSchema.parse(raw), out, { roots: existingRoots, claim });
  },
  prepare: async (raw, out) => {
    const { PrepareInputSchema, prepare, digests } = await import("./prepare.ts");
    return prepare(PrepareInputSchema.parse(raw), out, { discover, digests });
  },
  explore: async (raw, out) => {
    const { ExploreInputSchema, explore } = await import("./explore.ts");
    return explore(ExploreInputSchema.parse(raw), out);
  },
  evaluate: async (raw, out) => {
    const { EvaluateInputSchema, evaluate } = await import("./evaluate.ts");
    return evaluate(EvaluateInputSchema.parse(raw), out);
  },
};

export function parseArgv(argv: readonly string[]): Invocation {
  let operation: OperationWord | null = null;
  let inputPath = process.env["BABEL_JOB_INPUT"]?.trim() ?? "";
  let outputDir = process.env["BABEL_JOB_OUTPUT_DIR"]?.trim() ?? "";
  for (let i = 0; i < argv.length; i++) {
    const argument = argv[i] ?? "";
    switch (argument) {
      case "--input":
      case "--out": {
        const value = argv[i + 1];
        if (value === undefined || value === "") throw new Error(`${argument} needs a path\n${USAGE}`);
        if (argument === "--input") inputPath = value;
        else outputDir = value;
        i++;
        continue;
      }
      default:
        break;
    }
    if (argument.startsWith("-")) throw new Error(`unknown flag ${argument}\n${USAGE}`);
    if (operation !== null) throw new Error(`unexpected argument ${argument}\n${USAGE}`);
    if (!(argument in OPERATIONS)) throw new Error(`unknown operation ${argument}\n${USAGE}`);
    operation = argument as OperationWord;
  }
  if (operation === null) throw new Error(`no operation named\n${USAGE}`);
  if (inputPath === "") throw new Error(`${operation} needs --input <file>\n${USAGE}`);
  if (outputDir === "") throw new Error(`${operation} needs --out <directory>\n${USAGE}`);
  return { operation, inputPath, outputDir };
}

/**
 * Runs one operation and returns its receipt, or writes a failed one and rethrows. Reading the
 * input is inside the attempt on purpose: a job whose input document is missing or malformed
 * is a run that failed, and it must leave the same receipt as one that failed later.
 */
export async function run(invocation: Invocation): Promise<Receipt> {
  const sink = directorySink(invocation.outputDir);
  const startedAt = new Date().toISOString();
  let raw: unknown = null;
  try {
    raw = await Bun.file(invocation.inputPath).json();
    return await DISPATCH[invocation.operation](raw, sink);
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
