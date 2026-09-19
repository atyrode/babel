/*
  THE SECRET PREFLIGHT'S TWO FAILURE MODES, which pull in opposite directions (#339).

  A miss sends a credential to a model provider. A false positive redacts a digest, a UUID or a
  path in every transcript, which makes the corpus useless for analysis and gets the scanner
  turned off — so the negative half of each row below is as load-bearing as the positive half,
  and a near-miss is a realistic one: the right prefix at the wrong length, a value that is a
  template reference, a public certificate, a path to a credential rather than a credential.

  EVERY FIXTURE IS ASSEMBLED RATHER THAN WRITTEN WHOLE. A literal in the shape of a real
  credential format is what a repository's push protection matches on, so `"AKIA" + "…"` is not
  squeamishness: a test that cannot be pushed is a test that does not run.

  What is scanned is one NORMALIZED RECORD — canonical JSON on one line, or a line the normalizer
  could not parse behind its `!` marker — so every fixture here is in that shape. Scanning a bare
  sentence is not a case this code has.
*/

import { expect, test } from "bun:test";
import {
  PREFLIGHT_DETECTORS,
  SECRET_CLASSES,
  findSecrets,
  refusalMessage,
  secretScan,
  type SecretClass,
} from "./preflight.ts";

/** One normalized record holding `text`, which is how every one of these arrives. */
function record(text: string): string {
  return `${JSON.stringify({ text, type: "message" })}\n`;
}

function classesIn(text: string): readonly SecretClass[] {
  return findSecrets(text).map((found) => found.class);
}

/** A 20-character AWS access key id: the documented prefix plus 16 uppercase characters. */
const AWS_KEY = `${"AKIA"}IOSFODNN7SYNTH01`;
const GITHUB_TOKEN = `${"ghp_"}0synthetic1token2for3tests4only5abcd`;
const PASSWORD = `${"Xq7"}zP2mW9vT4kL8nR1sY6bH`;

/** PEM armour, assembled from its pieces for the reason every fixture here is: the header of a
 *  private key written out whole is a literal a scanner matches, here and in CI. */
const DASHES = "-".repeat(5);
const PRIVATE_LABEL = `RSA PRIVATE ${"KEY"}`;

function armour(edge: "BEGIN" | "END", label: string): string {
  return `${DASHES}${edge} ${label}${DASHES}`;
}

/**
 * ONE ROW PER DETECTOR CLASS: what it must catch, and the nearest thing it must leave alone.
 *
 * `positive` must yield the row's class; `negative` must yield NOTHING AT ALL, from any rule,
 * because a near-miss caught by a different detector is still a redaction the operator did not
 * want.
 */
interface Row {
  readonly class: SecretClass;
  readonly positive: string;
  readonly negative: string;
}

const ROWS: readonly Row[] = [
  {
    class: "aws-access-key-id",
    positive: `aws_access_key_id ${AWS_KEY}`,
    // The documented prefix at the wrong length is not an access key id.
    negative: `aws key ${"AKIA"}IOSFODNN7`,
  },
  {
    class: "google-api-key",
    positive: `maps key ${"AIza"}Sy0synthetic1key2for3tests4only5abc`,
    negative: `maps key ${"AIza"}Sy0short`,
  },
  {
    class: "github-token",
    positive: `gh auth login --with-token ${GITHUB_TOKEN}`,
    negative: `gh auth login --with-token ${"ghp_"}short`,
  },
  {
    class: "gitlab-token",
    positive: `CI job token ${"glpat-"}0synthetic1token2for3t`,
    negative: `CI job token ${"glpat-"}short`,
  },
  {
    class: "slack-token",
    positive: `slack ${"xoxb-"}2401234567890-2401234567891-syntheticonly0123456789`,
    negative: `slack ${"xoxb-"}12345`,
  },
  {
    class: "model-api-key",
    positive: `OPENAI_API_KEY ${"sk-"}proj0synthetic1key2for3tests4only5abcdefg`,
    negative: `OPENAI_API_KEY ${"sk-"}unset`,
  },
  {
    class: "stripe-api-key",
    positive: `stripe ${"sk"}_live_0synthetic1key2for3te`,
    negative: `stripe ${"sk"}_live_short`,
  },
  {
    class: "npm-token",
    positive: `npm publish with ${"npm_"}0synthetic1token2for3tests4only5abcd`,
    negative: `npm publish with ${"npm_"}short`,
  },
  {
    class: "jwt",
    // The header segment declares itself: `eyJ` is base64url for `{"`.
    positive: `id_token ${"eyJ"}hbGciOiJIUzI1NiJ9.eyJzdWIiOiJzeW50aGV0aWMifQ.c3ludGhldGljc2lnbmF0dXJl`,
    // Three dot-separated base64url runs with no JSON header are three words.
    negative: `three words aGVsbG8gd29ybGQ.bm90IGEgand0.c2lnbmF0dXJl`,
  },
  {
    class: "bearer-token",
    positive: `Authorization: Bearer ${PASSWORD}fT3wQ8jZ5xN2`,
    // What a transcript actually holds most of the time: the name of a variable, not its value.
    negative: `Authorization: Bearer $${"{GITHUB_TOKEN}"}`,
  },
  {
    class: "credential-assignment",
    // Assembled, like every other fixture here: a literal credential shape in a committed file
    // is what the repository's own pre-commit scan exists to refuse, and it refused this one.
    positive: `export DEPLOY_SECRET=${"9f8a"}7b6c5d4e3f2a1b0c9d8e7f6a5b4c`,
    // A field naming where a credential lives is not the credential.
    negative: `export DEPLOY_SECRET_FILE=/run/keys/deploy`,
  },
  {
    class: "connection-string",
    positive: `psql postgres://deploy:${PASSWORD}@db.internal:5432/babel`,
    negative: `psql postgres://db.internal:5432/babel`,
  },
  {
    class: "private-key-block",
    positive: `pasted key\n${armour("BEGIN", PRIVATE_LABEL)}\nMIIEogIBAAKCAQEAsynthetic\n${armour("END", PRIVATE_LABEL)}`,
    // Armour states its own content type: a certificate is public by declaration.
    negative: `${armour("BEGIN", "CERTIFICATE")}\nMIIBkTCB+wIBADANBgkqhkiG9w0BAQQFADAUMRIwEAYDVQQDEwlUZXN0\n${armour("END", "CERTIFICATE")}`,
  },
  {
    class: "high-entropy-string",
    positive: `curl -H "x-internal: Zp4Kq9Lm2Xv7Bn3Rt8Yw1Hd6Fj5Gs0Ac"`,
    // Long, mixed and dense enough to fail a naive entropy floor, and public by purpose.
    negative: `commit 4f9c1b7e2a8d6350e1c4b9a7f2d8e6c3b5a09172 in babel/machine/prepare.ts`,
  },
];

