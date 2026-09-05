import { describe, expect, test } from "bun:test";
import { LocalNameSchema, PluginIdSchema, PluginManifestSchema } from "@manifold/protocol";
import baselineManifest from "../atyrode.babel/manifest.json";
import sessionsManifest from "../atyrode.babel/sessions/manifest.json";
import {
  BASELINE_ID,
  CONFIGURE_ID,
  HOLD_SECONDS,
  LIST_RUNS_ACTION,
  REPORT_ARGV,
  REPORT_IDS,
  REPORTS_PANEL,
  RUN_ACTION,
  RUN_RECORDED_EVENT,
  ReportIdSchema,
  SESSIONS_ID,
  holdOpenArgv,
  shellQuote,
} from "../atyrode.babel/contract.ts";

/*
  The contract is spelled once in TypeScript, but a manifest is JSON the halves cannot
  import — so the two are held together here: the ids the manifests declare are the ids the
  halves use, and every name passes the grammar the engine's assembly will grade it by.
 */

describe("ids and names", () => {
  test("every plugin id parses and the sub-plugins share the baseline's prefix", () => {
    for (const id of [BASELINE_ID, SESSIONS_ID, CONFIGURE_ID]) {
      expect(PluginIdSchema.parse(id)).toBe(id);
    }
    expect(SESSIONS_ID.startsWith(`${BASELINE_ID}.`)).toBe(true);
    expect(CONFIGURE_ID.startsWith(`${BASELINE_ID}.`)).toBe(true);
  });

  test("local action and panel names have no dots (assembly refuses them)", () => {
    for (const name of [RUN_ACTION, LIST_RUNS_ACTION, REPORTS_PANEL]) {
      expect(LocalNameSchema.parse(name)).toBe(name);
    }
  });

  test("the manifests declare exactly what the contract spells", () => {
    const baseline = PluginManifestSchema.parse(baselineManifest);
    expect(baseline.id).toBe(BASELINE_ID);
    expect(baseline.contributes.panels).toEqual([]);
    expect(baseline.contributes.events.map((event) => event.id)).toEqual([RUN_RECORDED_EVENT]);
    expect(baseline.entry).toEqual({ server: true, web: "web.js" });

    const sessions = PluginManifestSchema.parse(sessionsManifest);
    expect(sessions.id).toBe(SESSIONS_ID);
    expect(sessions.contributes.panels.map((panel) => panel.id)).toEqual([REPORTS_PANEL]);
    expect(sessions.dependencies?.[BASELINE_ID]?.type).toBe("required");
    expect(sessions.entry).toEqual({ web: "web.js" });
    expect(sessions.capabilities).toEqual([]);
  });
});

describe("the reports", () => {
  test("the closed map is exactly the four read-only reports, and the enum is that map", () => {
    expect(REPORT_ARGV).toEqual({
      "archive-status": ["babel", "archive", "status"],
      "archive-fleet": ["babel", "archive", "fleet"],
      "storage-status": ["babel", "storage", "status"],
      version: ["babel", "version"],
    });
    expect([...REPORT_IDS].sort()).toEqual(Object.keys(REPORT_ARGV).sort() as typeof REPORT_IDS);
    expect(ReportIdSchema.safeParse("archive-push").success).toBe(false);
    expect(ReportIdSchema.safeParse("archive-verify").success).toBe(false);
  });
});

describe("shellQuote", () => {
  test("leaves a plain word alone and single-quotes anything else", () => {
    expect(shellQuote("babel")).toBe("babel");
    expect(shellQuote("--host=ws-linux")).toBe("--host=ws-linux");
    expect(shellQuote("/usr/local/bin/babel")).toBe("/usr/local/bin/babel");
    expect(shellQuote("two words")).toBe("'two words'");
    expect(shellQuote("$HOME")).toBe("'$HOME'");
    expect(shellQuote("a;b")).toBe("'a;b'");
    expect(shellQuote("back\\slash")).toBe("'back\\slash'");
    expect(shellQuote("")).toBe("''");
  });

  test("spells an embedded single quote as close-escape-reopen", () => {
    expect(shellQuote("it's")).toBe(String.raw`'it'\''s'`);
    expect(shellQuote("'")).toBe(String.raw`''\'''`);
  });

  test("what sh reads back is the original word, byte for byte", async () => {
    const words = ["babel", "two words", "it's", "$HOME", "a;b", "'", "", "tab\there", "*", "%s"];
    const line = words.map((word) => `printf '%s\\n' ${shellQuote(word)};`).join(" ");
    const proc = Bun.spawn(["sh", "-c", line], { stdout: "pipe" });
    const out = await new Response(proc.stdout).text();
    expect(await proc.exited).toBe(0);
    expect(out).toBe(`${words.join("\n")}\n`);
  });
});

describe("holdOpenArgv", () => {
  test("wraps the report so the tile shows the exit status and stays open", () => {
    expect(holdOpenArgv(REPORT_ARGV["archive-status"])).toEqual([
      "sh",
      "-c",
      "babel archive status; printf '\\n[babel exited %s]\\n' $?; exec sleep 600",
    ]);
    expect(HOLD_SECONDS).toBe(600);
  });

  test("quotes the report's words rather than interpolating them", () => {
    expect(holdOpenArgv(["babel", "a b", "it's"])[2]).toStartWith(
      String.raw`babel 'a b' 'it'\''s'; printf`,
    );
  });

  test("the wrapper runs the program, reports its exit status, then holds", async () => {
    // `sleep 600` is replaced by nothing here: `exec sleep` is the tail, so cut it off at the
    // last `;` and run what precedes it — the report line and the status line.
    const [sh, dashC, script] = holdOpenArgv(["sh", "-c", "echo hello; exit 3"]);
    const proc = Bun.spawn([sh, dashC, script.slice(0, script.lastIndexOf(";"))], {
      stdout: "pipe",
    });
    expect(await new Response(proc.stdout).text()).toBe("hello\n\n[babel exited 3]\n");
    expect(await proc.exited).toBe(0);
  });
});
