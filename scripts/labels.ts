#!/usr/bin/env bun
/**
 * The label inventory check. `bun scripts/labels.ts [--check|--apply] [--repo owner/name]`.
 *
 * `.github/labels.yml` declares every label the repository is supposed to have; this script
 * compares it with the live tracker and, with `--apply`, makes the live tracker match. A live
 * label that the file does not declare is reported and never deleted: deletion strips the label
 * off every issue that carries it, which is a decision, not a sync. Meaning lives in
 * `docs/TRIAGE.md` §Label model — this script only proves the inventory.
 */
import { $ } from "bun";
import { join } from "node:path";

const INVENTORY = ".github/labels.yml";
/** GitHub rejects a longer label description with HTTP 422. */
const MAX_DESCRIPTION = 100;
const repoRoot = join(import.meta.dir, "..");

interface Label {
  readonly name: string;
  readonly color: string;
  readonly description: string;
}

type Action = "create" | "update" | "extra";

interface Row {
  readonly action: Action;
  readonly name: string;
  readonly detail: string;
}

function stringField(entry: Record<string, unknown>, key: string, where: string): string {
  const value = entry[key];
  if (typeof value !== "string" || value.length === 0) {
    throw new Error(`${INVENTORY}: ${where} has no ${key}`);
  }
  return value;
}

/**
 * The declared inventory, with every malformed entry a hard error rather than a silent skip.
 * The description cap is the GitHub API's own (HTTP 422 "description is too long"): checking it
 * here is what keeps `--apply` from writing half the inventory and then throwing on the rest.
 */
function readInventory(text: string): readonly Label[] {
  const parsed: unknown = Bun.YAML.parse(text);
  if (!Array.isArray(parsed)) throw new Error(`${INVENTORY} is not a list of labels`);
  const labels = parsed.map((entry, index) => {
    if (typeof entry !== "object" || entry === null || Array.isArray(entry)) {
      throw new Error(`${INVENTORY}: entry ${String(index + 1)} is not a mapping`);
    }
    const record = entry as Record<string, unknown>;
    const where = `entry ${String(index + 1)}`;
    const name = stringField(record, "name", where);
    const color = stringField(record, "color", name);
    if (!/^[0-9a-f]{6}$/i.test(color)) {
      throw new Error(`${INVENTORY}: ${name} has color "${color}", want six hex digits without #`);
    }
    const description = stringField(record, "description", name);
    if (description.length > MAX_DESCRIPTION) {
      throw new Error(
        `${INVENTORY}: ${name} has a ${String(description.length)}-character description; the GitHub API accepts ${String(MAX_DESCRIPTION)}`,
      );
    }
    return { name, color, description };
  });
  const seen = new Set<string>();
  for (const label of labels) {
    if (seen.has(label.name)) throw new Error(`${INVENTORY}: ${label.name} is declared twice`);
    seen.add(label.name);
  }
  return labels;
}

/** Live labels, narrowed through `unknown`: `gh` output is data, not a typed contract. */
function readLive(json: string): readonly Label[] {
  const parsed: unknown = JSON.parse(json);
  if (!Array.isArray(parsed)) throw new Error("gh label list did not return a list");
  return parsed.map((entry) => {
    const record = entry as Record<string, unknown>;
    const name = record["name"];
    const color = record["color"];
    const description = record["description"];
    if (typeof name !== "string") throw new Error("gh label list returned a nameless label");
    return {
      name,
      color: typeof color === "string" ? color : "",
      description: typeof description === "string" ? description : "",
    };
  });
}

function diff(declared: readonly Label[], live: readonly Label[]): readonly Row[] {
  const byName = new Map(live.map((label) => [label.name, label]));
  const rows: Row[] = [];
  for (const want of declared) {
    const have = byName.get(want.name);
    if (have === undefined) {
      rows.push({
        action: "create",
        name: want.name,
        detail: `#${want.color} ${want.description}`,
      });
      continue;
    }
    const differences: string[] = [];
    if (have.color.toLowerCase() !== want.color.toLowerCase()) {
      differences.push(`color #${have.color} → #${want.color}`);
    }
    if (have.description !== want.description) {
      differences.push(`description "${have.description}" → "${want.description}"`);
    }
    if (differences.length > 0) {
      rows.push({ action: "update", name: want.name, detail: differences.join("; ") });
    }
  }
  const names = new Set(declared.map((label) => label.name));
  for (const have of live) {
    if (names.has(have.name)) continue;
    rows.push({ action: "extra", name: have.name, detail: `live but undeclared; not deleted` });
  }
  return rows;
}

function report(rows: readonly Row[]): number {
  const pending = rows.filter((row) => row.action !== "extra");
  if (rows.length > 0) {
    console.log("| action | label | detail |");
    console.log("| --- | --- | --- |");
    for (const row of rows) console.log(`| ${row.action} | \`${row.name}\` | ${row.detail} |`);
    console.log("");
  }
  const extras = rows.length - pending.length;
  console.log(
    `Labels: ${String(pending.length)} to apply, ${String(extras)} undeclared live label(s)`,
  );
  return pending.length > 0 ? 1 : 0;
}

const args = process.argv.slice(2);
const apply = args.includes("--apply");
const repoIndex = args.indexOf("--repo");
const repo = repoIndex === -1 ? undefined : args[repoIndex + 1];
if (repoIndex !== -1 && repo === undefined) throw new Error("--repo needs owner/name");
const repoArgs = repo === undefined ? [] : ["--repo", repo];

const declared = readInventory(await Bun.file(join(repoRoot, INVENTORY)).text());

async function live(): Promise<readonly Label[]> {
  const result = await $`gh label list ${repoArgs} --limit 200 --json name,color,description`
    .quiet()
    .nothrow();
  if (result.exitCode !== 0) {
    throw new Error(result.stderr.toString().trim() || "gh label list failed");
  }
  return readLive(result.text());
}

if (apply) {
  for (const row of diff(declared, await live())) {
    if (row.action === "extra") continue;
    const want = declared.find((label) => label.name === row.name);
    if (want === undefined) continue;
    const result =
      await $`gh label create ${repoArgs} ${want.name} --color ${want.color} --description ${want.description} --force`
        .quiet()
        .nothrow();
    if (result.exitCode !== 0) {
      throw new Error(`gh label create ${want.name}: ${result.stderr.toString().trim()}`);
    }
    console.log(`${row.action}: ${want.name}`);
  }
}

process.exit(report(diff(declared, await live())));