test("every detector class catches a realistic credential and leaves its near miss alone", () => {
  // The table and the rule set must not drift apart: a class added to one and not the other is
  // either an unreviewed detector or an untested one.
  expect(ROWS.map((row) => row.class).sort()).toEqual(
    SECRET_CLASSES.map((detector) => detector.name).sort(),
  );
  for (const row of ROWS) {
    expect(classesIn(record(row.positive))).toContain(row.class);
    expect(findSecrets(record(row.negative))).toEqual([]);
  }
});

test("what a transcript is mostly made of is not a secret", () => {
  // Content addressing means the corpus is saturated with digests, ids and paths — Babel's own
  // transcripts above all. A scanner that redacted these would have made the corpus unreadable
  // while catching nothing.
  const ordinary = record(
    [
      "run 550e8400-e29b-41d4-a716-446655440000 read babel/store/schema.ts at",
      "sha256:9c1185a5c5e9fc54612808977ee8f548b2258d31e8cb8bd5b4b0b2a1c1d0e0f1 and wrote",
      "prep-0f1e2d3c4b5a69788796a5b4c3d2e1f00112233445566778899aabbccddeeff0 at",
      "2026-09-13T07:41:22.518Z, and an inline image",
      "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8AAAAMBAQCS9G2MAAAAAElFTkSuQmCC",
      "and /home/alex/babel/babel/machine/preflight.ts:1",
    ].join(" "),
  );
  expect(findSecrets(ordinary)).toEqual([]);
  expect(secretScan().redact(ordinary, 1)).toBe(ordinary);
});

test("a redaction keeps the locator, drops the value, and leaves a record a record", () => {
  const original = record(`aws_access_key_id ${AWS_KEY} for the deploy role`);
  const scan = secretScan();
  const served = scan.redact(original, 7);

  expect(served).not.toContain(AWS_KEY);
  const site = scan.report().sites[0];
  expect(site).toEqual({
    class: "aws-access-key-id",
    line: 7,
    offset: original.indexOf(AWS_KEY),
    length: AWS_KEY.length,
  });
  // The marker carries the class and the locator and nothing else, and the locator is the one
  // the report names — the material and the receipt agree on where the original is.
  expect(served).toContain(
    `[[babel-redacted:aws-access-key-id@7:${String(site?.offset)}+${String(site?.length)}]]`,
  );
  // The material's contract survives redaction: one parseable canonical record per line.
  expect(served.endsWith("\n")).toBe(true);
  expect(served.split("\n")).toHaveLength(2);
  const parsed = JSON.parse(served.trimEnd()) as { text: string };
  expect(parsed.text).toContain("for the deploy role");

  // A second pass is a no-op: a marker is Babel's own output and is never re-examined, or the
  // locator inside it would be destroyed by the pass that found it.
  expect(secretScan().redact(served, 7)).toBe(served);
});

test("a refusal names classes and counts, and cannot contain the value", () => {
  const scan = secretScan();
  scan.redact(record(`aws ${AWS_KEY} and again ${"AKIA"}IOSFODNN7SYNTH02`), 1);
  scan.redact(record(`gh auth login --with-token ${GITHUB_TOKEN}`), 2);
  const report = scan.report();
  const message = refusalMessage("omp/a1b2c3", report);

  expect(message).toBe(
    "secret preflight refused omp/a1b2c3: aws-access-key-id (2), github-token (1);" +
      " values are never named",
  );
  for (const secret of [AWS_KEY, GITHUB_TOKEN, "IOSFODNN7SYNTH02"]) {
    expect(message).not.toContain(secret);
    // Nor in the report the receipt carries, which is the same claim about a different document.
    expect(JSON.stringify(report)).not.toContain(secret);
  }
  expect(report.redactions).toBe(3);
  expect(report.records).toBe(2);
});

test("a torn line is scanned, because a torn capture must not be the way past this gate", () => {
  // The normalizer keeps an unparseable line verbatim behind `!`; there is no JSON structure to
  // protect there, so the whole line is scannable.
  const torn = `!{"type":"message","text":"aws_access_key_id ${AWS_KEY}`;
  expect(classesIn(torn)).toEqual(["aws-access-key-id"]);
  expect(secretScan().redact(torn, 3)).not.toContain(AWS_KEY);
});

test("the rule set names itself, so a receipt saying `scanned` says scanned by what", () => {
  expect(PREFLIGHT_DETECTORS).toBe("babel.preflight.detectors/1");
});
